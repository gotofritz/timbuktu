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
tbuk reindex                     re-embed every document from raw/ after an embedding config change (--source-dir, --dry-run)

## Querying

tbuk ask "<question>"            RAG query: retrieve + LLM answer (streams by default)
tbuk ask --template <name> ...   use a named prompt template (default: qa)
tbuk ask --top <N> ...           retrieve N chunks (default: 5)
tbuk ask --no-stream ...         buffer output (useful for redirecting to a file)
tbuk search "<query>"            return matching chunks without calling the LLM
tbuk search --mode vector|keyword|hybrid ...   search mode (default: hybrid)

Query syntax (tbuk search only; tbuk ask reads a question, not an expression):
  main consumption               either form — any of the words
  main_consumption               that term only; _ and - stay inside a term
  main consumption -main_consumption   the words apart, not the identifier
  "main consumption"             that phrase, stop words kept
A leading - excludes; a query of only exclusions returns nothing.
A loose query reaches an identifier via the split form in search_text, so it
finds one in a fenced block or an inline code span, not one bare in prose.

## Knowledge base

tbuk stats                       document and chunk counts, DB size
tbuk list                        list all indexed documents
tbuk find <key=value>...         find documents by metadata
tbuk export <path>               tar snapshot of the KB (config + data folders) for backup/transfer
tbuk import <archive>            import a snapshot's documents + prompt templates, embedded locally (--on-conflict skip|overwrite|ask, --dry-run, --yes)
tbuk import data <archive>       documents only (same flags)
tbuk import templates <archive>  prompt templates only, no embedding provider needed (same flags)
                                 never reads the archive's config or embeddings
                                 rewind your own KB instead with: tar -xf kb.tar -C ~/.tbuk

## Templates

tbuk template list               list available prompt templates
tbuk template show <name>        print a template's files
Built-in templates: qa (default), brief (≤280 chars), anki (flashcards)

## Config: ~/.tbuk/config.yaml

database.path          SQLite file location
llm.provider           mlx | llama | ollama | claude | openai (default mlx)
llm.base_url           local server URL (default http://localhost:8080; ollama :11434)
llm.model              model name (required for claude/openai; HF repo id for mlx)
embedding.provider     mlx | llama | ollama | openai  (claude has no embedding API)
embedding.base_url     embedding server URL
embedding.dimension    must match the loaded model (default 768)
chunking.size          target chunk size in tokens (default 400; keep ≤ server ubatch)
chunking.overlap       overlap between consecutive chunks (default 50)
ingest.embed_concurrency  parallel embed requests per file (default 4)

## Gotchas

- HTTP 500 "input too large": chunk exceeds server ubatch (default 512 tokens).
  Fix: restart embedding server with -b 1024 -ub 1024, or lower chunking.size.
- SHA256 dedup: unchanged files are skipped. Use --force to re-index.
- "query embedding has N dimensions but stored vectors have M": embedding.provider
  or embedding.model changed. Fix: tbuk reindex (re-embeds from raw/, no originals needed).
- tbuk ask answers from model general knowledge if retrieval finds nothing.
  Use --require-context to abort instead.
- Claude provider: set ANTHROPIC_API_KEY; embedding must still be local (llama/ollama).
- tbuk doctor "tokenizer: ✗ default unicode61": index predates the query syntax
  above, so _ and - split terms. Fix: go run ./scripts/retokenize-fts <db path>.
  No re-embedding — the index rebuilds from the stored column.
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
