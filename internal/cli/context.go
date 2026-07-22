package cli

import (
	"github.com/spf13/cobra"
)

const contextText = `# tbuk — local-first RAG knowledge base

tbuk indexes your documents and lets you query them with a local or hosted LLM.
Pipeline: preprocess → chunk → embed → store (SQLite). Query: embed question →
vector/keyword/hybrid search → retrieve chunks → LLM generates answer.

## Setup

tbuk init                        create ~/.tbuk/ with config.yaml and built-in templates
tbuk doctor                      check config, DB, LLM/embedding connectivity
tbuk version                     print version

## Ingesting documents

tbuk ingest <path>               index a file or directory (auto-preprocesses if needed)
tbuk ingest --force <path>       re-index even if file is unchanged
tbuk preprocess <path>           extract text only (inspect before indexing)
tbuk update <path>               re-index a file only if it changed
tbuk delete <path>               remove a document from the knowledge base

## Querying

tbuk ask "<question>"            RAG query: retrieve + LLM answer (streams by default)
tbuk ask --template <name> ...   use a named prompt template (default: qa)
tbuk ask --top <N> ...           retrieve N chunks (default: 5)
tbuk ask --no-stream ...         buffer output (useful for redirecting to a file)
tbuk search "<query>"            return matching chunks without calling the LLM
tbuk search --mode vector|keyword|hybrid ...   search mode (default: hybrid)

## Knowledge base

tbuk stats                       document and chunk counts, DB size
tbuk list                        list all indexed documents
tbuk find <key=value>...         find documents by metadata

## Templates

tbuk template list               list available prompt templates
tbuk template show <name>        print a template's files
Built-in templates: qa (default), brief (≤280 chars), anki (flashcards)

## Config: ~/.tbuk/config.yaml

database.path          SQLite file location
llm.provider           llama | ollama | claude | openai
llm.base_url           llama/ollama server URL (default http://localhost:8080)
llm.model              model name (required for claude/openai)
embedding.provider     llama | ollama | openai  (always local; claude has no embedding API)
embedding.base_url     embedding server URL
embedding.dimension    must match the loaded model (default 768)
chunking.size          target chunk size in tokens (default 400; keep ≤ server ubatch)
chunking.overlap       overlap between consecutive chunks (default 50)
ingest.embed_concurrency  parallel embed requests per file (default 4)

## Gotchas

- HTTP 500 "input too large": chunk exceeds server ubatch (default 512 tokens).
  Fix: restart embedding server with -b 1024 -ub 1024, or lower chunking.size.
- SHA256 dedup: unchanged files are skipped. Use --force to re-index.
- tbuk ask answers from model general knowledge if retrieval finds nothing.
  Use --require-context to abort instead.
- Claude provider: set ANTHROPIC_API_KEY; embedding must still be local (llama/ollama).
`

func newContextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "context",
		Short: "Print a compact reference for agents and LLMs",
		Long:  "Prints a cheatsheet covering commands, config, and common gotchas — useful for priming an AI coding agent.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := cmd.OutOrStdout().Write([]byte(contextText))
			return err
		},
	}
}
