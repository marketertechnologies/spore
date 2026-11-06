#!/usr/bin/env bash
# Prepare this host for a Codex coordinator: append a
# "## Codex coordinator" section to the shared operator role at
# ~/.config/spore/coordinator-role.md.
#
# Host-side only. Consumer repos are never touched: Codex reads
# subdirectory CLAUDE.md files through the
# project_doc_fallback_filenames launch flag, so no AGENTS.md mirrors
# are needed. A second run finds nothing to change.

set -eu

home_root="${HOME:?HOME unset}"
codex_marker="## Codex coordinator"

codex_section=$(cat <<'EOF'
## Codex coordinator

- `/select-agent` (or `spore coordinator swap --to codex|claude`)
  swaps the driver on this pane. Flush `state.md` first.
- Codex shares the Claude memory dir and gets `state.md` injected on
  SessionStart. Skills live in `~/.agents/skills`.
- Spore launches Codex with `project_doc_fallback_filenames` set to
  `CLAUDE.md`, so Codex reads CLAUDE.md wherever no AGENTS.md exists.
  Consumer repos need no AGENTS.md mirrors.
- Known gap: claude.ai MCP connectors are absent under Codex.
EOF
)

shared_role="$home_root/.config/spore/coordinator-role.md"
if [ -f "$shared_role" ] && ! grep -qxF "$codex_marker" "$shared_role"; then
    printf '\n%s\n' "$codex_section" >> "$shared_role"
    echo "spore migrate 003: appended Codex coordinator section to $shared_role"
else
    echo "spore migrate 003: host already prepared for Codex"
fi
