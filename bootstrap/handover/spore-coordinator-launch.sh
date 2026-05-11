#!/usr/bin/env bash
# spore-coordinator-launch: default SPORE_COORDINATOR_AGENT for spore
# coordinators on an infected NixOS box. Spore exec's this in the
# project root with SPORE_COORDINATOR_ROLE pointing at the resolved
# role file (may not exist).
#
# We launch the selected interactive agent, seeding (shared-role +
# project-role) as the first user message. Each part is optional;
# missing parts are skipped and a missing pair yields an unseeded
# agent. If the agent exits immediately because first login is still
# needed, the coordinator pane stays alive with a clear shell.
#
# Role resolution:
#   $SPORE_COORDINATOR_ROLE_SHARED (default
#     "${XDG_CONFIG_HOME:-$HOME/.config}/spore/coordinator-role.md")
#     — host-wide content shared across every project; lets the
#     operator factor common respawn / wrap-up / operating-regime
#     boilerplate out of per-project role files.
#   $SPORE_COORDINATOR_ROLE — project-specific delta set by the
#     kernel to <project>/bootstrap/coordinator/role.md.
# When both are readable and non-empty they are concatenated with a
# blank line between them.
#
# Override knobs:
#   SPORE_COORDINATOR_PROVIDER     claude or codex (default: claude)
#   SPORE_COORDINATOR_MODEL        model to pass to the selected CLI
#   SPORE_COORDINATOR_EFFORT       codex effort (default: high)
#   SPORE_COORDINATOR_ROLE_SHARED  override shared role path; point at
#                                  a non-existent file to disable.
set -euo pipefail

provider="${SPORE_COORDINATOR_PROVIDER:-claude}"
model="${SPORE_COORDINATOR_MODEL:-}"
effort="${SPORE_COORDINATOR_EFFORT:-high}"
role="${SPORE_COORDINATOR_ROLE:-}"
shared="${SPORE_COORDINATOR_ROLE_SHARED:-${XDG_CONFIG_HOME:-$HOME/.config}/spore/coordinator-role.md}"

payload=""
if [[ -r "$shared" && -s "$shared" ]]; then
  payload+=$(cat "$shared")
  payload+=$'\n\n'
fi
if [[ -n "$role" && -r "$role" && -s "$role" ]]; then
  payload+=$(cat "$role")
fi

prompt=()
if [[ -n "$payload" ]]; then
  prompt=("$payload")
fi

case "$provider" in
  claude)
    args=(--dangerously-skip-permissions)
    if [[ -n "$model" ]]; then
      args+=(--model "$model")
    fi
    claude "${args[@]}" "${prompt[@]}" || exec /usr/local/bin/spore-greet-coordinator
    ;;
  codex)
    args=(--dangerously-bypass-approvals-and-sandbox --no-alt-screen --disable apps)
    if [[ -n "$model" ]]; then
      args+=(-m "$model")
    fi
    if [[ -n "$effort" ]]; then
      args+=(-c "model_reasoning_effort=\"$effort\"")
    fi
    codex "${args[@]}" "${prompt[@]}" || exec /usr/local/bin/spore-greet-coordinator
    ;;
  *)
    echo "spore-coordinator-launch: unsupported SPORE_COORDINATOR_PROVIDER=$provider" >&2
    exec /usr/local/bin/spore-greet-coordinator
    ;;
esac
