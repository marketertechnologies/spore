#!/usr/bin/env bash
# Passive post-upgrade smoke test for a spore host (read-only).
#
# Usage: scripts/smoke-post-upgrade.sh [expected-version] [repo-dir...]
#   expected-version  e.g. 0.40.0; skipped when empty or "-".
#   repo-dir          repos whose .claude/settings.json is checked for
#                     hooks the managed drop-in already runs. Default:
#                     every $HOME/*/.claude/settings.json.
#
# Checks: binary version, managed drop-in present and equal to what the
# binary renders, every /run/current-system/sw path it names exists,
# migration 004 backup present and the user settings already pruned,
# repo settings free of duplicated hooks, Codex hooks.json path, and
# spore tmux panes started before the drop-in (they still run the old
# hooks until respawned). Nothing is modified: prune runs on copies.
#
# Active probes (deny rules, force-push guard, role-loop end to end) are
# not here; run the P0.1 / P0.5 probes and a throwaway role-loop task.
# SPORE_SMOKE_SW / SPORE_SMOKE_DROPIN override the system paths (testing).
set -u

want="${1:-}"
[ "$want" = "-" ] && want=""
shift || true

SW="${SPORE_SMOKE_SW:-/run/current-system/sw}"
DROPIN="${SPORE_SMOKE_DROPIN:-/etc/claude-code/managed-settings.d/50-spore.json}"
fails=0 warns=0
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

pass() { printf 'PASS  %s\n' "$*"; }
fail() { printf 'FAIL  %s\n' "$*"; fails=$((fails + 1)); }
warn() { printf 'WARN  %s\n' "$*"; warns=$((warns + 1)); }

# 1. Binary.
if [ -x "$SW/bin/spore" ]; then
  sysver="$("$SW/bin/spore" --version 2>&1 | head -1)"
  pathver="$(spore --version 2>&1 | head -1)"
  pass "system spore: $sysver"
  [ "$sysver" = "$pathver" ] || warn "spore on PATH ($pathver) differs from $SW/bin/spore"
  if [ -n "$want" ]; then
    case "$sysver" in *"$want"*) pass "version matches $want" ;; *) fail "version: want $want, got $sysver" ;; esac
  fi
else
  fail "$SW/bin/spore missing (claudeManagedSettings disabled or upgrade not applied)"
fi

# 2. Managed drop-in.
if [ -f "$DROPIN" ]; then
  if jq -e . "$DROPIN" >/dev/null 2>&1; then
    pass "drop-in present and valid JSON: $DROPIN"
  else
    fail "drop-in is not valid JSON: $DROPIN"
  fi
  if [ -x "$SW/bin/spore" ]; then
    "$SW/bin/spore" hooks settings --managed --spore "$SW/bin/spore" \
      --hooks-dir "$SW/share/spore/hooks" >"$tmp/rendered.json" 2>/dev/null
    if jq -e '.enabledPlugins' "$DROPIN" >/dev/null 2>&1; then
      warn "drop-in enables the plugin; skipping the render comparison"
    elif diff -q <(jq -S . "$DROPIN") <(jq -S . "$tmp/rendered.json") >/dev/null; then
      pass "drop-in equals what the installed binary renders"
    else
      fail "drop-in differs from 'spore hooks settings --managed' (stale rebuild?)"
    fi
  fi
  missing=0
  for p in $(grep -oE "$SW/[A-Za-z0-9_./-]+" "$DROPIN" | sort -u); do
    [ -e "$p" ] || { fail "drop-in path missing: $p"; missing=1; }
  done
  [ "$missing" = 0 ] && pass "every $SW path in the drop-in exists"
  deny="$(jq -r '.permissions.deny[]?' "$DROPIN")"
  for rule in SendMessage 'Read(~/.config/spore/**/secrets.env)' 'Read(~/.claude/.credentials.json)' 'Bash(git push --force *)'; do
    grep -qxF "$rule" <<<"$deny" || fail "drop-in deny lacks $rule"
  done
  for cmd in 'hooks pretooluse' 'hooks engineer-check' 'hooks watch-inbox' 'load-state-md.pl' 'block-bg-bash.pl'; do
    grep -qF "$cmd" "$DROPIN" || fail "drop-in lacks hook: $cmd"
  done
else
  fail "drop-in missing: $DROPIN"
fi

# Copies a settings file and reports what prune-settings would remove.
would_prune() {
  local src="$1" copy="$tmp/prune-$RANDOM.json" out
  cp "$src" "$copy"
  if ! out="$("$SW/bin/spore" hooks prune-settings --file "$copy" --managed-file "$DROPIN" 2>&1)"; then
    printf 'prune-settings failed: %s\n' "$out"
    return 1
  fi
  if grep -qF 'left as is' <<<"$out"; then
    printf 'prune-settings skipped it: %s\n' "$out"
    return 1
  fi
  grep -F 'removed' <<<"$out" | sed "s|$copy|$src|" || true
}

# 3. User settings: migration 004 backed up and pruned.
us="$HOME/.claude/settings.json"
if ls "$us".spore-bak-* >/dev/null 2>&1; then
  pass "migration 004 backup: $(ls -1 "$us".spore-bak-* | tail -1)"
else
  warn "no $us.spore-bak-* (migration 004 did not prune, or nothing matched)"
fi
if [ -L "$us" ]; then
  warn "$us is a symlink; prune its source by hand"
elif [ -f "$us" ] && [ -f "$DROPIN" ] && [ -x "$SW/bin/spore" ]; then
  if ! left="$(would_prune "$us")"; then fail "$us: $left"
  elif [ -z "$left" ]; then pass "$us carries nothing the drop-in provides"; else fail "$us still duplicates:"$'\n'"$left"; fi
fi

# 4. Repo settings: duplicated hooks run twice.
if [ "$#" -gt 0 ]; then repos=("$@"); else repos=("$HOME"/*/); fi
for r in "${repos[@]}"; do
  f="${r%/}/.claude/settings.json"
  [ -f "$f" ] && [ -f "$DROPIN" ] && [ -x "$SW/bin/spore" ] || continue
  if ! left="$(would_prune "$f")"; then fail "$f: $left"
  elif [ -z "$left" ]; then pass "$f: no duplicated hooks"; else fail "$f duplicates (run 'spore hooks prune-settings --file $f'):"$'\n'"$left"; fi
done

# 5. Codex hooks.
ch="${CODEX_HOME:-$HOME/.codex}/hooks.json"
if [ -f "$ch" ]; then
  if grep -q '/nix/store/[^"]*/bin/spore' "$ch"; then
    fail "$ch calls spore by store path (GC risk)"
  elif grep -qF "$SW/bin/spore" "$ch"; then
    pass "$ch calls $SW/bin/spore"
  else
    warn "$ch names no spore binary"
  fi
fi

# 6. Live panes older than the drop-in still run the old hooks.
if [ -f "$DROPIN" ] && command -v tmux >/dev/null; then
  since="$(stat -c %Y "$DROPIN")"
  stale=0
  while read -r target pid; do
    start="$(date -d "$(ps -o lstart= -p "$pid" 2>/dev/null)" +%s 2>/dev/null)" || continue
    if [ "$start" -lt "$since" ]; then
      warn "pane $target started before the drop-in; respawn it"
      stale=1
    fi
  done < <(env -u TMUX tmux list-panes -a -F '#{session_name}:#{window_index}.#{pane_index} #{pane_pid}' 2>/dev/null | grep '^spore')
  [ "$stale" = 0 ] && pass "every spore pane started after the drop-in"
fi

printf '\n%d fail, %d warn\n' "$fails" "$warns"
[ "$fails" = 0 ]
