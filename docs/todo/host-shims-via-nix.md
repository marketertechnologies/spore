**Status**: not-started

# host shims: deliver bootstrap/handover via the spore nix derivation

## Problem

`bootstrap/handover/*.sh` ships through two coupled mechanisms today:

1. **Embedded into the binary** via `//go:embed all:bootstrap/handover`
   (`embed.go:53`, `BundledHandover`).
2. **Dropped onto the box once** by `spore infect`
   (`internal/infect/infect.go:374-470`): the embed is staged to
   `/tmp/spore-handover`, scp'd, then `install -m 0755 ...` into
   `/usr/local/bin/spore-{attach,coordinator-launch,worker-brief,
   fleet-tick,greet-coordinator,greet-worker}` and
   `/home/spore/.claude/hooks/`.

There is no refresh path. Once a host is infected, a binary upgrade
via `nix flake update spore && nixos-rebuild switch` picks up the new
`spore` (because nixpkgs builds it from the flake input) but the shims
on disk stay frozen at infect time. The current production state on
this host shows the symptom: two shims (`spore-coordinator-launch`,
`spore-worker-brief`) carry a hand-edit (`export
PATH="/run/current-system/sw/bin:${PATH}"`) that does not exist in
source. The operator added it because `which spore` was finding a
shadow binary; the fix never made it back into the tree, and nothing
will refresh the shim even if it did.

Goals this addresses:

- One delivery channel for binary + shims, refreshed by the same
  `nixos-rebuild switch` the operator already runs.
- No hidden on-disk drift from canonical source.
- A natural place to land the worker-brief retry-with-backoff fix
  (see prereq 1 below) and have every host pick it up automatically.

## Prereqs

### 1. worker-brief retry-with-backoff in source

Two coordinator smoke-tests on 2026-05-25 (marketer
`/tmp/marketer-findings.md`, crm-gateway `/tmp/crm-gateway-findings.md`)
hit the same race: tmux session spawns before the binary finishes
copying `tasks/<slug>.md` into the worker's worktree, so the
`[[ ! -f "$brief" ]]` guard at
`bootstrap/handover/spore-worker-brief.sh:24` fires and the worker
drops to bare interactive claude with no brief.

Both reports confirm the shim file is byte-identical to its
`bak-pre-sync` backup; the race is in the binary's spawn ordering
(likely `internal/fleet/lifecycle.go`).

We do not want to bake a known-fragile shim into a less-mutable
delivery channel. Land the cheapest fix first in source:

```sh
# replace the bare guard with up to ~3s of backoff
for _ in 1 2 3 4 5 6; do
  [[ -n "$slug" && -f "$brief" ]] && break
  sleep 0.5
done
if [[ -z "$slug" || ! -f "$brief" ]]; then
  echo "spore-worker-brief: ..." >&2
  exec "$agent" --dangerously-skip-permissions
fi
```

The binary's spawn-order fix can chase as a separate change.

### 2. PATH-prepend drift reconciliation

The on-disk hand-edit
`export PATH="/run/current-system/sw/bin:${PATH}"` in
`spore-coordinator-launch` and `spore-worker-brief` must fold into
source before the nix migration, or `nixos-rebuild switch` will
silently revert it.

Audit: confirm the prepend is still needed once the host has the nix
spore on `/run/current-system/sw/bin/spore` and the `/usr/local/bin/`
shadows are renamed (this host's `spore-lattest-hard-linked` rename
already cleared the original cause). If the prepend is obsolete,
delete the on-disk edit; if still needed, add it to the source files.

## Plan

### 1. Expose shims as a flake output

The spore flake (`flake.nix` at repo root) currently exposes
`packages.${system}.spore`. Add a sibling `packages.${system}.shims`
(or fold into the `spore` derivation as a separate `out` /
`postInstall` step) that installs every file under
`bootstrap/handover/` to a deterministic prefix:

- `bin/spore-attach`
- `bin/spore-coordinator-launch`
- `bin/spore-worker-brief`
- `bin/spore-fleet-tick`
- `bin/spore-greet-coordinator`
- `bin/spore-greet-worker`
- `share/spore/hooks/block-bg-bash.pl`
- `share/spore/hooks/load-state-md.pl`
- `share/spore/settings.json`
- `share/spore/systemd/spore-fleet-reconcile.service`
- `share/spore/systemd/spore-fleet-reconcile.timer`

Keep the existing `//go:embed all:bootstrap/handover` (the binary
still needs the embed for non-NixOS deploys, even if there are none
today; cheap insurance).

### 2. Reference the shims in the bundled flake

`bootstrap/flake/configuration.nix` already adds
`inputs.spore.packages.${system}.spore` to
`environment.systemPackages`. Add the shims derivation alongside, so
`nixos-rebuild switch` writes them under
`/run/current-system/sw/bin/spore-*`.

The hooks and systemd unit templates should be referenced from
`share/spore/` rather than copied; the systemd units can render at
build time with substituted paths if needed.

### 3. Drop the scp install from `spore infect`

`internal/infect/infect.go:374-470` stages `bootstrap/handover` into
`/tmp/spore-handover` and runs `install -m 0755 ...` for each shim.
Once the bundled flake installs the same files via nix:

- Delete the scp + install commands from `Handoff()`.
- Keep `SPORE_COORDINATOR_AGENT=/usr/local/bin/spore-coordinator-launch`
  and `SPORE_AGENT_BINARY=/usr/local/bin/spore-worker-brief` env
  references, but point them at `/run/current-system/sw/bin/...` (or
  add a stable `/usr/local/bin/` symlink farm if downstream relies on
  the path).
- Update `internal/infect/infect_test.go` cases that today seed the
  staged-handover fileset.

Decision point: the simplest path is to keep `/usr/local/bin/spore-*`
as the canonical shim path (so the env-var contract with `spore.toml`
and `lifecycle.go` stays stable), but back each entry with a symlink
into the nix store managed by the bundled module. That avoids a
breaking change for hosts already infected before this migration.

### 4. Refresh existing hosts

For hosts already infected:

- Operator runs `cd /etc/nixos && sudo nix flake update spore && sudo
  nixos-rebuild switch --flake .#spore-bootstrap`.
- The bundled module overwrites `/usr/local/bin/spore-*` (or the
  symlink targets) atomically. The previous on-disk edits are lost,
  which is the explicit goal.

Document the upgrade command in `bootstrap/flake/README.md` (or
wherever the flake's operator-facing notes live).

## Acceptance

1. Editing `bootstrap/handover/spore-worker-brief.sh`, tagging a
   release, then on a host running `sudo nix flake update spore &&
   sudo nixos-rebuild switch` picks up the new shim with no manual
   `scp` or `install`.
2. `readlink -f /usr/local/bin/spore-worker-brief` resolves into the
   nix store (or `/run/current-system/sw/bin/`).
3. The PATH-prepend hand-edit on this host's
   `spore-coordinator-launch` and `spore-worker-brief` is reconciled
   (either folded into source or proven obsolete and removed).
4. `spore infect` on a fresh box still produces a working coordinator
   and worker fleet without the dropped scp step.
5. `go test ./internal/infect/...` and `go test ./internal/fleet/...`
   stay green; `just check` passes.

## Cross-references

- `embed.go:53` (`BundledHandover`).
- `internal/infect/infect.go:321-490` (`Handoff`, `StageHandover`,
  install-shim commands).
- `internal/infect/infect_test.go:149-159` (staged-handover fixture).
- `bootstrap/flake/configuration.nix` (where the bundled module
  references `inputs.spore.packages.${system}.spore` today).
- `bootstrap/handover/spore-worker-brief.sh:24` (race-vulnerable
  guard; prereq 1).
- `bootstrap/handover/spore-coordinator-launch.sh` (PATH-prepend
  drift target; prereq 2).
- 2026-05-25 smoke reports: `/tmp/marketer-findings.md`,
  `/tmp/crm-gateway-findings.md`.
- Related todo: `docs/todo/retire-bash-token-monitor.md` (also touches
  the shim + hook surface but sequenceable independently).
