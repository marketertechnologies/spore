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
#     - host-wide content shared across every project; lets the
#     operator factor common respawn / wrap-up / operating-regime
#     boilerplate out of per-project role files.
#   $SPORE_COORDINATOR_ROLE - project-specific delta set by the
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
#   SPORE_COORDINATOR_SESSION      codex: key for the thread record the
#                                  inbox sidecar reads (default: the
#                                  pane's tmux session name).
set -euo pipefail

# Ensure the nix-managed claude / codex / spore win over any
# /usr/local/bin/ shadow on infected hosts.
export PATH="/run/current-system/sw/bin:${PATH}"

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
    args=(--dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust --no-alt-screen --disable apps)
    if [[ -n "$model" ]]; then
      args+=(-m "$model")
    fi
    if [[ -n "$effort" ]]; then
      args+=(-c "model_reasoning_effort=\"$effort\"")
    fi
    # Codex hooks cannot wake an idle thread; `codex queue` can, but
    # only through the per-CODEX_HOME app-server daemon the TUI attaches
    # to. Each coordinator session gets its own CODEX_HOME (own daemon,
    # rollouts, queue) so parallel coordinators never share one, and
    # the daemon is restarted from this pane's env. User config stays
    # in ~/.codex and is symlinked in; Codex writes through the links.
    # The sidecar delivers inbox envelopes to the thread the
    # SessionStart hook records under this session's key.
    if [[ -z "${SPORE_COORDINATOR_SESSION:-}" && -n "${TMUX:-}" ]]; then
      tmux_target=()
      if [[ -n "${TMUX_PANE:-}" ]]; then
        tmux_target=(-t "$TMUX_PANE")
      fi
      SPORE_COORDINATOR_SESSION=$(tmux display-message -p "${tmux_target[@]}" '#S' 2>/dev/null || true)
    fi
    export SPORE_COORDINATOR_SESSION="${SPORE_COORDINATOR_SESSION:-}"
    if [[ -n "${SPORE_TASK_INBOX:-}" && -n "$SPORE_COORDINATOR_SESSION" ]]; then
      user_home="${CODEX_HOME:-$HOME/.codex}"
      export CODEX_HOME="$(dirname "$SPORE_TASK_INBOX")/codex-home/${SPORE_COORDINATOR_SESSION//\//_}"
      mkdir -p "$CODEX_HOME"
      for f in auth.json config.toml hooks.json AGENTS.md; do
        if [[ -e "$user_home/$f" && ! -e "$CODEX_HOME/$f" ]]; then
          ln -sfn "$user_home/$f" "$CODEX_HOME/$f"
        fi
      done
      codex app-server daemon stop >/dev/null 2>&1 || true
    fi
    codex app-server daemon start >/dev/null 2>&1 ||
      echo "spore-coordinator-launch: codex app-server daemon start failed; inbox wake disabled" >&2
    sidecar=""
    if [[ -n "${SPORE_TASK_INBOX:-}" && -n "$SPORE_COORDINATOR_SESSION" ]]; then
      mkdir -p "$SPORE_TASK_INBOX"
      spore hooks watch-inbox --codex --pid $$ \
        </dev/null >/dev/null 2>>"$(dirname "$SPORE_TASK_INBOX")/codex-watch-inbox.log" &
      sidecar=$!
    fi
    rc=0
    codex "${args[@]}" "${prompt[@]}" || rc=$?
    if [[ -n "$sidecar" ]]; then
      kill "$sidecar" 2>/dev/null || true
    fi
    if (( rc != 0 )); then
      exec /usr/local/bin/spore-greet-coordinator
    fi
    ;;
  *)
    echo "spore-coordinator-launch: unsupported SPORE_COORDINATOR_PROVIDER=$provider" >&2
    exec /usr/local/bin/spore-greet-coordinator
    ;;
esac
