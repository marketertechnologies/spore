**Status**: step 1 landed; template already covered; live-file + per-project cleanup pending

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

### Step 1: silent no-op for `spore hooks watch-inbox` (LANDED)

PR #28 (`6c4bd3d`, merged `5bb97f9`). When both the slug positional
arg is absent and `$SPORE_TASK_INBOX` is empty, the command now
returns 0 silently instead of writing
`SPORE_TASK_INBOX is required when slug is omitted` to stderr.
Other branches unchanged. `TestHooksWatchInboxNoArgsNoEnvSilentNoOp`
in `cmd/spore/hooks_cmd_test.go` asserts exit 0 and empty stderr.

### Step 2: Stop hooks in the nix-shipped template

`bootstrap/handover/settings.json` has carried the Stop block since
commit `2d01536` ("spore-stop-hooks-claude", 2026-05-05) but every
`command` field used `/usr/local/bin/spore`. That path predates the
host-shims-via-nix phase 2 work: the canonical binary on a
phase-2-infected host is `/run/current-system/sw/bin/spore` (from
`inputs.spore.packages.${system}.spore`), and `/usr/local/bin/spore`
is either a leftover shadow (now renamed `spore-lattest-hard-linked`
on this host) or absent on a fresh infect. The shipped Stop hooks
would silently no-op (binary not found) on every freshly-infected
host.

Fixed alongside the spec correction: all five `command` entries in
`bootstrap/handover/settings.json` (Notification, Stop x4) point at
`/run/current-system/sw/bin/spore`. The two perl hook paths stay as
they are. It flows to `$out/share/spore/settings.json` via the
`shims` derivation.

Caveat: today the template only reaches a host once, during
`spore infect` (`internal/infect/infect.go:557` does
`install -m 0644 .../settings.json /home/spore/.claude/settings.json`).
There is no refresh path. `docs/host-nix-snippet.md` explicitly
flags this: "per-user hooks at `/home/spore/.claude/hooks/` and
`/home/spore/.claude/settings.json` still come from the one-shot
infect install. Not load-bearing for the cap-respawn flow; revisit
if it becomes important." Making host-level settings refresh under
`nixos-rebuild switch` -- the way the shims now do -- is a separate
spec, tracked as the follow-up below.

### Step 3: land the Stop block in the live `~/.claude/settings.json`

The live host file is missing the Stop block (this host was infected
before the template was updated, or the file was hand-edited; either
way it has drifted from the current template). Edit
`/home/spore/.claude/settings.json` and append a `Stop` entry to
`hooks` with the four commands:

```json
"Stop": [
  {
    "matcher": "",
    "hooks": [
      { "type": "command", "command": "/run/current-system/sw/bin/spore coordinator token-monitor", "timeout": 10 },
      { "type": "command", "command": "/run/current-system/sw/bin/spore worker token-monitor",      "timeout": 10 },
      { "type": "command", "command": "/run/current-system/sw/bin/spore fleet replenish-hook",      "timeout": 30 },
      { "type": "command", "command": "/run/current-system/sw/bin/spore hooks watch-inbox",         "timeout": 604800, "asyncRewake": true }
    ]
  }
]
```

Preserve the operator's existing customizations in the same file
(`statusLine`, `theme`, `verbose`, `autoCompactEnabled`, the bare
`spore hooks notify-coordinator` command without `/usr/local/bin/`).
This is outside the spore tree -- the coordinator prepares the
diff, the operator applies it.

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

1. Step 1 -- DONE. Operator rebuilds the kernel on this host so the
   live `/run/current-system/sw/bin/spore` carries the watch-inbox
   short-circuit before step 3 fires the hook into non-spore sessions.
2. Step 2 -- nothing to land in spore.
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

## Open questions / follow-ups

- **Refresh `~/.claude/settings.json` under `nixos-rebuild switch`.**
  Today the file is install-once at infect time. The same logic
  that made `/run/current-system/sw/bin/spore-*` shims refreshable in
  host-shims-via-nix phase 2 could be applied here: have the
  bundled flake activation script (or a sibling) lay down
  settings.json + hooks symlinked into the `shims` derivation,
  with operator customizations honored via a separate overlay
  file. Out of scope for this spec; should land as
  host-settings-via-nix.
- Should `spore install` (currently drops skills/) also be the
  command that creates `~/.config/spore/<name>/` and chmods it 700?
  Today the operator does this by hand. Cosmetic.
- Once host-level Stop hooks land, the only remaining reason for
  marketer/crm-gateway's `.claude/settings.json` is the Rails-specific
  SessionStart + PreToolUse blocks. Whether those should also move
  somewhere centralized (a `spore.toml` per-project hooks block
  consumed by `spore compose`, or similar) is a follow-up.
