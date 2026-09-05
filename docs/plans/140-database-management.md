# Plan 140: database management — `--db`, `TBUK_DB`, named collections

Lands roadmap **#140** (database management / seamless switching) from
`docs/plans/next-steps.md`. Adds a way to point a single invocation at a
different SQLite database *without* moving the rest of the data root, plus
optional names for the databases you switch between most.

`--root DIR` (#95) already switches the whole data root — db, extracted cache,
raw archive, prompt templates and config, all at once. That is the right answer
for a genuinely separate knowledge base. It is the wrong answer when the point
is only that the *documents* differ: cloning the root duplicates the prompt
templates you maintain, and splits the content-addressed extracted/raw caches
so the same PDF is extracted twice. This plan covers that second case.

## Goal

```bash
# a second database beside the default one, sharing prompts and caches
tbuk --db ~/kb/work.sqlite ingest ./work-notes
tbuk --db ~/kb/work.sqlite ask "what did we decide about the migration?"

# same thing, named once in config.yaml
tbuk --db work ask "what did we decide about the migration?"

# per-shell, no flag on every command
export TBUK_DB=work
tbuk ask "..."
tbuk stats                     # reports which database it read

# still one root: one prompts/, one extracted/, one raw/
tbuk template list             # same templates whichever --db is in play
```

Success = `--db` changes exactly one thing (`database.path`); everything else
keeps resolving under the root as it does today; a typo'd `--db` fails loudly
instead of silently creating an empty knowledge base; and no schema change,
because each database is already self-contained.

## Why now

The pieces are all in place, so this is small:

- Every command reads the database path from one place —
  `configFrom(cmd).Database.Path`, resolved once in the root
  `PersistentPreRunE` (`internal/cli/root.go`). Overriding it there reaches all
  20 commands with no per-command edits.
- `Config.ResolvePaths(root)` already rebases relative data paths and leaves
  absolute ones alone, so a collection path in the config gets root-relative
  resolution for free.
- The shared-root parts are already content-addressed —
  `extracted/<sha256>.v<N>.txt` (`preprocess.CacheName`) and
  `raw/<sha256><ext>` (`ingest.copySourceToRaw`) — so two databases can share
  them without collision. Sharing is in fact the win: ingest the same PDF into
  two collections and it is extracted once.
- `reindex` walks the documents *in the open database* and resolves each by its
  own stored `raw_path`, so a shared `raw/` never leaks another collection's
  documents into this one.

## Design decisions (and alternatives rejected)

### 1. Resolve in the root `PersistentPreRunE`, override `cfg.Database.Path`

The flag/env/collection resolution happens in one place, after
`config.LoadForRoot` and before `cfg.Validate()`. Downstream code is untouched:
`openApp(configFrom(cmd))` opens whatever path the config now holds.

*Rejected:* threading a separate "db path" through the command context beside
the config. It would leave two sources of truth for the same value, and every
command that opens the database would have to remember to prefer one.

### 2. Precedence: `--db` > `TBUK_DB` > `database.path`

Flag beats environment beats config — the usual order, most specific wins. The
config's `database.path` keeps its current meaning and default
(`./tbuk.sqlite` under the root), so an existing setup with neither flag nor
env behaves exactly as today.

`--root` and `--db` are independent and compose: `--root` picks the root (and
therefore prompts, extracted, raw, config), `--db` overrides only the database.
Given both, the database is *not* dragged under the root.

### 3. Flag and env paths resolve against the cwd, config paths against the root

This is the one subtle rule, and it follows what each surface already does:

| value | resolved against | precedent |
|---|---|---|
| `--db ./scratch.sqlite` | cwd (`filepath.Abs`) | `--root`, and every path argument (`tbuk ingest ./docs`) |
| `TBUK_DB=./scratch.sqlite` | cwd | same — it stands in for the flag |
| `collections: {work: ./work.sqlite}` | root (`resolveUnderRoot`) | `database.path`, `ingest.raw_dir`, every other config path |

So a relative path typed at a prompt means what it means in the shell, and a
relative path written in the config keeps the config portable — move the root
and the collections move with it. An absolute path is used as-is either way.

### 4. A bare name is a collection name, never a path

`--db work` must not turn into `./work` in whatever directory the user
happened to be in. The rule:

- A value matching `^[A-Za-z0-9][A-Za-z0-9_-]*$` — no `/`, no `.`, no `~`, no
  `:` — is a **collection name**, looked up in `collections:`.
- Anything else is a **path**.
- A name-shaped value that is not in `collections:` is an error naming the
  known collections. It never falls back to treating the name as a path.

That keeps `--db work` and `--db ./work.sqlite` unambiguous by shape alone,
with no new syntax to learn, and `--db :memory:` still reaches
`storage.Open`'s in-memory case (it contains `:`, so it is a path).

*Rejected:* a sigil (`--db @work`) — the issue's own example is
`tbuk --db work ask ...`, and a prefix is one more thing to remember.
*Rejected:* try the path first, fall back to the name — a stray file called
`work` in the cwd would silently shadow the configured collection.

### 5. A missing database file is an error when the path came from a flag, env, or collection

`storage.Open` creates and migrates any path it is given. That is right for
`database.path`, where the file is created once and used forever. It is wrong
for a flag: `--db ~/kb/wrok.sqlite` would create an empty database, and
`ask` would answer from the model's general knowledge (a documented gotcha)
against a knowledge base that does not exist.

So: when the resolved path came from the flag, the env var, or a collection,
and the file does not exist, fail before opening —

```
database not found: /Users/x/kb/wrok.sqlite (from --db)
create it with: tbuk init --db /Users/x/kb/wrok.sqlite
```

`tbuk init` is exempt: it is the command whose job is to create. Config-derived
`database.path` keeps today's create-on-open, so nothing existing changes.

*Rejected:* always create. Cheaper to implement, and it turns every typo into a
silently wrong answer.

### 6. `collections:` maps a name to a database path, and nothing else

```yaml
collections:
  work: ./work.sqlite
  personal: /Volumes/big/personal.sqlite
```

A collection is a database, not a second profile: no per-collection providers,
chunk sizes, prompts or caches. Those all stay root-level, which is precisely
what makes this different from `--root`. If a collection ever needs its own
providers, that is `--root`, and the answer is to use it.

Adding the field also means `collections:` stops being rejected by the config
loader — `LoadForRoot` decodes with `KnownFields(true)`, so an unknown key is a
hard error today.

### 7. Report which database was used, and where that came from

Switching databases invisibly is how you ingest into the wrong one. `stats`
already prints `db_path`. Add the resolution source (`--db`, `TBUK_DB`,
collection name, or config) to `doctor`'s report, and a
`tbuk collection list` that shows each configured name, its resolved path, and
whether that file exists.

## Package changes (sketch — confirm exact signatures when implementing)

### `internal/config`

```go
// config.go
type Config struct {
    // ...
    Collections map[string]string `yaml:"collections"` // name → database path
}
```

- `relativeDefaults()` — add an empty `Collections` map; `defaultConfigNode()`
  gets its head comment ("name a database here to switch to it with
  `--db <name>`; relative paths resolve under the root").
- `ResolvePaths(root)` — rebase each collection path via the existing
  `resolveUnderRoot`, so relative collection paths follow the root and absolute
  ones are pinned.
- `Validate()` — each collection name matches the name shape (decision 4) and
  each path is non-empty. Duplicate paths under two names are allowed: that is
  an alias, not a mistake.

```go
// collections.go (new)

// DBSource records where the resolved database path came from, for reporting
// and for the missing-file guard.
type DBSource int
const (
    DBFromConfig DBSource = iota
    DBFromFlag
    DBFromEnv
    DBFromCollection
)

// IsCollectionName reports whether s is shaped like a collection name rather
// than a filesystem path.
func IsCollectionName(s string) bool

// ResolveDB picks the database path for this invocation from the flag value,
// the environment value and cfg, applying the precedence and the
// name-vs-path rule. Relative flag/env paths are made absolute against cwd;
// collection paths are taken from cfg, already root-resolved by ResolvePaths.
// It touches no files — existence is the caller's check.
func ResolveDB(cfg Config, flag, env string) (path string, src DBSource, name string, err error)
```

Pure and table-driven-testable; the filesystem check stays in the CLI where the
"is this `init`?" exemption lives.

### `internal/cli`

- `root.go` — add the `--db` persistent flag; read `TBUK_DB`; call
  `config.ResolveDB` after `LoadForRoot`; assign `cfg.Database.Path`; apply the
  missing-file guard unless the command is `init`; store the source (and
  collection name) in the command context beside `cfgKey`/`rootKey`, with a
  `dbSourceFrom(cmd)` accessor next to `rootFrom`.
- `doctor.go` — report the database path with its source.
- `collection.go` (new) — `tbuk collection list`.
- `init.go` — with a `--db` in play, create and migrate that database
  (open/close through `storage.Open`) as part of scaffolding; when `--db NAME`
  names a collection the config does not have, add it (reusing the YAML-node
  merge approach that `FillMissingDefaults` already uses, so comments survive).
- `context.go` — the cheatsheet gains `--db` and `TBUK_DB` under Setup, and a
  line in Config for `collections`.

### Docs

- `docs/initial-context.md` — the **Config** section currently ends by calling
  `--root` "the concrete form of the 'DB switching' roadmap item — a whole-
  collection switch rather than a single `--db` path". That sentence is what
  this plan replaces; rewrite the flag-resolution paragraph to cover both flags
  and the precedence table. **Required before merge** (AGENTS.md: architecture
  and core patterns).
- `docs/user-guide.md` — a subsection after "Using a different data directory"
  (§5) contrasting the two: `--root` for a wholly separate knowledge base,
  `--db` for the same setup over different documents, including the shared-root
  consequences below.
- `docs/plans/next-steps.md` — mark quick win 1 delivered.

## CLI surface

```
tbuk --db <path|name> <command>     override the database for one invocation
TBUK_DB=<path|name>                 same, for the shell
tbuk init --db <path|name>          create (and register) a database
tbuk collection list                configured collections, resolved paths, exists?
```

`--db` is a persistent flag on the root command, so it works before or after
the subcommand (`tbuk --db work ask ...` and `tbuk ask --db work ...` both
resolve identically).

## Sharing one root between databases — what actually happens

Worth stating explicitly, because it is the whole point of `--db` and two of
these are sharp:

- **`extracted/` is shared and that is a feature.** Content-addressed by
  source SHA and extractor version, so the same document ingested into two
  collections is extracted once.
- **`raw/` is shared and safe.** Also content-addressed. `reindex` iterates the
  open database's own documents and resolves each by its stored `raw_path`, so
  it never re-embeds another collection's documents.
- **`tbuk delete` removes the extracted cache entry** (`internal/cli/delete.go`,
  the best-effort cleanup after `docs.Delete`) for that document's SHA. With a
  shared root, that discards a cache entry another collection may still
  reference. Consequence is a cache miss and a re-extract, not data loss —
  `raw/` still holds the copy. **Documented, not fixed:** making the cleanup
  survey every configured collection is more machinery than a proof of concept
  needs.
- **`tbuk export` archives the whole `raw/` and `extracted/` directories**
  (`internal/export`: `addDir` walks each configured folder wholesale), so
  exporting one collection from a shared root produces an archive fat with
  every collection's raw copies. The archive is valid and import re-embeds only
  what the exported database references; it is merely larger than it needs to
  be. Slimming it to the referenced files is listed as out of scope below.

## Testing (TDD, table-driven, ≥85% per package)

Write the failing test first in each case; `make check-ci` before the PR.

**`internal/config` (`collections_test.go`, `config_test.go`)**

- `ResolveDB` table: flag path; flag name that resolves; flag name that does
  not (error names the known collections); env path; env name; flag beats env;
  env beats config; neither → config's `database.path`; relative flag → cwd
  absolute; relative collection → root absolute; absolute left alone;
  `:memory:` treated as a path; empty `collections` with a name-shaped flag.
- `IsCollectionName` table over `work`, `work-kb`, `w2`, `./work`, `work.sqlite`,
  `~/work`, `/abs/work`, `:memory:`, `` (empty), `-lead`.
- `LoadForRoot` round-trip: a config carrying `collections:` loads (no
  `KnownFields` rejection) and its relative paths land under the root.
- `FillMissingDefaults` adds `collections` to a config that predates it, and
  reports it in `added`.
- `Validate` rejects a bad collection name and an empty collection path.

**`internal/cli` (`root_test.go`, `collection_test.go`, `init_test.go`,
`integration_test.go`)**

- `--db PATH` on an existing file makes a downstream command read that path
  (assert via `stats --format json`'s `db_path`).
- `TBUK_DB` does the same; `--db` wins when both are set.
- Missing file from `--db` errors with the path and the `tbuk init --db` hint;
  the same path via `database.path` still creates (no regression).
- `init` is exempt from the guard and creates the file, migrated (a table from
  `RunMigrations` is queryable afterwards).
- `--root R --db D` keeps prompts under `R` (`template list` unaffected) while
  the database is `D`.
- `collection list` prints names, resolved paths and existence; prints a usable
  message with no collections configured.
- Integration: ingest a file into the default database, ingest a different file
  with `--db`, then `stats` on each shows only its own documents while the
  second ingest's extracted cache hits the first's entry for a shared source.

## Rollout

1. **`--db` and `TBUK_DB` as paths.** Flag, env, precedence, cwd resolution,
   missing-file guard, source reporting in `doctor`/`stats`. Delivers most of
   the issue on its own; nothing in it depends on the config schema.
2. **Named collections.** `Collections` on `Config`, resolution by name,
   validation, `tbuk collection list`.
3. **`init --db` and docs.** Creating and registering a collection, then the
   `initial-context.md` / `user-guide.md` / `next-steps.md` pass and archiving
   this plan.

Each milestone is independently shippable and independently useful; 1 alone
closes the "switch only the DB" half of the issue.

## Out of scope (deliberate)

- **`TBUK_ROOT`.** Symmetric and cheap, but not asked for; `--root` already
  has no env var and adding one is a separate one-liner if it is ever wanted.
- **Per-collection anything else** — providers, models, chunking, prompts,
  caches. That is what `--root` is for.
- **Collection CRUD beyond `list`** (`add`/`remove`/`rename`). `init --db`
  registers; removing a name is one line in `config.yaml`.
- **Slimming `tbuk export` to the raw/extracted files the exported database
  actually references.** A real improvement, independent of this issue, and it
  touches archive layout — its own change.
- **Any schema change or migration.** Each database is already self-contained;
  nothing here reads or writes across two of them.

## Open questions

1. **A `default:` key naming the collection used with no flag?**
   (`collections: {default: work, work: ./work.sqlite}`.) It would let a user
   switch their day-to-day database without touching `database.path`. Proposed
   answer: **no** — it creates a second way to say the same thing, and the
   precedence table grows a row for no new capability. Settle before milestone 2.
2. **Should `--db` accept a *directory*, meaning `<dir>/tbuk.sqlite`?** Would
   read nicely (`--db ~/kb/work`) but blurs decision 4's shape rule and
   overlaps `--root`. Proposed answer: **no**.
3. **Should `delete`'s extracted-cache cleanup become collection-aware?**
   Proposed answer: **no** for the proof of concept (see the sharing section);
   revisit if it bites in practice.
