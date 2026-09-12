package eval

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
)

// LSAEmbedder embeds text by projecting its TF-IDF vector onto the leading
// singular directions of a corpus it was fitted on (latent semantic analysis).
//
// It exists because the fixture corpus has to be embedded by something before
// a real model has recorded it, and a hash-seeded pseudo-vector is the wrong
// something: it carries no semantics, so a ranking it pins fails identically
// whether a change was a regression or an improvement. LSA is a weak model
// with real distributional semantics — enough to pin a ranking, not enough to
// decide anything about retrieval quality. Whatever records a fixture names
// itself in that fixture's header, this included.
type LSAEmbedder struct {
	// terms maps a token to its row in the term-document matrix. Assignment is
	// by sorted order, never by map iteration, so two fits agree.
	terms map[string]int
	idf   []float64
	// proj is the V x k projection A·V·diag(1/lambda), so a TF-IDF row vector
	// times proj lands in the same space as the fitted documents.
	proj [][]float64
	dim  int
}

// FitLSA fits an embedder on corpus, reducing to at most dim dimensions. The
// corpus rank is a hard ceiling: a three-passage corpus has no fourth
// direction to project onto, so Dimension may come back smaller than asked.
func FitLSA(corpus []string, dim int) (*LSAEmbedder, error) {
	if dim < 1 {
		return nil, fmt.Errorf("eval: FitLSA: dimension %d must be at least 1", dim)
	}
	docs := make([][]string, 0, len(corpus))
	for _, text := range corpus {
		if tokens := tokenize(text); len(tokens) > 0 {
			docs = append(docs, tokens)
		}
	}
	if len(docs) == 0 {
		return nil, errors.New("eval: FitLSA: corpus has no text to fit on")
	}

	terms, idf := fitVocabulary(docs)
	// a is the term-document matrix, each column a unit-length TF-IDF vector.
	a := make([][]float64, len(terms))
	for t := range a {
		a[t] = make([]float64, len(docs))
	}
	for d, tokens := range docs {
		for term, weight := range tfidf(tokens, terms, idf) {
			a[term][d] = weight
		}
	}

	values, vectors := eigenSymmetric(gram(a, len(docs)))
	keep := rank(values, dim)
	if keep == 0 {
		return nil, errors.New("eval: FitLSA: corpus has no variance to project onto")
	}

	proj := make([][]float64, len(terms))
	for t := range proj {
		proj[t] = make([]float64, keep)
		for j := 0; j < keep; j++ {
			var sum float64
			for d := range docs {
				sum += a[t][d] * vectors[d][j]
			}
			proj[t][j] = sum / values[j]
		}
	}
	return &LSAEmbedder{terms: terms, idf: idf, proj: proj, dim: keep}, nil
}

// Embed projects each text into the fitted space, L2-normalised so a dot
// product is a cosine — which is what the vector searcher scores with. Text
// sharing no vocabulary with the corpus embeds to zero rather than to noise.
func (e *LSAEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		weights := tfidf(tokenize(text), e.terms, e.idf)
		projected := make([]float64, e.dim)
		for term, weight := range weights {
			for j := range projected {
				projected[j] += weight * e.proj[term][j]
			}
		}
		out[i] = normalize(projected)
	}
	return out, nil
}

// Dimension reports the fitted dimension, which may be below the one asked for.
func (e *LSAEmbedder) Dimension() int { return e.dim }

// tokenize lowercases and splits on anything that is not a letter or digit.
func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// fitVocabulary assigns every term a row by sorted order and computes its
// smoothed inverse document frequency.
func fitVocabulary(docs [][]string) (map[string]int, []float64) {
	df := make(map[string]int)
	for _, tokens := range docs {
		for term := range uniqueTerms(tokens) {
			df[term]++
		}
	}
	sorted := make([]string, 0, len(df))
	for term := range df {
		sorted = append(sorted, term)
	}
	sort.Strings(sorted)

	terms := make(map[string]int, len(sorted))
	idf := make([]float64, len(sorted))
	for i, term := range sorted {
		terms[term] = i
		idf[i] = math.Log(float64(len(docs))/float64(df[term])) + 1
	}
	return terms, idf
}

func uniqueTerms(tokens []string) map[string]struct{} {
	seen := make(map[string]struct{}, len(tokens))
	for _, term := range tokens {
		seen[term] = struct{}{}
	}
	return seen
}

// tfidf returns the unit-length TF-IDF weights of tokens, keyed by term row.
// Terms outside the fitted vocabulary are dropped: they carry no direction.
func tfidf(tokens []string, terms map[string]int, idf []float64) map[int]float64 {
	weights := make(map[int]float64)
	for _, token := range tokens {
		if row, ok := terms[token]; ok {
			weights[row]++
		}
	}
	var norm float64
	for row, count := range weights {
		w := (1 + math.Log(count)) * idf[row]
		weights[row] = w
		norm += w * w
	}
	if norm == 0 {
		return weights
	}
	norm = math.Sqrt(norm)
	for row := range weights {
		weights[row] /= norm
	}
	return weights
}

// gram returns the n x n matrix A^T·A, whose eigenvectors are A's right
// singular vectors. It is computed rather than the full SVD because a corpus
// has far more terms than documents, and this side is the small one.
func gram(a [][]float64, n int) [][]float64 {
	g := make([][]float64, n)
	for i := range g {
		g[i] = make([]float64, n)
	}
	for _, row := range a {
		for i := 0; i < n; i++ {
			if row[i] == 0 {
				continue
			}
			for j := i; j < n; j++ {
				g[i][j] += row[i] * row[j]
			}
		}
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			g[j][i] = g[i][j]
		}
	}
	return g
}

// eigenSymmetric diagonalises a symmetric matrix by cyclic Jacobi rotations,
// returning eigenvalues in descending order and their eigenvectors as columns.
// Every eigenvector's largest-magnitude component is made positive, so the
// sign a rotation happened to land on cannot change a recorded vector.
func eigenSymmetric(m [][]float64) ([]float64, [][]float64) {
	n := len(m)
	a := make([][]float64, n)
	v := make([][]float64, n)
	for i := range a {
		a[i] = append([]float64(nil), m[i]...)
		v[i] = make([]float64, n)
		v[i][i] = 1
	}

	const maxSweeps = 100
	for sweep := 0; sweep < maxSweeps && offDiagonal(a) > 1e-18; sweep++ {
		for p := 0; p < n-1; p++ {
			for q := p + 1; q < n; q++ {
				if math.Abs(a[p][q]) < 1e-18 {
					continue
				}
				theta := (a[q][q] - a[p][p]) / (2 * a[p][q])
				t := math.Copysign(1, theta) / (math.Abs(theta) + math.Sqrt(theta*theta+1))
				c := 1 / math.Sqrt(t*t+1)
				s := t * c
				rotate(a, v, p, q, c, s)
			}
		}
	}

	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return a[order[i]][order[i]] > a[order[j]][order[j]] })

	values := make([]float64, n)
	vectors := make([][]float64, n)
	for i := range vectors {
		vectors[i] = make([]float64, n)
	}
	for j, from := range order {
		values[j] = a[from][from]
		for i := 0; i < n; i++ {
			vectors[i][j] = v[i][from]
		}
	}
	fixSigns(vectors)
	return values, vectors
}

// rotate applies one Jacobi rotation to a and accumulates it into v.
func rotate(a, v [][]float64, p, q int, c, s float64) {
	n := len(a)
	app, aqq, apq := a[p][p], a[q][q], a[p][q]
	a[p][p] = c*c*app - 2*s*c*apq + s*s*aqq
	a[q][q] = s*s*app + 2*s*c*apq + c*c*aqq
	a[p][q], a[q][p] = 0, 0
	for i := 0; i < n; i++ {
		if i != p && i != q {
			aip, aiq := a[i][p], a[i][q]
			a[i][p], a[p][i] = c*aip-s*aiq, c*aip-s*aiq
			a[i][q], a[q][i] = s*aip+c*aiq, s*aip+c*aiq
		}
		vip, viq := v[i][p], v[i][q]
		v[i][p] = c*vip - s*viq
		v[i][q] = s*vip + c*viq
	}
}

func offDiagonal(a [][]float64) float64 {
	var sum float64
	for i := range a {
		for j := range a[i] {
			if i != j {
				sum += a[i][j] * a[i][j]
			}
		}
	}
	return sum
}

// fixSigns orients each column so its largest-magnitude entry is positive,
// breaking ties towards the lowest row.
func fixSigns(vectors [][]float64) {
	for j := range vectors {
		lead := 0
		for i := range vectors {
			if math.Abs(vectors[i][j]) > math.Abs(vectors[lead][j])+1e-15 {
				lead = i
			}
		}
		if vectors[lead][j] < 0 {
			for i := range vectors {
				vectors[i][j] = -vectors[i][j]
			}
		}
	}
}

// rank counts the leading eigenvalues carrying real variance, capped at dim.
func rank(values []float64, dim int) int {
	if len(values) == 0 || values[0] <= 0 {
		return 0
	}
	cutoff := values[0] * 1e-9
	keep := 0
	for _, v := range values {
		if v <= cutoff || keep == dim {
			break
		}
		keep++
	}
	return keep
}

// normalize scales v to unit length, leaving an all-zero vector alone rather
// than dividing by nothing.
func normalize(v []float64) []float32 {
	var norm float64
	for _, x := range v {
		norm += x * x
	}
	out := make([]float32, len(v))
	if norm == 0 {
		return out
	}
	norm = math.Sqrt(norm)
	for i, x := range v {
		out[i] = float32(x / norm)
	}
	return out
}
