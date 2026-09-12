#!/usr/bin/env bash
# Run the sweeps that decide the deferred defaults (#24 condense, #25 expand)
# over docs/eval/timbuktu-docs.yaml, writing one JSON report per run.
#
# Needs a knowledge base holding this repository's documentation, and — for the
# condense, expand and generation rows — a model. See docs/eval/README.md.
set -euo pipefail

SET="${SET:-docs/eval/timbuktu-docs.yaml}"
OUT="${OUT:-docs/eval/results}"
TBUK="${TBUK:-tbuk}"
MODE="${MODE:-hybrid}"

mkdir -p "$OUT"

run() {
  local name=$1; shift
  echo "==> $name"
  # A run that cannot complete must not leave a stale file behind claiming it did.
  if ! "$TBUK" eval "$SET" --mode "$MODE" --format json "$@" > "$OUT/$name.json.tmp"; then
    rm -f "$OUT/$name.json.tmp"
    echo "    FAILED — see above; later rows may need a model that is not running" >&2
    return 1
  fi
  mv "$OUT/$name.json.tmp" "$OUT/$name.json"
}

# Free: no model call at eval time. These are reproducible by anyone with the
# corpus ingested.
run off      --rewrite off
run window   --rewrite window
run gold     --gold

# Costs a model call per case. #24 and #25 are decided here.
run condense --rewrite condense
run expand3  --rewrite window --expand 3

echo
echo "wrote $OUT/*.json"
echo "diff any two with: $TBUK eval $SET --format json --baseline $OUT/window.json ..."
