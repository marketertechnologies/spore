#!/usr/bin/env bash
# Run one scenario against spore built at two refs and print both runs.
#
#   scripts/pr-before-after.sh <before-ref> <after-ref> <scenario.sh>
#
# The scenario runs twice, each time in a fresh sandbox: HOME, XDG dirs and
# the tmux server are private, SPORE_* is cleared, and $SPORE is the binary
# under test. It never touches the live fleet, ~/.claude or /etc.
# Scenario env: SPORE (binary), SANDBOX (scratch dir), SIDE (before|after),
# SRC (checkout of the ref, for fixtures).
set -euo pipefail
# An inherited $TMUX overrides TMUX_TMPDIR and points tmux at the operator's
# live server; a stray kill-server would log them off.
unset TMUX TMUX_PANE

[ $# -eq 3 ] || { echo "usage: $0 <before-ref> <after-ref> <scenario.sh>" >&2; exit 2; }
before=$1 after=$2 scenario=$(realpath "$3")
repo=$(git rev-parse --show-toplevel)
work=$(mktemp -d "${TMPDIR:-/tmp}/spore-ba.XXXXXX")
longest="$work/before/sandbox/tmux/tmux-$(id -u)/default"
if [ ${#longest} -gt 100 ]; then
	rmdir "$work"
	echo "TMPDIR too long for a tmux socket (${#longest} > 100 chars); use a shorter TMPDIR" >&2
	exit 2
fi
trap 'tmux_kill; for s in before after; do [ -d "$work/$s/src" ] && git -C "$repo" worktree remove --force "$work/$s/src"; done' EXIT

tmux_kill() {
	local sock
	for side in before after; do
		sock="$work/$side/sandbox/tmux/tmux-$(id -u)/default"
		[ -S "$sock" ] && env -u TMUX -u TMUX_PANE tmux -S "$sock" kill-server 2>/dev/null || true
	done
}

build() {
	local side=$1 ref=$2 src="$work/$1/src"
	mkdir -p "$work/$side"
	git -C "$repo" worktree add -q --detach "$src" "$ref"
	(cd "$src" && go build -o "$work/$side/bin/spore" ./cmd/spore)
	echo "$side: $(git -C "$repo" rev-parse --short "$ref") ($ref)"
}

run() {
	local side=$1 sb="$work/$1/sandbox"
	mkdir -p "$sb"/{home,state,config,cache,tmux}
	echo
	echo "=================== $side ==================="
	env -i PATH="$work/$side/bin:$PATH" TERM="${TERM:-xterm}" LANG="${LANG:-C.UTF-8}" \
		HOME="$sb/home" XDG_STATE_HOME="$sb/state" XDG_CONFIG_HOME="$sb/config" \
		XDG_CACHE_HOME="$sb/cache" TMUX_TMPDIR="$sb/tmux" \
		GOCACHE="${GOCACHE:-$(go env GOCACHE)}" GOMODCACHE="$(go env GOMODCACHE)" \
		GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.invalid \
		GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.invalid \
		SPORE="$work/$side/bin/spore" SANDBOX="$sb" SIDE="$side" SRC="$work/$side/src" \
		EDITOR=true VISUAL=true \
		timeout "${BA_TIMEOUT:-300}" bash -c 'cd "$SANDBOX" && bash "$0"' "$scenario" </dev/null 2>&1 || echo "(scenario exited $?)"
}

build before "$before"
build after "$after"
run before
run after
echo
echo "sandbox kept at $work"
