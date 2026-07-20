package embeddings

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// retryPolicy bounds transient-failure retries for provider HTTP calls.
type retryPolicy struct {
	maxRetries int           // retries after the first attempt
	base       time.Duration // first backoff; doubles each retry
}

// defaultRetryPolicy: 2 retries (3 attempts total) with exponential backoff.
// Bulk ingest against hosted providers routinely trips 429s; a few backed-off
// retries turn a failed file into a completed one without a manual re-run.
func defaultRetryPolicy() retryPolicy {
	return retryPolicy{maxRetries: 2, base: 500 * time.Millisecond}
}

// isRetryable reports whether an HTTP status is a transient provider failure
// worth retrying: rate limits (429) and server errors (5xx).
func isRetryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

// parseRetryAfter reads a Retry-After header value expressed in seconds. It
// returns 0 for empty, non-numeric, or non-positive values (HTTP-date form is
// not honoured — providers we target send seconds).
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// doWithRetry issues the request built by newReq, retrying transient failures
// (connection errors, 429, 5xx) per policy with exponential backoff, honouring
// a Retry-After header when present. newReq is called once per attempt so the
// request body is fresh each time. On success it returns the response with its
// body open for the caller to read and close. When retries are exhausted it
// returns the last response (so the caller builds its own status error) or, if
// every attempt failed at the transport layer, the last transport error.
func doWithRetry(ctx context.Context, client *http.Client, policy retryPolicy, newReq func() (*http.Request, error)) (*http.Response, error) {
	var lastErr error
	var wait time.Duration
	for attempt := 0; attempt <= policy.maxRetries; attempt++ {
		if attempt > 0 {
			if wait <= 0 {
				wait = policy.base << (attempt - 1)
			}
			if err := sleepCtx(ctx, wait); err != nil {
				return nil, err
			}
		}

		req, err := newReq()
		if err != nil {
			return nil, err
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			wait = 0
			continue
		}
		if attempt < policy.maxRetries && isRetryable(resp.StatusCode) {
			wait = parseRetryAfter(resp.Header.Get("Retry-After"))
			_ = resp.Body.Close()
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

// sleepCtx waits for d or until ctx is cancelled, returning ctx.Err() on cancel.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
