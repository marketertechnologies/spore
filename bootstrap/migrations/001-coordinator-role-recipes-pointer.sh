#!/usr/bin/env bash
# Append a "## Recipes" section to the operator-owned shared
# coordinator role so coordinators on snapshot hosts (whose role file
# was frozen at infect time) learn about `spore recipes ls`.
#
# Idempotent: a second run finds the marker and exits 0 without
# touching the file.
#
# Skipped silently when the file does not exist (e.g. a fresh host
# whose role.md will be installed from the bundled template, which
# already carries this section).

target="$HOME/.config/spore/coordinator-role.md"
marker="## Recipes"

if [ ! -f "$target" ]; then
    echo "spore migrate 001: $target absent; skipping (bundled role carries the section)"
    exit 0
fi

if grep -qxF "$marker" "$target"; then
    exit 0
fi

cat >> "$target" <<'EOF'

## Recipes

Reusable how-to documents for talking to external systems (Jira,
Sentry, Notion, GitHub, etc.) live in the embedded recipe library.

- `spore recipes ls` -- list available recipes by name and title.
- `spore recipes show <name>` -- print the raw markdown body of one
  recipe to stdout.

Check the list before composing a new external-API call from
scratch. Recipes encode the operator's preferred auth path, the
gotchas, and the working URL pattern; using them keeps coordinators
across hosts in sync.
EOF

echo "spore migrate 001: appended Recipes section to $target"
