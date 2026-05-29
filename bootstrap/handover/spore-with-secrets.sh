#!/usr/bin/env bash
# spore-with-secrets: host shim that sources layered spore secrets
# (~/.config/spore/secrets.env then
# ~/.config/spore/<project>/secrets.env) into the environment and
# execs the passed command. Per-project entries override the global
# on key collisions because they are sourced second.
#
# Project resolution:
#   1. $SPORE_PROJECT_ROOT basename when set (coordinator / worker pane).
#   2. `git rev-parse --show-toplevel` basename otherwise.
#   3. Skipped if neither resolves; only the global file is sourced.
#
# Files are optional; missing files are skipped without error.
#
# Usage:
#   spore-with-secrets <cmd> [args...]
#   spore-with-secrets gh pr create --title ...
set -euo pipefail

if [ $# -eq 0 ]; then
  echo "usage: spore-with-secrets <cmd> [args...]" >&2
  exit 2
fi

config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/spore"

project_root="${SPORE_PROJECT_ROOT:-}"
if [ -z "$project_root" ]; then
  project_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
fi
project=""
[ -n "$project_root" ] && project="$(basename "$project_root")"

set -a
[ -f "$config_dir/secrets.env" ] && . "$config_dir/secrets.env"
[ -n "$project" ] && [ -f "$config_dir/$project/secrets.env" ] && . "$config_dir/$project/secrets.env"
set +a

exec "$@"
