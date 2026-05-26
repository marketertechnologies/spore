#!/usr/bin/env bash
# spore-worker-brief: default SPORE_AGENT_BINARY for spore workers on
# an infected NixOS box. Spore exec's this inside a tmux session whose
# cwd is the worker's worktree, with SPORE_TASK_SLUG=<slug> in the env.
#
# We spawn an INTERACTIVE claude with the brief (tasks/<slug>.md) passed
# as a positional argument. This matches versality/spore's current
# lifecycle.go spawn shape (claude --dangerously-skip-permissions --
# "$(cat $SPORE_BRIEF_FILE)") and avoids `claude -p`, which from
# 2026-06-15 onward is metered against a separate Agent SDK monthly
# credit instead of the Claude plan.
#
# Falls back to interactive claude with no brief when the slug or brief
# is missing, so a misconfigured spawn does not strand the operator.
#
# Override knobs:
#   SPORE_WORKER_AGENT     binary to exec (default: claude)
set -euo pipefail

# Ensure the nix-managed claude wins over any /usr/local/bin/ shadow
# on infected hosts.
export PATH="/run/current-system/sw/bin:${PATH}"

slug="${SPORE_TASK_SLUG:-}"
agent="${SPORE_WORKER_AGENT:-claude}"
brief="tasks/${slug}.md"

if [[ -z "$slug" ]]; then
  echo "spore-worker-brief: SPORE_TASK_SLUG not set; dropping to interactive $agent" >&2
  exec "$agent" --dangerously-skip-permissions
fi

# Absorb the spawn race: tmux can open before the binary finishes
# copying tasks/<slug>.md into the worktree. Up to ~3s of backoff.
for _ in 1 2 3 4 5 6; do
  [[ -f "$brief" ]] && break
  sleep 0.5
done

if [[ ! -f "$brief" ]]; then
  echo "spore-worker-brief: no brief at $(pwd)/$brief after retry; dropping to interactive $agent" >&2
  exec "$agent" --dangerously-skip-permissions
fi

exec "$agent" --dangerously-skip-permissions -- "$(cat "$brief")"
