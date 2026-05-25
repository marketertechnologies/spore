**Status**: not-started

# retire the per-project bash token-monitor in favor of the kernel hook

## Problem

Two of three projects on this host still ship a 224-line bash
`bin/dev/spore-token-monitor` Stop hook (marketer and crm-gateway;
crm-gateway-ruby-client does not). Spore itself uses the kernel
hook directly in its own `.claude/settings.json`:

```
spore coordinator token-monitor
spore worker token-monitor
spore fleet replenish-hook
spore hooks watch-inbox
```

The bash shim and the kernel command overlap in purpose (context-budget
soft / hard caps on Stop) but they were authored independently. One
real workflow delta blocks the swap, plus a parallel event-ledger
system the bash hook builds out that the kernel covers a different
(better) way.

## The blocking delta

### Coordinator hard-cap action

- **Bash** hands the LLM a literal respawn-pane command that
  preserves the tmux session AND any attached SSH client:

  ```
  tmux respawn-pane -k -t spore/<project>/coordinator:0 \
    'exec claude... "$(cat <project>/bootstrap/coordinator/role.md)"'
  ```

- **Kernel** (`internal/fleet/coordinator.go:239`,
  `ReapCoordinator`):

  ```go
  _ = exec.Command("tmux", "kill-session", "-t", session).Run()
  ```

  `kill-session` drops the tmux session AND any attached SSH client.
  Real regression for SSH-attached operators on this host: their
  terminal closes when the coordinator caps.

## Non-blocking notes

### Hook placement + gating

- **Bash**: registered in the host-global
  `~/.claude/settings.json`. Each project's shim self-gates on
  `transcript_path` prefix.
- **Kernel**: registered in the per-project `.claude/settings.json`
  (Claude Code's hook layering handles project gating). Worker /
  non-coordinator gating is via `$SPORE_TASK_INBOX` presence.

Mechanical swap: copy `spore/.claude/settings.json`'s hook block
into each project, remove the per-project entry from the host-global
file.

### Env tunables (dropped from scope)

Audit on 2026-05-25 grepped every project tree, shell rc,
`/etc/spore/coordinator.env`, and systemd user-unit env for
`SPORE_TOKEN_SOFT` / `SPORE_TOKEN_HARD`. Zero external exporters.
Only the bash hooks themselves reference the names, with hardcoded
defaults `150000 / 190000`.

The kernel's defaults are equivalent in practice. If someone later
wants to tune caps per project, adding the env reads is a small
follow-up; not in scope here.

### events.jsonl + scan-worker-completions.pl

The bash hook writes a cross-project ledger at
`~/.local/state/spore/events.jsonl` with `{ts, kind, session_id,
slug, tokens}` per Stop event. crm-gateway (only) ships a perl
consumer, `bin/dev/scan-worker-completions.pl`, registered in the
host-global `~/.claude/settings.json` as a UserPromptSubmit hook
that tails the ledger and surfaces worker events as additionalContext
to the coordinator.

This entire writer + reader pair duplicates the kernel's inbox flow:

- Worker -> coordinator pokes go through
  `internal/hooks/notifycoordinator.go` (`spore hooks
  notify-coordinator`), which drops a file into the coordinator's
  project inbox.
- Coordinator side runs `spore hooks watch-inbox`
  (`internal/hooks/watchinbox.go`,
  `cmd/spore/hooks_cmd.go:35`), already wired in
  `spore/.claude/settings.json` as a Stop hook with
  `asyncRewake: true`. It blocks on inotify, exits 2 on message,
  Claude Code surfaces the rewake as additionalContext.
- The worker spawn (`internal/fleet/coordinator.go:127`,
  `internal/task/lifecycle.go:511`) already sets
  `SPORE_TASK_INBOX=<inbox>` for both coordinator and worker
  sessions, which is what the kernel's gating reads.

Decision (operator-confirmed 2026-05-25): retire the perl alongside
the bash hook. No compat write from the kernel to
`~/.local/state/spore/events.jsonl`; the kernel inbox flow is the
load-bearing path going forward. See `feedback_prefer_kernel_flow.md`
in project memory.

## Plan

### A. Kernel PR

Land before any per-project swap.

1. **`internal/fleet/coordinator.go` `ReapCoordinator`**: replace
   the unconditional `tmux kill-session` with a templated
   `tmux respawn-pane -k`. Template comes from
   `[coordinator].respawn_template` in `spore.toml`, defaulting to:

   ```
   tmux respawn-pane -k -t {session}:0 'exec {coordinator_launch}'
   ```

   where `{session}` is `CoordinatorSessionName(projectRoot)` and
   `{coordinator_launch}` is the host's
   `spore-coordinator-launch` path (`/usr/local/bin/...` today,
   to be stabilized by `host-shims-via-nix.md`).

   Preserve the existing tmux session and any attached SSH client.
   Fall back to `kill-session` only when the template is explicitly
   empty (so an operator can opt out).

2. **Tests**:
   - Unit test that `ReapCoordinator` issues `respawn-pane -k` (not
     `kill-session`) when the default template applies.
   - Unit test the empty-template fallback to `kill-session`.
   - Spore.toml round-trip test that `[coordinator].respawn_template`
     parses cleanly.

3. Out of scope (deferred follow-ups):
   - `SPORE_TOKEN_SOFT/HARD` env reads in
     `runCoordinatorTokenMonitor` (no exporters found in audit;
     add later if a use case appears).
   - `cmd/spore/coordinator_cmd.go` other coordinator subcommands.

### B. Per-project swap (one PR per project)

For marketer and crm-gateway, in either order, after A merges:

1. Add the kernel Stop-hook block to project `.claude/settings.json`
   (copy from `spore/.claude/settings.json` verbatim, minus the
   `gopls-lsp` plugin entry which is project-specific).
2. Delete `bin/dev/spore-token-monitor`.
3. crm-gateway only: delete `bin/dev/scan-worker-completions.pl`,
   the `~/.local/state/spore/scan-worker-completions/` cursor dir,
   and the doc references in `CLAUDE.md:149` and `AGENTS.md:95`
   ("Events are appended to `~/.local/state/spore/events.jsonl`...").
4. Remove the per-project Stop hook entry AND the
   UserPromptSubmit hook entry from the host-global
   `~/.claude/settings.json`. If no other project still depends on
   that global file for spore reasons, leave the file untouched
   beyond removing the spore-specific entries.
5. Smoke: trigger a soft cap (one large turn) and a hard cap
   (sequence of large turns); confirm wrap-up message + respawn-pane
   behavior; confirm SSH client survives. Dispatch a worker and
   confirm worker events still reach the coordinator via the inbox
   flow.
6. Stale ledger cleanup: `rm
   ~/.local/state/spore/events.jsonl` after both projects swap, so
   there is no orphan file giving the impression the system is still
   writing.

## Acceptance

1. `internal/fleet/coordinator.go` `ReapCoordinator` issues
   `tmux respawn-pane -k ...` against the coordinator's
   spore-coordinator-launch shim by default. SSH clients attached
   to that session survive a hard-cap fire. Test coverage in place.
2. `bin/dev/spore-token-monitor` is deleted from marketer and
   crm-gateway. Their `.claude/settings.json` carries the kernel
   hook block.
3. `bin/dev/scan-worker-completions.pl` is deleted from crm-gateway
   along with its cursor dir and the AGENTS.md / CLAUDE.md
   references.
4. The host-global `~/.claude/settings.json` no longer references
   either project's bash hook or the perl scan tool.
5. `~/.local/state/spore/events.jsonl` is gone (no kernel-side
   writer; no remaining reader).
6. Worker events still reach coordinator sessions via the kernel
   inbox flow; verified by a multi-worker smoke.

## Sequencing vs. host-shims-via-nix

The two specs touch overlapping surfaces (shims + hooks) but the
work is independent:

- The kernel PR (A) does not depend on shim delivery; it edits
  Go source and Go tests.
- The per-project swap (B) depends on the kernel PR but not on
  shim delivery.
- The respawn-pane template in A points at
  `/usr/local/bin/spore-coordinator-launch`, which is exactly the
  path `host-shims-via-nix` is migrating. As long as both specs
  agree to keep that path stable (symlink farm into the nix store
  is fine), neither blocks the other.

Pick whichever is cheapest to land first; the other slots in behind.

## Cross-references

- `internal/fleet/coordinator.go:239` (`ReapCoordinator`,
  current `kill-session`).
- `internal/fleet/coordinator.go:127` (worker spawn,
  `SPORE_TASK_INBOX=` env).
- `internal/task/lifecycle.go:511` (worker spawn env, same
  variable).
- `internal/hooks/notifycoordinator.go` (worker -> coordinator
  poke).
- `internal/hooks/watchinbox.go`, `cmd/spore/hooks_cmd.go:35`
  (`spore hooks watch-inbox`).
- `cmd/spore/coordinator_cmd.go:385` (`runCoordinatorTokenMonitor`,
  no env reads today; intentionally out of scope here).
- `cmd/spore/worker_cmd.go:46` (`runWorkerTokenMonitor`).
- `.claude/settings.json` (canonical kernel-hook layout).
- crm-gateway `bin/dev/scan-worker-completions.pl` and
  `bin/dev/spore-token-monitor`; marketer `bin/dev/spore-token-monitor`.
- Project memory: `feedback_prefer_kernel_flow.md` (the operator
  call: kernel mechanism over bash compat).
- Stale assumption from the bash shim: "workers must exit 0
  because `claude -p` hangs on injected stderr turns" is outdated;
  `spore-worker-brief` now spawns interactive claude (commit
  `fix(handover): spore-worker-brief uses interactive claude, drops
  -p`), so the kernel `exit 2` shape is safe.
- Related todo: `docs/todo/host-shims-via-nix.md` (overlapping shim
  delivery surface; sequenceable independently).
