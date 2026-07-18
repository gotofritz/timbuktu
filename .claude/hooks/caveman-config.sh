# caveman — shared config + flag-file helpers (bash port).
# Sourced by caveman-activate.sh and caveman-mode-tracker.sh. Not executable.

CAVEMAN_VALID_MODES="off lite full ultra wenyan-lite wenyan wenyan-full wenyan-ultra commit review compress"
CAVEMAN_INDEPENDENT_MODES="commit review compress"
CAVEMAN_MAX_FLAG_BYTES=64

caveman_dir() {
    printf '%s' "${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
}

caveman_flag_path() {
    printf '%s/.caveman-active' "$(caveman_dir)"
}

caveman_is_valid_mode() {
    case " $CAVEMAN_VALID_MODES " in
        *" $1 "*) return 0 ;;
    esac
    return 1
}

caveman_is_independent_mode() {
    case " $CAVEMAN_INDEPENDENT_MODES " in
        *" $1 "*) return 0 ;;
    esac
    return 1
}

caveman_default_mode() {
    if [ -n "${CAVEMAN_DEFAULT_MODE:-}" ]; then
        local mode
        mode=$(printf '%s' "$CAVEMAN_DEFAULT_MODE" | tr '[:upper:]' '[:lower:]')
        if caveman_is_valid_mode "$mode" && ! caveman_is_independent_mode "$mode"; then
            printf '%s' "$mode"
            return 0
        fi
    fi

    local config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/caveman"
    local config_path="$config_dir/config.json"
    if [ -f "$config_path" ]; then
        if ! command -v jq >/dev/null 2>&1; then
            printf 'caveman: jq required to read %s — using default mode\n' "$config_path" >&2
        else
            local mode
            mode=$(jq -r '.defaultMode // ""' "$config_path" 2>/dev/null | tr '[:upper:]' '[:lower:]')
            if [ -n "$mode" ] && caveman_is_valid_mode "$mode" && ! caveman_is_independent_mode "$mode"; then
                printf '%s' "$mode"
                return 0
            fi
        fi
    fi

    printf 'full'
}

# Best-effort symlink-safe write: refuses if flag is a symlink, writes via
# temp + rename with 0600 perms. Silent on any filesystem error.
caveman_write_flag() {
    local mode=$1 flag dir tmp
    flag=$(caveman_flag_path)
    dir=$(dirname "$flag")
    [ -d "$dir" ] || mkdir -p "$dir" 2>/dev/null || return 0
    if [ -L "$flag" ]; then return 0; fi
    tmp="$dir/.caveman-active.$$.$(date +%s 2>/dev/null || printf 0)"
    ( umask 077 && printf '%s' "$mode" > "$tmp" ) 2>/dev/null || { rm -f -- "$tmp" 2>/dev/null; return 0; }
    mv -f -- "$tmp" "$flag" 2>/dev/null || rm -f -- "$tmp" 2>/dev/null
}

caveman_clear_flag() {
    rm -f -- "$(caveman_flag_path)" 2>/dev/null || true
}

# Symlink-safe, size-capped, whitelist-validated read. Echoes the mode on
# success; returns non-zero on any anomaly (no output).
caveman_read_flag() {
    local flag mode size
    flag=$(caveman_flag_path)
    [ -L "$flag" ] && return 1
    [ -f "$flag" ] || return 1
    size=$(wc -c < "$flag" 2>/dev/null | tr -d ' ')
    [ -z "$size" ] && return 1
    [ "$size" -gt "$CAVEMAN_MAX_FLAG_BYTES" ] && return 1
    mode=$(head -c "$CAVEMAN_MAX_FLAG_BYTES" "$flag" 2>/dev/null \
        | tr -d '\n\r' \
        | tr '[:upper:]' '[:lower:]' \
        | tr -cd 'a-z0-9-')
    caveman_is_valid_mode "$mode" || return 1
    printf '%s' "$mode"
}
