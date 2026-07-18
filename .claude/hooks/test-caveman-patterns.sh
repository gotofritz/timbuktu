#!/usr/bin/env bash
# Regression tests for portable grep patterns in caveman-mode-tracker.sh.
# Run on macOS (BSD grep) and Linux (GNU grep) to verify no \b dependency.

set -u

PASS=0
FAIL=0

ACT='(^|[[:space:]])(activate|enable|turn on|start|talk like)[[:space:]].*caveman'
NEG='(^|[[:space:]])(stop|disable|turn off|deactivate)([[:space:]]|$)'
DEACT='(^|[[:space:]])(stop|disable|deactivate|turn off)[[:space:]].*caveman|caveman[[:space:]].*(stop|disable|deactivate|turn off)|(^|[[:space:]])normal mode([[:space:]]|$)'

match() {
    local desc="$1" pattern="$2" input="$3" want="$4"
    if printf '%s' "$input" | grep -qE "$pattern"; then got=yes; else got=no; fi
    if [ "$got" = "$want" ]; then
        PASS=$((PASS + 1))
    else
        FAIL=$((FAIL + 1))
        printf 'FAIL: %s — input=%q want=%s got=%s\n' "$desc" "$input" "$want" "$got"
    fi
}

# Activation — should match
match "activate caveman"        "$ACT" "activate caveman"        yes
match "enable caveman mode"     "$ACT" "enable caveman mode"     yes
match "turn on caveman"         "$ACT" "turn on caveman"         yes
match "start caveman"           "$ACT" "start caveman"           yes
match "talk like caveman"       "$ACT" "talk like caveman"       yes
match "please activate caveman" "$ACT" "please activate caveman" yes

# Activation — should NOT match
match "how does caveman mode work" "$ACT" "how does caveman mode work" no
match "reactivate caveman"         "$ACT" "reactivate caveman"         no
match "caveman is great"           "$ACT" "caveman is great"           no

# Negation (deactivation words present) — should match
match "stop present"    "$NEG" "stop caveman" yes
match "disable present" "$NEG" "disable it"   yes
match "turn off eol"    "$NEG" "turn off"     yes

# Negation — should NOT match (word is prefix of longer word)
match "disabled"      "$NEG" "disabled"      no
match "deactivating"  "$NEG" "deactivating"  no

# Deactivation triggers — should match
match "stop caveman"       "$DEACT" "stop caveman"       yes
match "disable caveman"    "$DEACT" "disable caveman"    yes
match "caveman stop"       "$DEACT" "caveman stop"       yes
match "caveman disable"    "$DEACT" "caveman disable"    yes
match "normal mode"        "$DEACT" "normal mode"        yes
match "enable normal mode" "$DEACT" "enable normal mode" yes
match "normal mode please" "$DEACT" "normal mode please" yes

# Deactivation — should NOT match
match "normalmode"         "$DEACT" "normalmode"         no
match "abnormal mode"      "$DEACT" "abnormal mode"      no
match "caveman is awesome" "$DEACT" "caveman is awesome" no

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
