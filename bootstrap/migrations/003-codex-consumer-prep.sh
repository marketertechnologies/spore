#!/usr/bin/env bash
# Prepare every fleet consumer on this host for a Codex coordinator:
#   1. Write an AGENTS.md byte copy next to every git-tracked
#      subdirectory CLAUDE.md that has none (the agents-mirror lint
#      contract). Existing AGENTS.md files are never touched.
#   2. Drop the stale "No token-monitor Stop hook installed yet"
#      bullet from bootstrap/coordinator/{role,dogfood-role}.md.
#   3. Append a "## Codex coordinator" section to the shared operator
#      role at ~/.config/spore/coordinator-role.md.
#
# Discovery: the spore-fleet module renders one
# spore-fleet-reconcile-tasks-<name>.path unit per configured project
# into ~/.config/systemd/user/ (home-manager), with
# PathChanged=<project>/tasks. That is the host's authoritative fleet
# project list, so a project root is the PathChanged value minus
# "/tasks".
#
# Consumer working trees are edited but never committed. Each changed
# file is printed so the operator can have that project's coordinator
# commit it. A second run finds nothing to change.

set -eu

home_root="${HOME:?HOME unset}"
units_dir="$home_root/.config/systemd/user"
stale_prefix='- **No token-monitor Stop hook installed yet**.'
codex_marker="## Codex coordinator"

codex_section=$(cat <<'EOF'
## Codex coordinator

- `/select-agent` (or `spore coordinator swap --to codex|claude`)
  swaps the driver on this pane. Flush `state.md` first.
- Codex shares the Claude memory dir and gets `state.md` injected on
  SessionStart. Skills live in `~/.agents/skills`.
- Codex reads `AGENTS.md`, not `CLAUDE.md`. Keep every AGENTS.md an
  exact copy of its sibling CLAUDE.md (the `agents-mirror` lint).
- Known gap: claude.ai MCP connectors are absent under Codex.
EOF
)

changed=0

shopt -s nullglob
units=("$units_dir"/spore-fleet-reconcile-tasks-*.path)
shopt -u nullglob

roots=""
for unit in "${units[@]}"; do
    path=$(sed -n 's/^PathChanged=//p' "$unit" | head -n 1)
    [ -n "$path" ] || continue
    roots="$roots${path%/tasks}"$'\n'
done
roots=$(printf '%s' "$roots" | sort -u)

while IFS= read -r root; do
    [ -n "$root" ] || continue
    [ -d "$root/.git" ] || [ -f "$root/.git" ] || continue

    while IFS= read -r -d '' rel; do
        case "$rel" in
            .worktrees/* | */.worktrees/* | vendor/* | */vendor/* | node_modules/* | */node_modules/*) continue ;;
        esac
        src="$root/$rel"
        dst="$(dirname "$src")/AGENTS.md"
        [ -f "$src" ] || continue
        [ -e "$dst" ] && continue
        cp "$src" "$dst"
        echo "spore migrate 003: wrote $dst"
        changed=1
    done < <(git -c "safe.directory=$root" -C "$root" ls-files -z -- '*/CLAUDE.md')

    for role in "$root/bootstrap/coordinator/role.md" "$root/bootstrap/coordinator/dogfood-role.md"; do
        [ -f "$role" ] || continue
        grep -qF -- "$stale_prefix" "$role" || continue
        tmp="$role.spore-migrate-003"
        awk -v prefix="$stale_prefix" '
            skip && /^[ \t]+[^ \t]/ { next }
            { skip = 0 }
            index($0, prefix) == 1 { skip = 1; next }
            { print }
        ' "$role" > "$tmp"
        if cmp -s "$tmp" "$role"; then
            rm -f "$tmp"
            continue
        fi
        cat "$tmp" > "$role"
        rm -f "$tmp"
        echo "spore migrate 003: removed stale token-monitor bullet from $role"
        changed=1
    done
done <<<"$roots"

shared_role="$home_root/.config/spore/coordinator-role.md"
if [ -f "$shared_role" ] && ! grep -qxF "$codex_marker" "$shared_role"; then
    printf '\n%s\n' "$codex_section" >> "$shared_role"
    echo "spore migrate 003: appended Codex coordinator section to $shared_role"
    changed=1
fi

if [ "$changed" -eq 0 ]; then
    echo "spore migrate 003: fleet consumers already prepared for Codex"
fi
