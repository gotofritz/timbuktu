#!/usr/bin/env bash
# Build the knowledge base that docs/eval/timbuktu-docs.yaml is labelled
# against: this repository's own documentation, in a root of its own so the
# measurement corpus stays out of a working knowledge base.
#
# Needs the embedding server named by the config — every chunk is embedded on
# the way in, whichever sweep you plan to run afterwards.
set -euo pipefail

ROOT="${ROOT:-$HOME/.tbuk-eval}"
TBUK="${TBUK:-tbuk}"

# One path per invocation: `tbuk ingest` takes exactly one argument.
PATHS=(
  README.md
  docs/user-guide.md
  docs/initial-context.md
  docs/plans
)

if [ ! -f "$ROOT/config.yaml" ]; then
  echo "==> init $ROOT"
  "$TBUK" --root "$ROOT" init
fi

for p in "${PATHS[@]}"; do
  echo "==> ingest $p"
  "$TBUK" --root "$ROOT" ingest "$p"
done

echo
echo "==> doctor"
# The Eval section is the point: a label naming a document that was never
# ingested scores zero on every run and reads exactly like a retrieval failure.
"$TBUK" --root "$ROOT" doctor
