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

# Ingest skips a document whose SHA256 is unchanged, which is right for a
# working knowledge base and wrong after a run that recorded the documents and
# then failed to embed them: the rows are there, the vectors are not, and every
# re-run skips them. FORCE=1 re-ingests regardless.
FORCE_FLAG=""
if [ -n "${FORCE:-}" ]; then
  FORCE_FLAG="--force"
fi

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
  # shellcheck disable=SC2086 # FORCE_FLAG is one optional flag, not a list
  "$TBUK" --root "$ROOT" ingest $FORCE_FLAG "$p"
done

echo
echo "==> doctor"
# The Eval section is the point: a label naming a document that was never
# ingested scores zero on every run and reads exactly like a retrieval failure.
"$TBUK" --root "$ROOT" doctor
