#!/usr/bin/env bash
# Score the generation stage over docs/eval/timbuktu-docs.yaml: what the answers
# are worth, not just what retrieval found.
#
# This is the expensive sweep. Every case costs one model call to answer and,
# under --judge, a second to mark it — so a single run is roughly 48 calls and
# several minutes, and RUNS=3 is three times that. Budget accordingly.
#
# Needs a knowledge base holding this repository's documentation, and a model.
# See docs/eval/README.md.
set -euo pipefail

SET="${SET:-docs/eval/timbuktu-docs.yaml}"
OUT="${OUT:-docs/eval/results}"
TBUK="${TBUK:-tbuk}"
MODE="${MODE:-hybrid}"
# 1 writes a baseline. 2 or more reports the spread instead: a judged score is
# model-written, and one run of a model-written number is an anecdote (#173).
RUNS="${RUNS:-1}"
# Which artifact a multi-run sweep leaves behind. A spread is read rather than
# diffed, so text is the default; json is for a machine, and costs the same.
FORMAT="${FORMAT:-text}"
# The same default as scripts/eval-ingest.sh, and passed explicitly: a sweep
# reading another root measures another corpus and looks exactly like this one.
ROOT="${ROOT:-$HOME/.tbuk-eval}"

if [ ! -f "$ROOT/config.yaml" ]; then
  echo "no knowledge base at $ROOT — run 'make eval-ingest' first" >&2
  exit 1
fi

mkdir -p "$OUT"
echo "root  $ROOT"
echo "set   $SET"
echo "runs  $RUNS"
echo

# --stage both, not generation: it scores retrieval in the same pass, so the
# report records what the answers were built from rather than leaving that to
# be paired up with a separate run afterwards.
common=(--root "$ROOT" eval "$SET" --mode "$MODE" --stage both --judge)

if [ "$RUNS" -le 1 ]; then
  echo "==> generation baseline"
  # A run that cannot complete must not leave a stale file behind claiming it did.
  if ! "$TBUK" "${common[@]}" --format json > "$OUT/generation.json.tmp"; then
    rm -f "$OUT/generation.json.tmp"
    echo "    FAILED — see above" >&2
    exit 1
  fi
  mv "$OUT/generation.json.tmp" "$OUT/generation.json"
  echo "wrote $OUT/generation.json"
  echo "diff a later run against it with: --baseline $OUT/generation.json"
  exit 0
fi

# One invocation, one format. The cheap sweeps elsewhere write JSON and text by
# running twice; at a model call an answer and another a mark, this one cannot
# afford to. Text is the default because a spread is read, not diffed — a spread
# is not a report, so it is not what --baseline consumes either way.
echo "==> generation × $RUNS ($FORMAT)"
out="$OUT/generation-spread.$FORMAT"
if [ "$FORMAT" = "text" ]; then
  "$TBUK" "${common[@]}" --repeat "$RUNS" | tee "$out"
else
  if ! "$TBUK" "${common[@]}" --repeat "$RUNS" --format json > "$out.tmp"; then
    rm -f "$out.tmp"
    echo "    FAILED — see above" >&2
    exit 1
  fi
  mv "$out.tmp" "$out"
fi

echo
echo "wrote $out"
echo "read the judged rows first: a correctness that moves between runs is the judge, not the answers"
