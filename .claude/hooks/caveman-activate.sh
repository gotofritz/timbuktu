#!/usr/bin/env bash
# caveman — Claude Code SessionStart hook (bash port).
# Writes flag file, then emits the active intensity level's ruleset to stdout
# so it's injected as hidden SessionStart context.

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./caveman-config.sh
. "$SCRIPT_DIR/caveman-config.sh"

claude_dir=$(caveman_dir)
flag_path=$(caveman_flag_path)
# Prefer repo-local config (where this hook is installed); fall back to global.
repo_settings="$SCRIPT_DIR/../settings.json"
if [ -f "$repo_settings" ]; then
    settings_path="$repo_settings"
else
    settings_path="$claude_dir/settings.json"
fi

# Rehydrate from persisted flag; fall back to default on fresh session.
# Independent modes are one-shot and must not survive across sessions.
mode=$(caveman_read_flag 2>/dev/null || true)
if [ -n "$mode" ] && caveman_is_independent_mode "$mode"; then
    caveman_clear_flag
    mode=""
fi
[ -z "$mode" ] && mode=$(caveman_default_mode)

if [ "$mode" = "off" ]; then
    caveman_clear_flag
    exit 0
fi

caveman_write_flag "$mode"

if caveman_is_independent_mode "$mode"; then
    printf 'CAVEMAN MODE ACTIVE — level: %s. Behavior defined by /caveman-%s skill.' "$mode" "$mode"
    exit 0
fi

# Canonical alias.
if [ "$mode" = "wenyan" ]; then
    label="wenyan-full"
else
    label="$mode"
fi

emit_fallback() {
    cat <<EOF
CAVEMAN MODE ACTIVE — level: $label

Respond terse like smart caveman. All technical substance stay. Only fluff die.

## Persistence

ACTIVE EVERY RESPONSE. No revert after many turns. No filler drift. Still active if unsure. Off only: "stop caveman" / "normal mode".

Current level: **$label**. Switch: \`/caveman lite|full|ultra\`.

## Rules

Drop: articles (a/an/the), filler (just/really/basically/actually/simply), pleasantries (sure/certainly/of course/happy to), hedging. Fragments OK. Short synonyms (big not extensive, fix not "implement a solution for"). Technical terms exact. Code blocks unchanged. Errors quoted exact.

Pattern: \`[thing] [action] [reason]. [next step].\`

Not: "Sure! I'd be happy to help you with that. The issue you're experiencing is likely caused by..."
Yes: "Bug in auth middleware. Token expiry check use \`<\` not \`<=\`. Fix:"

## Auto-Clarity

Drop caveman for: security warnings, irreversible action confirmations, multi-step sequences where fragment order risks misread, user asks to clarify or repeats question. Resume caveman after clear part done.

## Boundaries

Code/commits/PRs: write normal. "stop caveman" or "normal mode": revert. Level persist until changed or session end.
EOF
}

skill_path="$SCRIPT_DIR/../skills/caveman/SKILL.md"
if [ -f "$skill_path" ]; then
    # Strip YAML frontmatter, then keep only the active level's row in the
    # intensity table and the active level's example bullets.
    output=$(awk -v label="$label" '
        BEGIN { in_fm = 0; fm_done = 0 }
        NR == 1 && $0 == "---" { in_fm = 1; next }
        in_fm && $0 == "---" { in_fm = 0; fm_done = 1; next }
        in_fm { next }

        # Intensity table rows: | **<level>** | ... |
        /^\|[[:space:]]*\*\*[^*]+\*\*[[:space:]]*\|/ {
            if (index($0, "**" label "**") > 0) print
            next
        }

        # Example bullets: "- <level>: ..."
        {
            if (match($0, /^- [a-zA-Z0-9_-]+: /)) {
                # Strip "- " prefix and ": " suffix from the matched run to
                # extract the bullet label.
                bullet_label = substr($0, 3, RLENGTH - 4)
                if (bullet_label == label) print
                next
            }
            print
        }
    ' "$skill_path")
    printf 'CAVEMAN MODE ACTIVE — level: %s\n\n%s' "$label" "$output"
else
    emit_fallback
fi

# Statusline setup nudge — best-effort. Silent on parse failure.
if command -v jq >/dev/null 2>&1 && [ -f "$settings_path" ]; then
    has_statusline=$(jq -r 'if .statusLine then "yes" else "no" end' "$settings_path" 2>/dev/null || printf 'yes')
else
    has_statusline=$(grep -q '"statusLine"' "$settings_path" 2>/dev/null && printf 'yes' || printf 'no')
fi

if [ "$has_statusline" != "yes" ]; then
    statusline_cmd="bash \"$SCRIPT_DIR/caveman-statusline.sh\""
    printf '\n\nSTATUSLINE SETUP NEEDED: caveman ships a badge showing the active mode (e.g. [CAVEMAN], [CAVEMAN:ULTRA]). It is not configured yet. To enable, add this to %s: "statusLine": { "type": "command", "command": "%s" }. Proactively offer to set this up for the user on first interaction.' "$settings_path" "$statusline_cmd"
fi
