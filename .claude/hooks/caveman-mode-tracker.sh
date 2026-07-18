#!/usr/bin/env bash
# caveman — Claude Code UserPromptSubmit hook (bash port).
# Reads {prompt, ...} JSON from stdin, updates the .caveman-active flag based
# on /caveman commands or natural language activation, and emits a per-turn
# reminder so the active mode stays anchored across long sessions.

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./caveman-config.sh
. "$SCRIPT_DIR/caveman-config.sh"

if ! command -v jq >/dev/null 2>&1; then
    printf 'caveman-mode-tracker: jq is required but not installed — prompt parsing disabled\n' >&2
    exit 0
fi
input=$(cat)
prompt=$(printf '%s' "$input" | jq -r '.prompt // ""' 2>/dev/null || printf '')
prompt=$(printf '%s' "$prompt" | tr '[:upper:]' '[:lower:]' | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')

# Natural-language activation: explicit phrases only ("activate caveman",
# "talk like caveman", "enable caveman mode"). Generic mentions like
# "how does caveman mode work?" must not trigger activation.
# Patterns use POSIX character classes — no GNU \b word boundaries.
if printf '%s' "$prompt" | grep -qE '(^|[[:space:]])(activate|enable|turn on|start|talk like)[[:space:]].*caveman'; then
    if ! printf '%s' "$prompt" | grep -qE '(^|[[:space:]])(stop|disable|turn off|deactivate)([[:space:]]|$)'; then
        m=$(caveman_default_mode)
        if [ "$m" != "off" ]; then
            caveman_write_flag "$m"
        fi
    fi
fi

# Slash-command parsing.
# Independent modes (/caveman-commit etc) are one-shot — emit context for this
# turn only, never persist to flag.
emit_independent=""
case "$prompt" in
    /caveman*)
        cmd=$(printf '%s' "$prompt" | awk '{print $1}')
        arg=$(printf '%s' "$prompt" | awk '{print $2}')
        new_mode=""
        case "$cmd" in
            /caveman-commit) emit_independent=commit ;;
            /caveman-review) emit_independent=review ;;
            /caveman-compress|/caveman:caveman-compress|/caveman:compress) emit_independent=compress ;;
            /caveman|/caveman:caveman)
                if [ -z "$arg" ]; then
                    new_mode=$(caveman_default_mode)
                else
                    case "$arg" in
                        off|stop|disable) new_mode=off ;;
                        wenyan-full) new_mode=wenyan ;;
                        *)
                            if caveman_is_valid_mode "$arg" && ! caveman_is_independent_mode "$arg"; then
                                new_mode=$arg
                            fi ;;
                    esac
                fi ;;
        esac
        if [ "$new_mode" = "off" ]; then
            caveman_clear_flag
        elif [ -n "$new_mode" ]; then
            caveman_write_flag "$new_mode"
        fi
        ;;
esac

# Deactivation triggers (slash + natural language). POSIX-portable patterns.
if printf '%s' "$prompt" | grep -qE '(^|[[:space:]])(stop|disable|deactivate|turn off)[[:space:]].*caveman|caveman[[:space:]].*(stop|disable|deactivate|turn off)|(^|[[:space:]])normal mode([[:space:]]|$)'; then
    caveman_clear_flag
fi

# Per-turn reinforcement.
# Independent modes: one-shot — load SKILL.md for this turn only (not from flag).
# Base modes: persistent reminder from flag.
ctx=""
if [ -n "$emit_independent" ]; then
    skill_path="$SCRIPT_DIR/../skills/caveman-$emit_independent/SKILL.md"
    if [ -f "$skill_path" ]; then
        # Strip YAML frontmatter; emit remaining content as context.
        ctx=$(awk 'BEGIN{fm=0} NR==1&&/^---$/{fm=1;next} fm&&/^---$/{fm=0;next} fm{next} {print}' "$skill_path")
    else
        ctx="Apply /caveman-$emit_independent skill behavior this turn."
    fi
else
    active=$(caveman_read_flag) || active=""
    if [ -n "$active" ]; then
        # wenyan is a canonical alias for wenyan-full.
        if [ "$active" = "wenyan" ]; then label="wenyan-full"; else label="$active"; fi
        skill_path="$SCRIPT_DIR/../skills/caveman/SKILL.md"
        if [ -f "$skill_path" ]; then
            # Mirror caveman-activate.sh: strip frontmatter, keep only the
            # active label's intensity table row and example bullets.
            skill_body=$(awk -v label="$label" '
                BEGIN { in_fm = 0 }
                NR == 1 && $0 == "---" { in_fm = 1; next }
                in_fm && $0 == "---"   { in_fm = 0; next }
                in_fm { next }
                /^\|[[:space:]]*\*\*[^*]+\*\*[[:space:]]*\|/ {
                    if (index($0, "**" label "**") > 0) print
                    next
                }
                {
                    if (match($0, /^- [a-zA-Z0-9_-]+: /)) {
                        if (substr($0, 3, RLENGTH - 4) == label) print
                        next
                    }
                    print
                }
            ' "$skill_path")
            ctx="CAVEMAN MODE ACTIVE — level: $label

$skill_body"
        else
            ctx="CAVEMAN MODE ACTIVE ($active). Drop articles/filler/pleasantries/hedging. Fragments OK. Code/commits/security: write normal."
        fi
    fi
fi
if [ -n "$ctx" ]; then
    jq -nc --arg ctx "$ctx" \
        '{hookSpecificOutput: {hookEventName: "UserPromptSubmit", additionalContext: $ctx}}'
fi

exit 0
