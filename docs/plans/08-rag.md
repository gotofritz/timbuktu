# Subplan 08: RAG & Ask Command

## Goal

Implement the retrieval-augmented generation pipeline: embed query, retrieve
top-K chunks with citations, render a prompt template, stream LLM response.
Expose via `tbuk ask` and `tbuk template` commands.

## Deliverables

- `Retriever` struct: embed query → fetch chunks → format citations
- Prompt template system (disk-based, Go `text/template`, YAML manifest)
- Built-in `qa` template installed by `tbuk init`
- `tbuk ask` command with streaming output
- `tbuk template list` and `tbuk template edit` commands
- Unit tests ≥ 85 % coverage

## Package Layout

```
internal/retrieval/
  retrieval.go     ← Retriever, RetrievedChunk (with citation)
  retrieval_test.go

internal/prompts/
  prompts.go       ← TemplateDir, Load(), List(), Render()
  manifest.go      ← Manifest struct, YAML parsing
  prompts_test.go
```

## Retrieval Pipeline

```go
type RetrievedChunk struct {
    ChunkID    int64
    DocumentID int64
    Path       string
    Title      string
    ChunkIndex int
    Text       string
    Score      float64
    Citation   string  // "path §chunkIndex"
}

type Retriever struct {
    searcher *search.Searcher
}

func (r *Retriever) Retrieve(ctx context.Context, query string, topK int, meta map[string]string) ([]RetrievedChunk, error)
```

Retrieve runs `Searcher.Hybrid()` then maps results to `RetrievedChunk`
with `Citation = fmt.Sprintf("%s §%d", path, chunkIndex)`.

## Prompt Template System

### Directory Structure on Disk

```
~/.tbuk/prompts/
  qa/
    manifest.yaml
    system.tmpl
    user.tmpl
  anki/
    manifest.yaml
    system.tmpl
    user.tmpl
```

### Manifest Schema

```yaml
name: qa
description: "Question-answering over retrieved context."
model: ""            # empty = use config default
temperature: 0.2
max_tokens: 2048
retrieval:
  top_k: 5
  max_tokens: 8000   # context budget for chunks
variables:           # user-definable variables with defaults
  language:
    default: "English"
output: text         # text | markdown | markdown-table | json
```

### Template Variables

Both `.system.tmpl` and `.user.tmpl` receive:

```go
type TemplateData struct {
    Question  string
    Chunks    []RetrievedChunk
    Variables map[string]string // manifest defaults merged with CLI --var flags
}
```

### Built-in `qa` Templates

`system.tmpl`:
```
You are a helpful assistant that answers questions using only the provided context.
If the context does not contain the answer, say so clearly.
```

`user.tmpl`:
```
Question:

{{ .Question }}

Context:

{{ range .Chunks }}
Source: {{ .Citation }}

{{ .Text }}

{{ end }}
```

### Loader

```go
type TemplateDir struct {
    Root string // ~/.tbuk/prompts
}

func (td *TemplateDir) Load(name string) (*Template, error)
func (td *TemplateDir) List() ([]Manifest, error)
func (t *Template) Render(data TemplateData) (system, user string, err error)
```

## `tbuk ask` Command

```
tbuk ask <question> [--template qa] [--var key=value]... [--top 5] [--no-stream]
```

Flow:
1. Load template (default: `qa`)
2. `Retriever.Retrieve(query, topK=manifest.Retrieval.TopK)`
3. Build `TemplateData` with CLI `--var` overrides merged over manifest defaults
4. `template.Render(data)` → system + user prompts
5. `llm.Chat([{system,...},{user,...}])` → token channel
6. Stream tokens to stdout; on `Done`, print newline
7. Print citations block:
   ```
   Sources:
     [1] /docs/README.md §2
     [2] /docs/ARCHITECTURE.md §5
   ```

`--no-stream`: buffer all tokens, print at once (useful for piping).

## `tbuk template` Subcommands

```
tbuk template list
tbuk template edit <name>   # opens $EDITOR
tbuk template show <name>   # print manifest + templates to stdout
```

## Tests

- `TestRetriever_returnsTopK` — mock searcher, verify K chunks returned
- `TestRetriever_emptyCitation` — path + chunkIndex formatted correctly
- `TestManifest_defaults` — missing fields get default values
- `TestManifest_badYAML` — error on malformed manifest
- `TestTemplateRender_qa` — built-in qa template renders expected output
- `TestTemplateRender_customVar` — `--var language=French` appears in output
- `TestTemplateList` — returns all template names from dir

## Dependencies

No new packages (stdlib `text/template`, `gopkg.in/yaml.v3` already present).

## PR Scope

One PR. Depends on Subplan 05 (LLM), Subplan 07 (search).
