#!/usr/bin/env bash
# Drop spore's hooks and permissions.deny from ~/.claude/settings.json
# once the spore-fleet module's managed settings drop-in provides them,
# so each hook runs once, from the Nix store copy.
#
# Matching is by command (program base name plus arguments), so stale
# copies such as ~/.claude/hooks/load-state-md.pl or a bare `spore`
# match their store counterparts. Everything else in the file stays.
# The original is backed up to settings.json.spore-bak-<stamp> first.
#
# Without the drop-in (claudeManagedSettings.enable = false, or a host
# off the module) the user file is the only source of the hooks, so
# this leaves it alone. Enabling the drop-in later: run
# `spore hooks prune-settings --file ~/.claude/settings.json` by hand.

set -eu

managed="${SPORE_MANAGED_SETTINGS:-/etc/claude-code/managed-settings.d/50-spore.json}"
target="${HOME:?HOME unset}/.claude/settings.json"

if [ ! -f "$managed" ]; then
    echo "spore migrate 004: $managed absent; $target left as is"
    exit 0
fi
if [ ! -f "$target" ]; then
    echo "spore migrate 004: no $target; nothing to prune"
    exit 0
fi
spore hooks prune-settings --file "$target" --managed-file "$managed"
