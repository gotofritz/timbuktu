# Subplan 03: Preprocessing & Chunking

## Goal

Extract plain text from supported document formats, split into overlapping
chunks, and compute SHA256 hashes. No storage or embedding here — pure
transformation pipeline.

## Deliverables

- `Extractor` interface + dispatcher (picks extractor by MIME type)
- Text extractors: Markdown, plain text, HTML, PDF
- Token-aware chunker (~800 token target, ~100 token overlap)
- SHA256 helper
- `tbuk preprocess <path>` command (prints extracted text + chunk boundaries)
- Unit tests ≥ 85 % coverage

## Package Layout

```
internal/preprocess/
  extractor.go     ← Extractor interface, DetectMIME(), NewExtractor()
  markdown.go      ← strip fences/frontmatter, return plain text
  plaintext.go     ← passthrough
  html.go          ← strip tags, decode entities
  pdf.go           ← extract text page-by-page
  preprocess_test.go

internal/chunking/
  chunker.go       ← Chunker struct, Chunk() method
  token.go         ← CountTokens() (char/4 approximation)
  chunking_test.go
```

## Interfaces

```go
// Extractor converts raw bytes of a document into plain text.
type Extractor interface {
    Extract(ctx context.Context, r io.Reader) (string, error)
}

// Chunk is a slice of text with position metadata.
type Chunk struct {
    Index      int
    Text       string
    TokenCount int
    StartByte  int
    EndByte    int
}

// Chunker splits text into overlapping chunks.
type Chunker struct {
    Size    int // target token count per chunk
    Overlap int // overlap in tokens between adjacent chunks
}

func (c *Chunker) Split(text string) []Chunk
```

## Token Counting

Use character-based approximation: `tokens ≈ len(text) / 4`.
No external tokenizer dependency for MVP. Document the approximation clearly.
Can swap in `tiktoken-go` later without changing the interface.

## Chunking Algorithm

1. Split text into sentences (split on `. `, `\n\n`, `! `, `? `)
2. Greedily accumulate sentences until `Size` tokens reached
3. Emit chunk; backtrack `Overlap` tokens for next chunk start
4. Last chunk emits whatever remains (may be smaller than `Size`)

## SHA256 Helper

```go
// internal/preprocess/sha256.go
func HashReader(r io.Reader) (string, error)  // hex-encoded SHA256
func HashFile(path string) (string, error)
```

## PDF Extraction

Use `github.com/ledongthuc/pdf` (pure Go, no CGo). Concatenate page texts with
`\n\n` between pages.

## HTML Extraction

Use `golang.org/x/net/html` tokenizer to strip tags. Decode HTML entities.
Collapse whitespace runs.

## Markdown Extraction

Strip YAML frontmatter (`---` blocks). Strip code fence markers but keep
content. Strip inline formatting (`**`, `_`, `` ` ``). Keep headings as plain
text with the `#` markers removed.

## `tbuk preprocess` Command

```
tbuk preprocess <path> [--format json|text]
```

Text output (default):
```
=== /path/to/file.md ===
MIME: text/markdown
SHA256: abc123...
Chunks: 4

--- Chunk 0 (212 tokens) ---
...text...
```

JSON output:
```json
{
  "path": "...",
  "mime": "text/markdown",
  "sha256": "...",
  "chunks": [{"index":0,"tokens":212,"text":"..."}]
}
```

## Dependencies

| Package | Version | Reason |
|---------|---------|--------|
| `github.com/ledongthuc/pdf` | latest | Pure-Go PDF text extraction |
| `golang.org/x/net` | latest | HTML tokenizer |

## Tests

- `TestMarkdownExtractor` — strips frontmatter, code fences, inline markup
- `TestHTMLExtractor` — strips tags, decodes entities
- `TestPlainTextExtractor` — passthrough
- `TestPDFExtractor` — extracts text from a small test PDF fixture
- `TestChunker_basic` — single chunk when text is small
- `TestChunker_overlap` — verify overlap bytes are re-included
- `TestChunker_empty` — empty string → zero chunks
- `TestHashFile` — known file → expected SHA256

## PR Scope

One PR. Depends on Subplan 01 (go.mod). No storage writes.
