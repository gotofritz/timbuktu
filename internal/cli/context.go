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
tbuk ask --session <name> ...    record the turn in a conversation thread (created if new)
tbuk ask --continue ...          the most recently used thread (-c); an error when there is none
tbuk search "<query>"            return matching chunks without calling the LLM
tbuk search --mode vector|keyword|hybrid ...   search mode (default: hybrid)

Query syntax (tbuk search only; tbuk ask reads a question, not an expression):
  main consumption               either form — any of the words
  main_consumption               that term only; _ stays inside a term
  main consumption -main_consumption   the words apart, not the identifier
  "main consumption"             that phrase, stop words kept
  long-term                      the words adjacent; - is a separator, so
                                 hyphenated prose answers to its words too
A leading - excludes; a query of only exclusions returns nothing.
A loose query reaches an identifier via the split form in search_text, so it
finds one in a fenced block or an inline code span, not one bare in prose.

## Conversation threads

tbuk ask --session go "how do slices grow?"   start (or continue) the thread "go"
tbuk ask --session go "and maps?"             the model sees the thread; retrieval does too
tbuk ask -c "and channels?"                   the thread used last

Without --session/-c nothing changes: ask is single-shot, same prompt as ever.
A thread lives in the knowledge base's own DB, so --root switches both together
and it survives reindex, a new terminal and a reboot. Names are lowercased and
trimmed, so --session Go and --session go are one thread; an unknown name
creates it. A turn stores question + planned query + answer + citation strings
(not chunk ids, which reindex renumbers), and is written only when the answer
completes — Ctrl-C, a provider error or an empty completion records nothing.

Inside a thread, retrieval runs on a planned query, not the question as typed:
the "window" planner prepends the last retrieval.window_turns questions
(manifest, default 2), so "and maps?" still reaches Go's maps. Setting
retrieval.rewrite: off in a template's manifest.yaml turns that off.

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

Paths below are written ~/.tbuk throughout. That is the default data root on
Linux and macOS; on Windows it is %USERPROFILE%\.tbuk (tbuk doctor's Platform
section prints the resolved directory). --root <dir> moves the whole root on
any platform.


database.path          SQLite file location
llm.provider           mlx | llama | ollama | claude | openai (default mlx)
llm.base_url           local server URL (default http://localhost:8080; ollama :11434)
llm.model              model name (required for claude/openai; HF repo id for mlx)
llm.max_tokens         output budget per answer (default 4096)
llm.context_tokens     model's whole window, prompt + reply (default 8192; 0 disables the guard)
embedding.provider     mlx | llama | ollama | openai  (claude has no embedding API)
embedding.base_url     embedding server URL
embedding.dimension    must match the loaded model (default 768)
chunking.size          target chunk size in tokens (default 400; keep ≤ server ubatch)
chunking.overlap       overlap between consecutive chunks (default 50)
                       Both are counted with a script-aware estimator: 4 ASCII
                       chars per token, 2 accented/Cyrillic/Greek chars per
                       token, 1 CJK rune per token, 2 tokens per emoji. A CJK
                       chunk therefore holds fewer bytes than an English one at
                       the same size.
ingest.embed_concurrency  parallel embed requests per file (default 4)
session.history_turns  prior Q/A pairs replayed into the prompt (default 6; 0 = replay none)
session.max_turns      turns kept per thread, oldest dropped first (default 0 = keep everything)

## Gotchas

- HTTP 500 "input too large": chunk exceeds server ubatch (default 512 tokens).
  Fix: restart embedding server with -b 1024 -ub 1024, or lower chunking.size.
  On a non-Latin corpus indexed before the estimator became script-aware, the
  stored chunks are oversized whatever the config says: tbuk doctor's
  "Chunking / stored" line reports it; tbuk reindex re-chunks and re-embeds.
- SHA256 dedup: unchanged files are skipped. Use --force to re-index.
- "query embedding has N dimensions but stored vectors have M": embedding.provider
  or embedding.model changed. Fix: tbuk reindex (re-embeds from raw/, no originals needed).
- tbuk ask answers from model general knowledge if retrieval finds nothing.
  Use --require-context to abort instead.
- tbuk ask fits the whole prompt (system + template + replayed turns + chunks +
  question) into llm.context_tokens minus the answer's max_tokens, before
  calling the model. Over budget it says "dropped the N oldest of M replayed
  turns" (threads only), then "compacted the retrieved text" (whitespace and
  filler words squeezed out), then "dropped N of M retrieved chunks", then
  drops the thread entirely, and if even a context-free, thread-free prompt
  overflows it fails locally instead of calling. History goes before evidence.
  Fix: raise llm.context_tokens to your model's real window, lower --top, or
  lower the template's max_tokens. A template may pin its own window with a
  top-level context_tokens in manifest.yaml.
- Claude provider: set ANTHROPIC_API_KEY; embedding must still be local (llama/ollama).
- "this knowledge base predates conversation threads": the DB was created before
  the sessions tables existed. Fix: go run ./scripts/add-sessions <db path>.
  tbuk doctor reports it on the Database / sessions line. No re-embedding.
- "--continue: this knowledge base has no conversation threads yet": nothing to
  continue. Start one with tbuk ask --session NAME. Never falls back silently.
- tbuk doctor "tokenizer: ✗ ...": the index was built under an earlier
  tokenizer. "default unicode61" splits every term at _; "tokenchars '_-'"
  keeps - inside a token, which locks hyphenated prose into one term.
  Fix, either way: go run ./scripts/retokenize-fts <db path>. No re-embedding —
  the index rebuilds from the stored column.
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
