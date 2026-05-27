**Status**: drafting

# centralize claude-code Stop hooks at host level

## Problem

Onboarding a new repo to spore management still requires a per-project
`.claude/settings.json` purely to wire four Stop hooks:

```
spore coordinator token-monitor
spore worker token-monitor
spore fleet replenish-hook
spore hooks watch-inbox
```

Two existing projects on this host carry this file (marketer,
crm-gateway). A third (crm-gateway-ruby-client) does not, and its
coordinator runs without context-cap respawn, fleet replenish, or
inbox-driven wakeups. The operator expects to add many more repos
and wants every coordinator to behave alike.

The other hook categories already centralized:

- `Notification` -> `spore hooks notify-coordinator`
- `PreToolUse` -> `block-bg-bash.pl`
- `SessionStart` -> `load-state-md.pl`

All three live in host-level `~/.claude/settings.json` and the
nix-shipped template at `bootstrap/handover/settings.json` (-> `$out/share/spore/settings.json` via the `shims` derivation).
Stop hooks are the remaining gap.

## Why this is the right phase

The four kernel commands in question are already designed to be
host-safe. Each self-gates on `$SPORE_TASK_INBOX` and the coordinator
state dir prefix:

- `spore coordinator token-monitor`: `Config.IsCoordinator()` returns
  false on empty `$SPORE_TASK_INBOX` or an inbox outside the
  coordinator state root; `Check` returns `Level: "skip"` (exit 0).
  See `internal/coordinator/tokenmonitor/tokenmonitor.go:88-94` and
  `:102-104`.
- `spore worker token-monitor`: same shape. Documented in
  `cmd/spore/worker_cmd.go:24-25` ("Skips coordinator inboxes and
  sessions with no `$SPORE_TASK_INBOX`").
- `spore fleet replenish-hook`: explicit `isCoordinatorSession()`
  no-op at `cmd/spore/fleet_cmd.go:241-243`. Swallows stdin, never
  returns non-zero.

The one exception: `spore hooks watch-inbox` errors on stderr when
both the slug arg is absent AND `$SPORE_TASK_INBOX` is empty
(`cmd/spore/hooks_cmd.go:173`). At host level this would emit
"SPORE_TASK_INBOX is required when slug is omitted" on every Stop
event in non-spore sessions. That's the one piece of kernel work
this spec covers.

## Goals

1. New repo onboarding shrinks to: create `~/.config/spore/<name>/`,
   run `spore bootstrap`, run `spore coordinator start`. No in-repo
   harness files needed for parity with marketer/crm-gateway on the
   Stop-hook axis.
2. crm-gateway-ruby-client (and any future light-adoption repo)
   picks up token-monitor / replenish / watch-inbox automatically.
3. Existing per-project Stop hooks in marketer and crm-gateway
   removed once host-level coverage is verified, to avoid each
   hook firing twice (claude-code merges, does not dedupe).
4. Freshly-infected hosts get the same coverage out of the box via
   the nix flake's `share/spore/settings.json`.

## Non-goals

- Migrating marketer/crm-gateway's project-specific SessionStart
  (`bin/spore-worker-init`) or PreToolUse (`bin/spore-task-done-gate`)
  hooks. Those bring up per-slug Postgres/RabbitMQ/Redis and run
  `bin/ci-local` respectively. They are Rails-specific and stay
  per-project.
- Building a `spore install --hooks` mechanism that writes a
  per-project settings.json. The point is to NOT need per-project
  settings.json for the Stop hooks at all.
- Touching the kernel's hook-gating logic beyond the one
  watch-inbox short-circuit. The other three commands are already
  correct.

## Plan

### Step 1: silent no-op for `spore hooks watch-inbox`

When both the slug positional arg is absent and `$SPORE_TASK_INBOX`
is empty, exit 0 silently instead of writing the current error to
stderr and returning non-zero. Behavior in all other branches
unchanged (an explicit slug, or `$SPORE_TASK_INBOX` set, still
runs the watcher).

Site: `cmd/spore/hooks_cmd.go` near line 173. Wrap the existing
`SPORE_TASK_INBOX is required` block in an "if we're being invoked
as a Stop hook with no spore context, just return 0" guard.

Tests: extend `cmd/spore/hooks_cmd_test.go` (or the package-level
test in `internal/hooks/watchinbox_test.go`, whichever already
covers the env-driven path) with one case asserting exit 0 and
empty stderr when both slug and env are absent.

One commit, one PR.

### Step 2: add Stop hooks to the nix-shipped template

Edit `bootstrap/handover/settings.json` to add a `Stop` hooks block
mirroring the per-project block in marketer/crm-gateway, using
`/usr/local/bin/spore` paths (matching the existing entries in the
same file):

```json
"Stop": [
  {
    "matcher": "",
    "hooks": [
      { "type": "command", "command": "/usr/local/bin/spore coordinator token-monitor", "timeout": 10 },
      { "type": "command", "command": "/usr/local/bin/spore worker token-monitor",      "timeout": 10 },
      { "type": "command", "command": "/usr/local/bin/spore fleet replenish-hook",      "timeout": 30 },
      { "type": "command", "command": "/usr/local/bin/spore hooks watch-inbox",         "timeout": 604800, "asyncRewake": true }
    ]
  }
]
```

This file flows to `$out/share/spore/settings.json` via the `shims`
derivation in `flake.nix`. New infected hosts pick it up the next
time `docs/host-nix-snippet.md`'s install path is followed; existing
hosts pick it up via the next `nix flake update spore &&
nixos-rebuild switch`.

### Step 3: land the same block in the live `~/.claude/settings.json`

Step 2 is the shipped template. Step 3 is the live file the
operator's current claude sessions actually read on this specific
host. Edit `/home/spore/.claude/settings.json` to add the same
Stop block.

This is outside the spore tree -- operator territory -- but called
out here so the rollout sequence is on paper. The diff is small;
the coordinator can prepare it and the operator applies it.

### Step 4: remove redundant per-project Stop hooks

In `/home/spore/marketer/.claude/settings.json` and
`/home/spore/crm-gateway/.claude/settings.json`, drop the four
Stop-hook entries. Keep the SessionStart and PreToolUse blocks
(those wire `bin/spore-worker-init` and `bin/spore-task-done-gate`,
which are project-specific and stay).

If both host and project have Stop hooks, claude-code merges them
and each kernel command fires twice per Stop event. Not incorrect
(each is idempotent and self-gating) but wasteful and confusing in
logs.

Step 4 lands in the marketer and crm-gateway repos respectively,
not in spore. Coordinator can prepare the diffs; operator pushes
each repo's branch.

## Rollout order

Steps land in order; each gates the next.

1. Step 1 merges to spore main. Operator pulls + rebuilds the kernel
   on this host so the live `/run/current-system/sw/bin/spore`
   carries the watch-inbox short-circuit.
2. Step 2 merges to spore main. (Can ride in the same PR as step 1
   if scope stays small.)
3. Step 3: operator edits `~/.claude/settings.json` on this host.
   Verify in a fresh claude session: trigger a Stop event in a
   non-spore directory and confirm zero stderr noise; trigger a
   Stop in a coordinator pane and confirm `token-monitor` fires
   normally.
4. Step 4: drop per-project Stop hooks from marketer and
   crm-gateway. Verify each coordinator still respawns at cap, the
   fleet still replenishes, and watch-inbox still wakes on inbox
   writes.

## Verification

Per-step:

- **Step 1**: `go test ./cmd/spore/... ./internal/hooks/...` green,
  plus a manual smoke from a non-spore cwd: `echo '{}' | spore hooks
  watch-inbox` returns 0 with empty stderr.
- **Step 2**: `just nix-check` green. `nix build .#shims` produces
  a derivation containing the updated `share/spore/settings.json`;
  inspect with `cat result/share/spore/settings.json`.
- **Step 3**: in a claude session at `/home/spore` (non-spore cwd),
  end a turn and confirm no stderr noise from the Stop hooks. In a
  coordinator pane near soft cap, end a turn and confirm the
  respawn message fires.
- **Step 4**: after the marketer settings change, end a coordinator
  turn near cap and confirm a single (not duplicated) wrap-up
  message. Same for crm-gateway.

## Open questions

- Should `spore install` (currently drops skills/) also be the
  command that creates `~/.config/spore/<name>/` and chmods it 700?
  Today the operator does this by hand. Cosmetic; outside this
  spec.
- Once host-level Stop hooks land, the only remaining reason for
  marketer/crm-gateway's `.claude/settings.json` is the Rails-specific
  SessionStart + PreToolUse blocks. Whether those should also move
  somewhere centralized (a `spore.toml` per-project hooks block
  consumed by `spore compose`, or similar) is a follow-up.
