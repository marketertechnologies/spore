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
soft / hard caps on Stop) but they were authored independently and
differ in three load-bearing ways. Until those gaps close, swapping
projects to the kernel hook is a workflow regression for the operator.

## The three deltas

### 1. Hook placement + gating

- **Bash**: registered in the host-global
  `~/.claude/settings.json`. Each project's shim self-gates on
  `transcript_path` prefix, so a session in marketer ignores the
  crm-gateway shim and vice versa.
- **Kernel**: registered in the per-project
  `.claude/settings.json` (Claude Code's hook layering handles
  project gating). Worker / non-coordinator gating is via
  `$SPORE_TASK_INBOX` presence.

Net: swap shape is "move the registration from one settings file to
another", but only safe after deltas 2 and 3 land.

### 2. Coordinator hard-cap action

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

### 3. Coordinator env tunables

- **Bash** reads `SPORE_TOKEN_SOFT` and `SPORE_TOKEN_HARD` and uses
  them as the soft / hard percent caps.
- **Kernel** `runCoordinatorTokenMonitor`
  (`cmd/spore/coordinator_cmd.go:385`) reads no env vars. The worker
  side has three tunables; the coordinator side has zero.

Before swapping, audit each project for `export
SPORE_TOKEN_SOFT=...` or `SPORE_TOKEN_HARD=...` in shell rcs,
`spore.toml`, or systemd units. If any project actually relies on
non-default caps, the swap fails silently (caps revert to kernel
defaults) until the kernel honors the env.

## Plan

### A. Kernel PR (one)

Land before any per-project swap.

1. **`internal/fleet/coordinator.go` `ReapCoordinator`**: replace
   the unconditional `tmux kill-session` with a templated
   `tmux respawn-pane -k`. Template comes from
   `[coordinator].respawn_template` in `spore.toml`, defaulting to:

   ```
   tmux respawn-pane -k -t {session}:0 'exec {coordinator_launch}'
   ```

   where `{session}` is `CoordinatorSessionName(projectRoot)` and
   `{coordinator_launch}` is
   `/usr/local/bin/spore-coordinator-launch` (or whatever the host
   resolves to once `host-shims-via-nix` lands).

   Preserve the existing tmux session and any attached SSH client.

2. **`cmd/spore/coordinator_cmd.go` `runCoordinatorTokenMonitor`**:
   read `SPORE_TOKEN_SOFT` and `SPORE_TOKEN_HARD` (parsing percent
   values), falling back to the current hard-coded defaults. Match
   the worker token-monitor's env-tunable shape so both sides are
   consistent.

3. **Optional**: surface the same knobs as `[coordinator]`
   `token_soft` / `token_hard` keys in `spore.toml`, with env vars
   as override.

4. Tests: a unit test that `ReapCoordinator` issues
   `respawn-pane -k` (not `kill-session`) when a template is set,
   and falls back to `kill-session` only when the template is
   explicitly empty.

### B. events.jsonl reader audit

The bash token-monitor writes to a cross-project
`~/.local/state/spore/events.jsonl` ledger. Before retiring the
shim, grep marketer + crm-gateway + this repo for any reader of
that path (`events.jsonl`, `SPORE_EVENTS_LOG`, etc.). If a reader
exists, decide whether the kernel ledger
(`~/.local/state/spore/<project>/...`) already covers it or whether
we need a compat write.

### C. Per-project swap (one PR per project)

For marketer and crm-gateway, in either order:

1. Add the kernel hook block to project `.claude/settings.json`
   (copy from `spore/.claude/settings.json` verbatim, minus the
   `gopls-lsp` plugin entry which is project-specific).
2. Delete `bin/dev/spore-token-monitor`.
3. Remove the per-project Stop hook entry from the host-global
   `~/.claude/settings.json`. Confirm no other projects on the
   host still depend on it; if so, leave the entry but gate it on
   the remaining project's transcript path.
4. Smoke: trigger a soft cap (one large turn) and a hard cap
   (sequence of large turns) and confirm wrap-up + respawn-pane
   behavior matches the operator's expectation.

## Acceptance

1. `internal/fleet/coordinator.go` `ReapCoordinator` issues
   `tmux respawn-pane -k ...` against the coordinator's
   spore-coordinator-launch shim by default. SSH clients attached
   to that session survive a hard-cap fire.
2. `SPORE_TOKEN_SOFT=N SPORE_TOKEN_HARD=M spore coordinator
   token-monitor` honors the overrides; default-only run uses the
   prior kernel defaults.
3. `bin/dev/spore-token-monitor` is deleted from marketer and
   crm-gateway. Their `.claude/settings.json` carries the kernel
   hook block.
4. The host-global `~/.claude/settings.json` no longer references
   either project's bash hook (or is rewritten to gate on something
   sane if a non-spore reason still requires a global entry).
5. `~/.local/state/spore/events.jsonl` either remains consistent
   with the prior bash format (if a reader still depends on it) or
   the reader is migrated to the kernel ledger and the cross-project
   file is dropped.

## Sequencing vs. host-shims-via-nix

The two specs touch overlapping surfaces (shims + hooks) but the
work is independent:

- The kernel PR (A) does not depend on shim delivery; it edits
  Go source and Go tests.
- The per-project swap (C) depends on the kernel PR but not on
  shim delivery.
- The respawn-pane template in delta 2 points at
  `/usr/local/bin/spore-coordinator-launch`, which is exactly the
  path `host-shims-via-nix` is migrating. As long as both specs
  agree to keep that path stable (symlink farm into the nix store
  is fine), neither blocks the other.

Pick whichever is cheapest to land first; the other slots in behind.

## Cross-references

- `internal/fleet/coordinator.go:239` (`ReapCoordinator`,
  current `kill-session`).
- `cmd/spore/coordinator_cmd.go:385` (`runCoordinatorTokenMonitor`,
  no env reads today).
- `cmd/spore/worker_cmd.go:46` (`runWorkerTokenMonitor`, env
  tunables to mirror).
- `.claude/settings.json` (canonical kernel-hook layout).
- Stale assumption: the bash shim's "workers must exit 0 because
  `claude -p` hangs on injected stderr turns" comment is outdated;
  `spore-worker-brief` now spawns interactive claude (commit
  `fix(handover): spore-worker-brief uses interactive claude, drops
  -p`), so the kernel `exit 2` shape is safe.
- Related todo: `docs/todo/host-shims-via-nix.md` (overlapping shim
  delivery surface; sequenceable independently).
