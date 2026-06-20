#!/usr/bin/env bash
# SessionStart hook: load <project_root>/state.md into the model context.
#
# Bash port of load-state-md.pl. The kernel ships both because downstream
# consumers can have either perl or bash on their PATH but not always
# both; this variant is wired on dogfood (NixOS) where the coordinator
# systemd unit's PATH lacks perl.
#
# Resolves <project_root> via `git rev-parse --git-common-dir` so worker
# sessions running in <project_root>/.worktrees/<slug>/ still pick up the
# canonical state.md in the main worktree. Falls back to cwd when not in
# a git repo so the hook is harmless outside spore.
set -euo pipefail

cwd="${CLAUDE_PROJECT_DIR:-$PWD}"

root="$cwd"
if gcd=$(git -C "$cwd" rev-parse --git-common-dir 2>/dev/null); then
    case "$gcd" in
        /*) abs="$gcd" ;;
        *)  abs="$cwd/$gcd" ;;
    esac
    if resolved=$(cd "$abs/.." 2>/dev/null && pwd -P); then
        root="$resolved"
    fi
fi

path="$root/state.md"

if [ ! -e "$path" ]; then
    cat > "$path" <<'STUB'
# coordinator state

(stub auto-minted on first SessionStart; rewrite after your first
meaningful turn. Re-read on every respawn; the transcript is not
durable.)

## Active tasks

| slug | intent | blocker | last seen |
|---|---|---|---|

## Open operator questions

## Recent events
STUB
fi

[ -r "$path" ] || exit 0
[ -s "$path" ] || exit 0

awk 'BEGIN {
    printf "{\"hookSpecificOutput\":{\"hookEventName\":\"SessionStart\",\"additionalContext\":\""
    first = 1
}
{
    line = $0
    gsub(/\\/, "\\\\", line)
    gsub(/"/, "\\\"", line)
    gsub(/\t/, "\\t", line)
    gsub(/\r/, "\\r", line)
    if (first) { first = 0 } else { printf "\\n" }
    printf "%s", line
}
END {
    printf "\"}}\n"
}' "$path"
