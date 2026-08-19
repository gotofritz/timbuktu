# Subplan 21: Audio/Video Transcription + LLM Structuring

Generalises roadmap **#21** (YouTube-transcript ingestor) from
`docs/plans/next-steps.md` into the case that comes first: a local audio or
video file handed to `tbuk ingest`. A remote YouTube URL is the same pipeline
with a different fetch step, and rides the `Source` seam of subplan **#18**
once that lands; this subplan deliberately covers only files on disk, so it can
ship independently of #18.

## Goal

```bash
tbuk ingest ~/Movies/interview.mp4
```

should transcribe the audio, turn the transcript into structured Markdown, keep
that Markdown, and index it — with no extra flags and no other command. The
same holds for a bare audio file:

```bash
tbuk ingest ~/Recordings/standup.m4a
```

Audio is not a lesser case bolted on afterwards: the first thing the video path
does is throw the video away and keep the audio, so `.mp3`, `.m4a`, `.wav`,
`.flac`, `.ogg`, `.opus` and `.aac` enter the pipeline one step later than
`.mp4`, `.mov`, `.mkv`, `.webm` and `.m4v` and are otherwise identical. Both
lists go into `preprocess.DetectMIME` and `ingest.supportedExts`.

Transcription must be **provider-agnostic in the same shape as the rest of the
app**: a local engine and a hosted API behind one interface, selected by
`config.yaml`, exactly as `internal/llm` and `internal/embeddings` already do.

Success = an `.mp4` and an `.mp3` both ingest end-to-end offline with a local
whisper build, the identical pipeline reaches Groq/OpenAI by changing
`transcribe.provider`, and every existing extractor path is untouched.

## Why not cgo whisper

`whisper.cpp` ships official Go bindings, but they are cgo. This repo's release
promise is a statically linked, pure-Go binary (pure-Go SQLite, cross-compiled
to six OS/arch pairs in one GoReleaser run — see `README.md`). Linking cgo
whisper would end cross-compilation and turn every release into a per-platform
toolchain problem, for a feature most ingests never touch.

There is no mature pure-Go whisper inference (no usable ggml port), so local
inference has to live in another process. That is not a workaround: it is the
same relationship the app already has with `llama-server` and `ollama`. Two
process shapes cover it:

- **`whispercpp`** — exec a `whisper-cli` binary on `PATH` with a GGUF model.
  Fully offline, no server to keep running, no cgo.
- **`openai`** — POST to an OpenAI-shaped `/v1/audio/transcriptions`. One
  client covers *local* (`whisper-server` from whisper.cpp, Speaches,
  LocalAI → `base_url: http://localhost:8080`) and *hosted*
  (Groq `whisper-large-v3-turbo`, OpenAI `whisper-1`), because they share the
  wire format. This is the `base_url` pattern `LLMConfig` already uses.

Local-vs-remote is therefore a config line, not a build tag.

## ffmpeg is a hard dependency

Whisper consumes 16 kHz mono PCM. Video containers (MP4/MKV/WebM) and
compressed audio (AAC/Opus/MP3) have no usable pure-Go demux+decode path, and
`whisper-cli` itself only reads WAV. So the pipeline shells out:

```
ffmpeg -nostdin -i <src> -vn -ac 1 -ar 16000 -c:a pcm_s16le -f wav <tmp>.wav
```

`ffmpeg` must be on `PATH`; absence is a clear error at ingest time and a
checked line in `tbuk doctor`. It is a runtime dependency of the A/V path only
— text ingests never invoke it, and the binary stays pure Go.

## Deliverables

- New `internal/transcribe` package: `Transcriber` interface, `Transcript` /
  `Segment` types, `whispercpp` (exec) and `openai` (HTTP) providers, ffmpeg
  audio extraction, `NewTranscriber(cfg)` factory mirroring `llm.NewLLM`.
- `preprocess`: A/V MIME detection, and a path-based extractor seam
  (`PathExtractor`) so an extractor that cannot work from an `io.Reader` still
  dispatches through `preprocess.Extract`.
- LLM structuring pass: a `transcript` prompt template that turns raw segments
  into Markdown, reusing `internal/prompts` + `internal/llm`.
- Artefact split: verbatim transcript to `raw/<sha>.txt`, structured Markdown
  to `extracted/<sha>.md` (the indexed text), segment timings to
  `extracted/<sha>.segments.json`. The media file itself is never copied.
- `transcribe:` config block, validated in `config.Validate` beside the LLM and
  embedding provider checks; `tbuk doctor` lines for ffmpeg, the whisper binary
  and model, and the endpoint.
- `ingest.supportedExts` gains the video *and* audio extensions so `IngestDir`
  picks them up.
- Tests, TDD, table-driven: fake `ffmpeg`/`whisper-cli` scripts on a
  test-controlled `PATH`, `httptest` for the OpenAI-shaped provider, golden
  Markdown for the structuring pass, and a skip-if-unchanged regression.

## Package Layout

```
internal/transcribe/
  transcribe.go       ← Transcriber, Transcript, Segment, NewTranscriber
  audio.go            ← ffmpeg extraction to a temp 16k mono WAV
  whispercpp.go       ← exec whisper-cli, parse its JSON output
  openai.go           ← multipart POST /v1/audio/transcriptions
  markdown.go         ← Transcript → fallback Markdown (no LLM)
  transcribe_test.go  ← shared fakes: fake binaries, fixture transcripts
  audio_test.go  whispercpp_test.go  openai_test.go  markdown_test.go

internal/preprocess/
  av.go               ← avExtractor: ffmpeg → Transcriber → structuring → Markdown
  extractor.go        ← DetectMIME gains A/V; NewExtractor returns PathExtractor
```

## Interfaces

```go
package transcribe

// Segment is one timed span of speech. Start/End are seconds from the start of
// the media, kept so a citation can deep-link (…&t=<sec>) and so chunking can
// follow transcript cadence rather than sentence punctuation, which
// auto-captions rarely carry.
type Segment struct {
    Start float64
    End   float64
    Text  string
}

type Transcript struct {
    Language string
    Segments []Segment
    Duration float64
}

// Transcriber turns a media file into a timed transcript. Implementations take
// any path ffmpeg can decode; audio extraction happens inside.
type Transcriber interface {
    Name() string
    Transcribe(ctx context.Context, path string) (Transcript, error)
}

func NewTranscriber(cfg *config.TranscribeConfig) (Transcriber, error)
```

`NewTranscriber` is the same switch shape as `llm.NewLLM`:

```go
switch cfg.Provider {
case "whispercpp": return newWhisperCPP(cfg)
case "openai":     return newOpenAITranscriber(cfg)
default:           return nil, fmt.Errorf("transcribe: unknown provider %q", cfg.Provider)
}
```

**`whispercpp`** runs `whisper-cli -m <model> -f <wav> -oj -of <tmp>` and reads
the emitted JSON (`transcription[].offsets` → `Segment`). The binary name is
configurable (`whisper-cli`, `whisper-cpp` and `main` are all in the wild), and
`--language`, `--threads` come from config.

**`openai`** POSTs multipart `file` + `model` +
`response_format=verbose_json` and decodes `segments[]`. Key from
`OPENAI_API_KEY` (`llm/openai.go`'s rule), skipped when `base_url` points at
localhost so a local server needs no key. Transient 429/5xx go through the
shared retry helper (`embeddings/retry.go`); 401 fails fast with the provider's
message, bounded-body, like the other adapters.

## Pipeline

`preprocess.avExtractor.ExtractFile(ctx, path)`:

1. **Extract audio** — ffmpeg to a temp 16 kHz mono WAV, removed on return.
2. **Transcribe** — `Transcriber.Transcribe`, then write `raw/<sha>.txt` and
   `extracted/<sha>.segments.json`. Both are keyed on the media SHA256 and both
   are read back on a later run, so a re-ingest or a re-structuring never pays
   for transcription twice. This is the expensive step; nothing else is.
3. **Structure** — render the segments into the `transcript` prompt template and
   stream one LLM completion: a title, a short summary, and `##` sections whose
   headings carry a `[hh:mm:ss]` marker. `normalize` in the template's manifest
   repairs drift, as it does for the other templates.
4. **Persist the Markdown** — write it to `extracted/<sha>.md` (see below) and
   return it as the extracted text.
5. **Chunk → embed → store** — unchanged. The document's metadata gains
   `duration`, `language`, `transcribed_by` and `structured_by`.

If `transcribe.structure` is false, or no LLM is reachable, step 3 is skipped
and `markdown.go` renders the segments directly — timestamped paragraphs, no
model involved. The pipeline degrades to a plain transcript rather than
failing.

## What is written where — and the video is never copied

Today `WithRawDir` copies the ingested source byte-for-byte to
`raw/<sha><ext>`. For A/V that is wrong twice over: a 2 GB video doubled into
`~/.tbuk/raw` is not an archive anyone wants, and the artefacts worth keeping
are the *text* ones, which are otherwise unrecoverable without paying for
transcription again. The media file stays where the user put it — its path is
already the document's identity.

Three files, all keyed on `<sha>` = the SHA256 of the **media** file, so every
derived artefact stays addressable by the recording it came from:

| Path | What | Why |
| --- | --- | --- |
| `raw/<sha>.txt` | verbatim transcript, `[hh:mm:ss]` per line | the transcript of record: what the engine actually heard, before any model reworded it |
| `extracted/<sha>.md` | structured Markdown | the text that is chunked, embedded and cited |
| `extracted/<sha>.segments.json` | segments with float offsets | machine-readable timings for deep-linked citations (#23) and transcript-cadence chunking (#29) |

`raw/<sha>.txt` doubles as the transcription cache: present and not `--force`
means the engine is never invoked again, so re-running the structuring pass
with a different prompt or model is free. `--no-raw` suppresses only the `raw/`
copy — and with it that cache — exactly as it does for any other ingest.

This widens what `rawDir` means: "the source bytes as ingested" becomes "the
source *as text*, before interpretation". Note it in
`docs/initial-context.md` in the same PR.

One consequence for the ingester: it looks for extracted text at
`extractedDir/<sha>.txt` and auto-preprocesses when that file is missing. The
A/V path writes `.md` there instead — Markdown is what the structuring pass
produces and what the Markdown-aware chunking (#29) will want to see — so the
cache lookup takes the extension from the MIME rather than assuming `.txt`.

## Extractor seam

`preprocess.Extractor` is `Extract(ctx, io.Reader)`. ffmpeg cannot take a
non-seekable stream for MP4 (`moov` is often at the end of the file), so the
A/V extractor needs a path. Add, beside it:

```go
// PathExtractor extracts text from a file that cannot be processed as a plain
// byte stream — the A/V path shells out to ffmpeg, which needs a seekable file.
type PathExtractor interface {
    ExtractFile(ctx context.Context, path string) (string, error)
}
```

`NewExtractor` keeps returning `Extractor` for the four text MIMEs;
`Extract(ctx, path)` type-switches and calls `ExtractFile` when the extractor
implements `PathExtractor`. `ingest.DefaultFileExtractor` already has that
exact method, so the ingester is unchanged.

Two guards move with it:

- **`MaxFileSize` (100 MB) must not apply to A/V.** It exists to stop a stray
  multi-GB file being read into memory; ffmpeg streams and never does that. The
  limit becomes per-extractor — text extractors keep 100 MB, the A/V extractor
  gets its own (default 4 GB) ceiling from config.
- **`safeExtract`'s panic recovery** still wraps the call, unchanged.

## Config

```yaml
transcribe:
  provider: whispercpp        # whispercpp | openai
  model: ~/.tbuk/models/ggml-large-v3-turbo.bin   # path (whispercpp) or id (openai)
  base_url: ""                # empty → provider default; localhost:8080 for a local server
  binary: whisper-cli         # whispercpp only
  language: ""                # empty → auto-detect
  structure: true             # run the LLM structuring pass
  max_file_size: 4294967296   # A/V ceiling, bytes
```

`Config.Validate` gains `validTranscribeProviders = {"whispercpp", "openai"}`,
mirroring the factory switch, with the same "fail fast, before any work"
message style as the LLM and embedding checks. An absent `transcribe` block
means A/V files are *not* ingestable and `tbuk ingest video.mp4` says so in one
line naming the config key — never a partial ingest.

## CLI

```
tbuk ingest <file|dir>          # A/V handled automatically by extension
    [--no-structure]            # skip the LLM pass; timestamped transcript only
    [--force] [--verbose] [--no-raw]
```

No new command and no `--transcribe` flag: the file type decides, which is what
"ingest also transcribes video" means. `--no-structure` is the per-run override
of `transcribe.structure`.

`tbuk doctor` gains a **Transcription** section: ffmpeg found and its version,
provider, model file present (whispercpp) or endpoint probed (`openai` against
a localhost `base_url`; hosted endpoints reported as
`hosted API — not probed`, reusing `isHostedProvider`'s existing rule).

Progress: transcription of a long file is minutes of silence today's output has
no shape for, so the A/V path prints `<path> → transcribing (N min of audio)…`
before the call and the normal `→ k chunks embedded` after.

## Error Handling

- Missing `ffmpeg`/`whisper-cli` → one error naming the binary and the config
  key, not an `exec: "…": executable file not found in $PATH` leak.
- ffmpeg or whisper exiting non-zero → last ~1 KB of its stderr, bounded, in
  the wrapped error (same bounded-body discipline as the HTTP adapters).
- A file with no audio track → a clear "no audio stream" error, not an empty
  document.
- **Upload limits**: hosted `/v1/audio/transcriptions` caps request bodies
  (25 MB on OpenAI and Groq) — roughly 4 hours of 16 kHz mono WAV compressed,
  but minutes of raw WAV. First cut: the `openai` provider transcodes to Opus
  before upload and errors with a size-and-limit message above the cap.
  Silence-aware splitting and stitching is a follow-up, called out here so the
  segment offsets stay absolute from the start.
- `ctx` cancellation kills the child process (`exec.CommandContext`) and
  removes the temp WAV.

## Tests

TDD throughout; `_test.go` first in every package.

- `audio_test.go` — a fake `ffmpeg` script on a `t.TempDir()` `PATH` asserts the
  exact argv, the temp WAV's removal, non-zero exit surfacing, and ctx kill.
- `whispercpp_test.go` — a fake `whisper-cli` that writes a fixture JSON;
  asserts segment parsing (including offsets in ms → seconds), model/language
  flag plumbing, and the missing-binary message.
- `openai_test.go` — `httptest` server asserts multipart field names,
  `response_format=verbose_json`, auth header present for a remote `base_url`
  and absent for localhost, 429 retry via the shared helper, 401 fail-fast.
- `markdown_test.go` — golden file for the no-LLM rendering, including
  `[hh:mm:ss]` formatting at hour boundaries.
- `preprocess/av_test.go` — dispatch by every video and audio extension, the
  transcript cache hit (second call runs no transcriber), `MaxFileSize` not applied to A/V, panic
  recovery intact.
- `ingest` — `.mp4` and `.m4a` fixtures (seconds long, produced by the fake
  chain, no binaries committed) ingest to chunks; unchanged media is skipped on
  re-ingest; `raw/<sha>.txt` and `extracted/<sha>.md` each written once and only
  once and no copy of the media anywhere; `--no-raw` writes no `raw/` file and
  still indexes; a second run with `raw/<sha>.txt` present invokes no
  transcriber.
- `config_test.go` — provider validation, defaults round-trip through
  `DefaultYAMLForRoot`, absent block behaviour.
- Nothing in the suite invokes a real ffmpeg, whisper, or network endpoint.

## Out of Scope

- YouTube/remote URLs (roadmap #21's original framing) — needs the `Source`
  seam from #18 plus `yt-dlp`; same `Transcriber` underneath.
- Speaker diarization, translation, and word-level timestamps.
- Splitting long media for hosted upload limits (noted above).
- Transcript-cadence chunking (#29) — the segments are stored for it, but this
  subplan chunks the structured Markdown with the existing chunker.
