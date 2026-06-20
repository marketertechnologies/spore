# Coordinator supervisor + token-cap rotation

The coordinator is a long-lived agent. Left as a single `exec` of the
driver (claude/codex), it dies the moment the driver exits, including on
a token-cap wrap, and the operator's attached pane closes with it. This
note describes the wisdom distilled from mcom's helm coordinator that
turns a token-cap wrap into an in-place rotation the operator never
notices, plus the supporting controls (exit-code contract, statusline,
headless guard, tmux session config).

All of it is generalizable kernel wisdom. The mcom-specific names (helm,
rover, HELM_*) are translated to the kernel names (coordinator, worker,
SPORE_*).

## Supervisor loop (automatic rotation)

`[coordinator].supervise = true` (or `SPORE_COORDINATOR_SUPERVISE=1`)
runs the driver inside an in-pane respawn loop instead of a single
`exec`:

```
while true; do
  SPORE_ACCOUNT_TIER="${SPORE_ACCOUNT_TIER:-${WT_ACCOUNT_TIER:-}}" <driver> "$(cat <role>)"
  sleep 1
done
```

When the token-monitor Stop hook fires its wrap reminder, the
coordinator flushes its state and exits the driver. The loop catches the
exit and boots a fresh driver in place, re-reading the role file. The
tmux pane stays alive across the rotation, so an attached operator keeps
its pane and full scrollback.

`SPORE_ACCOUNT_TIER` is derived from `WT_ACCOUNT_TIER` inline on every
iteration. The loop's bash text is frozen at `tmux new-session` time, so
a derivation outside the loop would leave a pane that started before a
tier change carrying a stale (or unset) tier forever and wrapping early
on every cycle. Deriving inside the loop heals the pane on the next
wrap. The same two vars are also threaded into the session env so the
supervisor loop and the token-monitor agree on the wrap cap.

Off by default: the single-exec lifecycle keeps the post-spawn settle
check able to detect a bad agent binary (a looping driver that fails to
exec would never let the session die, so the settle check could not tell
"spawned" from "instantly dead").

## Exit-code contract (systemd ExecStart model)

`spore coordinator spawn` blocks until the session dies, then exits with
a code a systemd unit can act on:

- `0`  clean signal shutdown (SIGTERM/SIGINT). No respawn.
- `1`  preflight failure (tier mismatch, agent exec failure). Pin with
       `RestartPreventExitStatus=1` so a respawn that would fix nothing
       never fires.
- `64` unexpected session death (external `tmux kill-session`, a sibling
       running `coordinator stop` without stopping the unit, a
       tmux-server crash). `Restart=on-failure` respawns it, bounded by
       `StartLimitBurst`.

This makes the long-lived ExecStart model a viable alternative to the
watchdog-timer model (a timer ticking `spore fleet reconcile`, which
calls `EnsureCoordinator` idempotently). The restart-guard unit settings
themselves are tier-2 wisdom (see the fleet module / ROC-15).

## Statusline

`spore statusline` renders a tmux `status-right` string with the live
token usage of the pane's agent and a five-step colour ramp
(green/cyan/yellow/red/red+bold) that warns before the wrap cap. The
default cap tracks the coordinator token-monitor soft cap so the red
zone lines up with the wrap reminder. `EnsureCoordinator` wires it on
spawn together with `status-interval 5`.

## Headless guard

`spore guard <binary> [args...]` refuses to exec the agent binary when
the spawn would be headless (stdin not a TTY and `$TMUX` unset), then
execs it. A headless agent has no attachable pane and orphans to PID 1
when its launcher exits. The kernel always spawns into `tmux
new-session`, so this is the backstop for a manual or misconfigured
launch. Wire it as `SPORE_COORDINATOR_AGENT="spore guard claude"`.

## tmux session config

On spawn the coordinator session gets, best-effort:

- `detach-on-destroy off` so killing the driver pane (a rotation or a
  manual kill) does not detach an attached operator client.
- `status-interval 5` + the statusline `status-right`.

The tmux socket is shared via `TMUX_TMPDIR`: the kernel's tmux helpers
inherit the caller's `TMUX_TMPDIR` so the coordinator session lands on
the same server as the operator's interactive tmux and worker sessions.
Pin it to `XDG_RUNTIME_DIR` on the host so a systemd unit (whose
environment does not export `TMUX_TMPDIR`) does not fall back to `/tmp`
and spawn an orphan server the idempotency check cannot see.
