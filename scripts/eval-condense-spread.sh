#!/usr/bin/env bash
# Sample what condense varies by between runs (#173), and prove the window
# does not.
#
# condense is the one knob in this harness whose output comes from a model, so
# it is the one whose numbers are a sample rather than a measurement. This runs
# it several times over one corpus, with no ingest in between, and reports the
# spread. The window run is the control: it is deterministic, so a spread of
# anything but zero there means the corpus or the index moved under the runs
# and neither column can be read.
#
# Needs a knowledge base holding this repository's documentation, and a model.
# See docs/eval/README.md.
set -euo pipefail

SET="${SET:-docs/eval/timbuktu-docs.yaml}"
OUT="${OUT:-docs/eval/results}"
TBUK="${TBUK:-tbuk}"
MODE="${MODE:-hybrid}"
RUNS="${RUNS:-3}"
# The same default as scripts/eval-ingest.sh, and passed explicitly on every
# run — a sweep reading another root measures another corpus, and looks exactly
# like a measurement of this one.
ROOT="${ROOT:-$HOME/.tbuk-eval}"

if [ ! -f "$ROOT/config.yaml" ]; then
  echo "no knowledge base at $ROOT — run 'make eval-ingest' first" >&2
  exit 1
fi
if [ "$RUNS" -lt 2 ]; then
  echo "RUNS=$RUNS: one run is not a spread" >&2
  exit 1
fi

mkdir -p "$OUT"
echo "root  $ROOT"
echo "set   $SET"
echo "runs  $RUNS"
echo

spread() {
  local name=$1; shift
  echo "==> $name × $RUNS"
  # A run that cannot complete must not leave a stale file behind claiming it did.
  if ! "$TBUK" --root "$ROOT" eval "$SET" --mode "$MODE" --repeat "$RUNS" \
      --format json "$@" > "$OUT/$name.json.tmp"; then
    rm -f "$OUT/$name.json.tmp"
    echo "    FAILED — see above" >&2
    return 1
  fi
  mv "$OUT/$name.json.tmp" "$OUT/$name.json"
  # The text spread names the cases that moved and the queries they moved on,
  # which is what explains a gain rather than restating it.
  "$TBUK" --root "$ROOT" eval "$SET" --mode "$MODE" --repeat "$RUNS" "$@" | tee "$OUT/$name.txt"
  echo
}

# The control. Deterministic, so its spread should be exactly zero; anything
# else is the instrument moving, not the planner.
spread window-spread   --rewrite window
# The measurement. One model call per case, every run.
spread condense-spread --rewrite condense

echo "wrote $OUT/{window,condense}-spread.{json,txt}"
echo "record the numbers in docs/eval/README.md"
