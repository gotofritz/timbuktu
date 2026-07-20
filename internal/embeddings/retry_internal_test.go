package embeddings

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func fastRetry() retryPolicy { return retryPolicy{maxRetries: 2, base: time.Millisecond} }

func newReqFn(t *testing.T, url string) func() (*http.Request, error) {
	t.Helper()
	return func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	}
}

func TestDoWithRetry_retriesThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := doWithRetry(context.Background(), srv.Client(), fastRetry(), newReqFn(t, srv.URL))
	if err != nil {
		t.Fatalf("doWithRetry: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
}

func TestDoWithRetry_exhaustsReturnsLastResponse(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	resp, err := doWithRetry(context.Background(), srv.Client(), fastRetry(), newReqFn(t, srv.URL))
	if err != nil {
		t.Fatalf("doWithRetry: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	// maxRetries=2 → 3 total attempts, then hand the last response back so the
	// caller builds its EmbedError from the body.
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
}

func TestDoWithRetry_transportErrorExhausts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listening → connection refused every attempt

	_, err := doWithRetry(context.Background(), http.DefaultClient, fastRetry(), newReqFn(t, url))
	if err == nil {
		t.Fatal("expected transport error, got nil")
	}
}

func TestDoWithRetry_contextCancelDuringBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	slow := retryPolicy{maxRetries: 2, base: time.Hour}
	newReq := func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := doWithRetry(ctx, srv.Client(), slow, newReq)
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"2", 2 * time.Second},
		{"0", 0},
		{"bad", 0},
		{"-5", 0},
	}
	for _, tt := range tests {
		if got := parseRetryAfter(tt.in); got != tt.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestOpenAIEmbedder_retriesTransient(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1,0,0],"index":0}]}`))
	}))
	defer srv.Close()

	emb := &openAIEmbedder{
		baseURL: srv.URL, model: "m", apiKey: "k", dimension: 3,
		client: srv.Client(), retry: fastRetry(),
	}
	out, err := emb.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(out) != 1 || len(out[0]) != 3 {
		t.Fatalf("out = %v, want one 3-dim vector", out)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
}

func TestOllamaEmbedder_retriesTransient(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"embeddings":[[1,0,0]]}`))
	}))
	defer srv.Close()

	emb := &ollamaEmbedder{
		baseURL: srv.URL, model: "m", dimension: 3,
		client: srv.Client(), retry: fastRetry(),
	}
	out, err := emb.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(out) != 1 || len(out[0]) != 3 {
		t.Fatalf("out = %v, want one 3-dim vector", out)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{http.StatusOK, false},
		{http.StatusBadRequest, false},
		{http.StatusNotFound, false},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusGatewayTimeout, true},
	}
	for _, tt := range tests {
		if got := isRetryable(tt.status); got != tt.want {
			t.Errorf("isRetryable(%d) = %v, want %v", tt.status, got, tt.want)
		}
	}
}
