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
app**: engines behind one interface, selected by `config.yaml`, exactly as
`internal/llm` and `internal/embeddings` already do. Whisper is *an* engine
here, not the design.

Success = an `.mp4` and an `.m4a` both ingest end-to-end offline on macOS with
no model download at all, the same files ingest offline on Linux with a local
whisper build, and the identical pipeline reaches a hosted API by changing one
config line. Every existing extractor path is untouched.

## Engine survey

The engine is a swappable detail; this is the ground it was chosen from.

### Local

| Engine | Why | Cost |
| --- | --- | --- |
| **Apple `SpeechTranscriber`** (Speech framework, macOS 26+) | on-device, no model to download, no Python, decodes media itself via AVFoundation, per-segment timings | macOS-only, and unreachable from Go without a helper process |
| **whisper.cpp** (`whisper-cli`) | one self-contained binary + GGUF model, runs everywhere (CPU/Metal/CUDA), ~100 languages, segment timestamps | model download, quality below the newest ASR models |
| **NVIDIA Parakeet TDT** (v3 multilingual) | at/near the top of open ASR leaderboards, very high realtime factor | NeMo/Python runtime, or an ONNX port to maintain |
| **faster-whisper / Speaches** | several times faster than reference whisper, and speaks the OpenAI HTTP shape already | a Python service to keep running |
| **WhisperX** | forced alignment + pyannote diarization — "who said what" | heaviest of the lot, Python |
| **Vosk** | has Go bindings, tiny footprint | accuracy far below the rest; not worth a provider |

### Remote

| API | Why | Cost |
| --- | --- | --- |
| **Groq `whisper-large-v3-turbo`** | cheap, fast, OpenAI wire format → no new adapter | hosted |
| **OpenAI `whisper-1` / `gpt-4o-transcribe`** | reference; `gpt-4o-transcribe` has the better WER | `gpt-4o-transcribe`'s timestamp support is weaker than `verbose_json`, which this pipeline wants |
| **Deepgram Nova-3** | very fast, diarization, streaming | its own wire format = its own adapter |
| **AssemblyAI Universal** | diarization, chapters and summaries built in — could stand in for the LLM structuring pass | own format; overlaps work this repo already does with prompts |
| **ElevenLabs Scribe** | top-tier accuracy, diarization | own format |

### The third path: skip ASR

A multimodal LLM (Gemini, GPT-4o-audio) takes the audio directly and returns
transcript *and* structure in one call, collapsing steps 2 and 3 of the
pipeline below. Rejected as the default — timings come back vague, long or
noisy recordings invite hallucination, and hours-long media needs chunking
anyway — but it is one more `Transcriber` implementation whenever it is wanted,
and the pipeline below stays valid if it ever becomes the cheap option.

*(Model names, limits and pricing above move fast: re-check before betting a
default on them.)*

### Decision

Three providers ship:

| Provider | Role |
| --- | --- |
| `apple` | **default on macOS.** Nothing to install, nothing to download, no per-minute cost, and it decodes MP4/M4A itself |
| `whispercpp` | **default everywhere else**, and the escape hatch on macOS: portable, offline, same output shape |
| `openai` | remote *and* local-server: one OpenAI-shaped client covers Groq, OpenAI, and a local `whisper-server` / Speaches / LocalAI via `base_url` |

Deepgram, AssemblyAI, Parakeet and the multimodal one-shot path are each a new
file implementing `Transcriber` — no pipeline change, so none of them is in
this subplan.

## Why no engine is linked into the binary

`whisper.cpp` ships official Go bindings, but they are cgo, and Apple's Speech
framework is Swift-only. This repo's release promise is a statically linked,
pure-Go binary cross-compiled to six OS/arch pairs in one GoReleaser run
(`README.md`). Linking either engine ends cross-compilation and turns every
release into a per-platform toolchain problem, for a feature most ingests never
touch. There is no mature pure-Go whisper inference either (no usable ggml
port).

So **every engine runs out of process** — which is not a workaround but the
relationship this app already has with `llama-server` and `ollama`. `tbuk`
stays pure Go; engines are binaries or endpoints it talks to.

### The Apple helper

`apple` needs a small Swift CLI — `tbuk-speech` — that takes a media path,
runs `SpeechAnalyzer` + `SpeechTranscriber`, and writes the same JSON the Go
side already parses. It lives in `tools/tbuk-speech/` (a SwiftPM package),
is built by a **macOS-only** CI job, and is published as an extra darwin
release asset. It is not linked into `tbuk` and its absence degrades one
provider, never the build.

Version floor: `SpeechTranscriber` is macOS 26+. Below that the helper refuses
with a message naming `whispercpp` as the alternative — the older
`SFSpeechRecognizer` path is not worth carrying (short-audio limits, worse
output). The locale's on-device asset may need installing on first use
(`AssetInventory`); the helper triggers that and reports progress, and
`tbuk doctor` reports whether the asset is present.

## ffmpeg: required for two providers, not for Apple

Whisper consumes 16 kHz mono PCM, and video containers (MP4/MKV/WebM) plus
compressed audio (AAC/Opus/MP3) have no usable pure-Go demux+decode path, so
`whispercpp` and `openai` shell out:

```
ffmpeg -nostdin -i <src> -vn -ac 1 -ar 16000 -c:a pcm_s16le -f wav <tmp>.wav
```

`apple` skips this entirely — AVFoundation decodes the container itself — so a
default macOS install needs **no ffmpeg at all**. That is the point of making
it the default: on the machine most likely to be running this, the A/V path has
zero new dependencies. Where ffmpeg *is* needed, its absence is one clear error
at ingest time and a checked line in `tbuk doctor`.

## Deliverables

- New `internal/transcribe` package: `Transcriber` interface, `Transcript` /
  `Segment` types, `apple` / `whispercpp` / `openai` providers, ffmpeg audio
  extraction (used by the latter two), `NewTranscriber(cfg)` factory mirroring
  `llm.NewLLM`.
- `tools/tbuk-speech/`: the Swift helper, its own CI job, released as a darwin
  asset.
- `preprocess`: A/V MIME detection, and a path-based extractor seam
  (`PathExtractor`) so an extractor that cannot work from an `io.Reader` still
  dispatches through `preprocess.Extract`.
- LLM structuring pass: a `transcript` prompt template that turns raw segments
  into Markdown, reusing `internal/prompts` + `internal/llm`.
- Artefact split: verbatim transcript to `raw/<sha>.txt`, structured Markdown
  to `extracted/<sha>.md` (the indexed text), segment timings to
  `extracted/<sha>.segments.json`. The media file itself is never copied.
- `transcribe:` config block with a GOOS-dependent default provider, validated
  in `config.Validate` beside the LLM and embedding provider checks;
  `tbuk doctor` lines per provider.
- `ingest.supportedExts` gains the video *and* audio extensions so `IngestDir`
  picks them up.
- Tests, TDD, table-driven: fake `ffmpeg` / `whisper-cli` / `tbuk-speech`
  scripts on a test-controlled `PATH`, `httptest` for the OpenAI-shaped
  provider, golden Markdown for the structuring pass, and a skip-if-unchanged
  regression.

## Package Layout

```
internal/transcribe/
  transcribe.go       ← Transcriber, Transcript, Segment, NewTranscriber
  audio.go            ← ffmpeg extraction to a temp 16k mono WAV
  apple.go            ← exec tbuk-speech (darwin); build-tagged stub elsewhere
  whispercpp.go       ← exec whisper-cli, parse its JSON output
  openai.go           ← multipart POST /v1/audio/transcriptions
  markdown.go         ← Transcript → fallback Markdown (no LLM)
  transcribe_test.go  ← shared fakes: fake binaries, fixture transcripts
  audio_test.go  apple_test.go  whispercpp_test.go  openai_test.go
  markdown_test.go

internal/preprocess/
  av.go               ← avExtractor: (ffmpeg →) Transcriber → structuring → Markdown
  extractor.go        ← DetectMIME gains A/V; NewExtractor returns PathExtractor

tools/tbuk-speech/    ← SwiftPM package: SpeechAnalyzer → the same JSON
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
// a media path, not a stream: ffmpeg cannot seek a pipe (an MP4's moov atom is
// often at the end of the file) and AVFoundation wants a URL. Whether audio is
// extracted first is the implementation's business.
type Transcriber interface {
    Name() string
    Transcribe(ctx context.Context, path string) (Transcript, error)
}

func NewTranscriber(cfg *config.TranscribeConfig) (Transcriber, error)
```

`NewTranscriber` is the same switch shape as `llm.NewLLM`:

```go
switch cfg.Provider {
case "apple":      return newAppleTranscriber(cfg)   // darwin only
case "whispercpp": return newWhisperCPP(cfg)
case "openai":     return newOpenAITranscriber(cfg)
default:           return nil, fmt.Errorf("transcribe: unknown provider %q", cfg.Provider)
}
```

**`apple`** execs `tbuk-speech --json <path> [--locale …]` and decodes its
segments. `apple.go` carries `//go:build darwin`; the non-darwin stub returns
"provider `apple` is macOS-only — use `whispercpp`", so a Linux binary still
*parses* a config naming it and fails with a sentence instead of a panic.

**`whispercpp`** runs `whisper-cli -m <model> -f <wav> -oj -of <tmp>` and reads
the emitted JSON (`transcription[].offsets` → `Segment`). The binary name is
configurable (`whisper-cli`, `whisper-cpp` and `main` are all in the wild), and
`--language` / `--threads` come from config.

**`openai`** POSTs multipart `file` + `model` + `response_format=verbose_json`
and decodes `segments[]`. Key from `OPENAI_API_KEY` (`llm/openai.go`'s rule),
skipped when `base_url` points at localhost so a local server needs no key.
Transient 429/5xx go through the shared retry helper (`embeddings/retry.go`);
401 fails fast with the provider's message, bounded-body, like the other
adapters.

## Pipeline

`preprocess.avExtractor.ExtractFile(ctx, path)`:

1. **Prepare audio** — `whispercpp` / `openai`: ffmpeg to a temp 16 kHz mono
   WAV, removed on return. `apple`: nothing; the helper reads the media file.
2. **Transcribe** — `Transcriber.Transcribe`, then write `raw/<sha>.txt` and
   `extracted/<sha>.segments.json`. Both are keyed on the media SHA256 and both
   are read back on a later run, so a re-ingest or a re-structuring never pays
   for transcription twice. This is the expensive step; nothing else is.
3. **Structure** — render the segments into the `transcript` prompt template and
   stream one LLM completion: a title, a short summary, and `##` sections whose
   headings carry a `[hh:mm:ss]` marker. `normalize` in the template's manifest
   repairs drift, as it does for the other templates.
4. **Persist the Markdown** — write it to `extracted/<sha>.md` and return it as
   the extracted text.
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

`preprocess.Extractor` is `Extract(ctx, io.Reader)`, which no transcriber can
use (see the `Transcriber` comment above). Add, beside it:

```go
// PathExtractor extracts text from a file that cannot be processed as a plain
// byte stream — the A/V path hands the file to a media decoder, which needs a
// seekable file, not a pipe.
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
  multi-GB file being read into memory; the A/V path streams and never does
  that. The limit becomes per-extractor — text extractors keep 100 MB, the A/V
  extractor gets its own (default 4 GB) ceiling from config.
- **`safeExtract`'s panic recovery** still wraps the call, unchanged.

## Config

```yaml
transcribe:
  provider: apple             # apple (macOS) | whispercpp | openai
  model: ""                   # unused by apple; path (whispercpp) or id (openai)
  base_url: ""                # openai only; empty → provider default
  binary: ""                  # helper/engine binary override (tbuk-speech, whisper-cli)
  language: ""                # empty → auto-detect / system locale
  structure: true             # run the LLM structuring pass
  max_file_size: 4294967296   # A/V ceiling, bytes
```

The default is **GOOS-dependent**: `tbuk init` writes `apple` on darwin and
`whispercpp` elsewhere, and `Defaults()` does the same, so a fresh macOS
install transcribes with nothing else installed while a Linux install is told
plainly which binary it needs. `Config.Validate` gains
`validTranscribeProviders = {"apple", "whispercpp", "openai"}` mirroring the
factory switch — the name is valid on every platform, and only *constructing*
`apple` off darwin fails, so a config file stays portable between the user's
machines.

An absent `transcribe` block means A/V files are not ingestable, and
`tbuk ingest video.mp4` says so in one line naming the config key — never a
partial ingest.

## CLI

```
tbuk ingest <file|dir>          # A/V handled automatically by extension
    [--no-structure]            # skip the LLM pass; timestamped transcript only
    [--force] [--verbose] [--no-raw]
```

No new command and no `--transcribe` flag: the file type decides, which is what
"ingest also transcribes video" means. `--no-structure` is the per-run override
of `transcribe.structure`.

`tbuk doctor` gains a **Transcription** section, per provider: `apple` — helper
found, macOS version at or above the floor, locale asset installed;
`whispercpp` — ffmpeg and the engine binary found, model file present;
`openai` — ffmpeg found, endpoint probed for a localhost `base_url`, hosted
endpoints reported as `hosted API — not probed` via the existing
`isHostedProvider` rule.

Progress: transcription of a long file is minutes of silence today's output has
no shape for, so the A/V path prints `<path> → transcribing (N min of audio)…`
before the call and the normal `→ k chunks embedded` after.

## Error Handling

- Missing `tbuk-speech` / `whisper-cli` / `ffmpeg` → one error naming the
  binary, where to get it, and the config key — not a raw
  `exec: "…": executable file not found in $PATH`.
- `apple` on a non-darwin build, or on macOS below the floor → the message
  names `whispercpp` as the way forward.
- Any child process exiting non-zero → last ~1 KB of its stderr, bounded, in
  the wrapped error (same discipline as the HTTP adapters).
- A file with no audio track → a clear "no audio stream" error, not an empty
  document.
- **Upload limits**: hosted `/v1/audio/transcriptions` caps request bodies
  (25 MB on OpenAI and Groq) — minutes of raw WAV. First cut: the `openai`
  provider transcodes to Opus before upload and errors with a size-and-limit
  message above the cap. Silence-aware splitting and stitching is a follow-up,
  called out here so segment offsets stay absolute from the start.
- `ctx` cancellation kills the child process (`exec.CommandContext`) and
  removes any temp WAV.

## Tests

TDD throughout; `_test.go` first in every package.

- `audio_test.go` — a fake `ffmpeg` script on a `t.TempDir()` `PATH` asserts the
  exact argv, the temp WAV's removal, non-zero exit surfacing, and ctx kill.
- `apple_test.go` — a fake `tbuk-speech` writing fixture JSON; asserts segment
  parsing, locale plumbing, that **no ffmpeg is invoked** on this path, the
  missing-helper message, and (via the build-tagged stub) the non-darwin error.
  The real helper is exercised by its own SwiftPM tests in the macOS CI job.
- `whispercpp_test.go` — a fake `whisper-cli` writing fixture JSON; asserts
  parsing (offsets in ms → seconds), model/language flags, missing-binary
  message.
- `openai_test.go` — `httptest` asserts multipart field names,
  `response_format=verbose_json`, auth header present for a remote `base_url`
  and absent for localhost, 429 retry via the shared helper, 401 fail-fast.
- `markdown_test.go` — golden file for the no-LLM rendering, including
  `[hh:mm:ss]` formatting at hour boundaries.
- `preprocess/av_test.go` — dispatch by every video and audio extension, the
  transcript cache hit (second call runs no transcriber), `MaxFileSize` not
  applied to A/V, panic recovery intact.
- `ingest` — `.mp4` and `.m4a` fixtures (seconds long, produced by the fake
  chain, no binaries committed) ingest to chunks; unchanged media is skipped on
  re-ingest; `raw/<sha>.txt` and `extracted/<sha>.md` each written once and only
  once and no copy of the media anywhere; `--no-raw` writes no `raw/` file and
  still indexes; a second run with `raw/<sha>.txt` present invokes no
  transcriber.
- `config_test.go` — provider validation, the GOOS-dependent default, defaults
  round-trip through `DefaultYAMLForRoot`, absent block behaviour.
- Nothing in the Go suite invokes a real engine, ffmpeg, or network endpoint,
  and the Go suite stays green on Linux CI with no macOS involved.

## Out of Scope

- YouTube/remote URLs (roadmap #21's original framing) — needs the `Source`
  seam from #18 plus `yt-dlp`; same `Transcriber` underneath.
- Further providers — Deepgram, AssemblyAI, Parakeet, multimodal one-shot. Each
  is one file behind the interface; none changes the pipeline.
- Speaker diarization, translation, and word-level timestamps.
- Splitting long media for hosted upload limits (noted above).
- Transcript-cadence chunking (#29) — the segments are stored for it, but this
  subplan chunks the structured Markdown with the existing chunker.
