**Status**: prereqs landed, phase 1 next

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

## Prereqs (BOTH LANDED)

### 1. worker-brief retry-with-backoff in source (LANDED)

Two coordinator smoke-tests on 2026-05-25 (marketer
`/tmp/marketer-findings.md`, crm-gateway `/tmp/crm-gateway-findings.md`)
hit the same race: tmux session spawns before the binary finishes
copying `tasks/<slug>.md` into the worker's worktree, so the
`[[ ! -f "$brief" ]]` guard at
`bootstrap/handover/spore-worker-brief.sh:24` fires and the worker
drops to bare interactive claude with no brief.

Fix landed on `main` via PR #19 (commit `0a6ce2c`): up to ~3s of
0.5s backoff before falling through to the no-brief path. Slug
missing remains a fast-fail (misconfig, not race). The binary's
spawn-order fix is still backlog (order `tasks/<slug>.md` write
before the tmux spawn in `internal/fleet/lifecycle.go`).

### 2. PATH-prepend drift reconciliation (LANDED)

The on-disk hand-edit
`export PATH="/run/current-system/sw/bin:${PATH}"` was reconciled
into source on `main` via PR #20 (commit `6f4bed7`). Folded in
rather than deleted: the two shims call `claude` (and `codex`) by
bare name, so the prepend protects the nix-managed binary against
any future `/usr/local/bin/` shadow. Operator-validated rationale;
once shims ship via nix and become immutable on disk, we lose the
ability to patch this from the operator side.

## Delivery channels: what's actually in place

The spec earlier assumed `bootstrap/flake/configuration.nix` already
referenced `inputs.spore.packages.${system}.spore`. It does not.
The bundled flake (`bootstrap/flake/flake.nix`) only takes nixpkgs
and disko as inputs; it provisions a base NixOS that
`nixos-anywhere` installs onto a fresh box. The spore binary and
shims arrive AFTER that, via `Handoff()`'s scp+install in
`internal/infect/infect.go`.

The hosts that DO consume spore via nix today are existing infected
hosts whose OPERATOR-managed `/etc/nixos/configuration.nix` (not in
this repo, root-owned, not visible to coordinators on those hosts)
references `inputs.spore.packages.${system}.spore`. Refreshing those
hosts is the path that exists today:

```
cd /etc/nixos
sudo nix flake update spore
sudo nixos-rebuild switch --flake .#spore-bootstrap
```

That refresh today only swaps the spore binary, because the spore
flake only exposes the binary. Shims stay frozen at infect-time
content on `/usr/local/bin/spore-*`.

## Plan

### Phase 1: existing hosts (no bundled-flake changes)

#### 1.1. Expose shims as a flake output

Add `packages.${system}.shims` to root `flake.nix` (sibling of
`spore`). The derivation installs every file under
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
still needs the embed for the existing scp+install path in
`Handoff()` and for any non-NixOS deploys).

#### 1.2. Document the per-host `/etc/nixos/` snippet

The operator-managed per-host nix config needs:

- The shims package added to `environment.systemPackages` (or a
  `services.spore-fleet.shimsPackage` option if we promote it to
  the `nixosModules.spore-fleet` surface).
- A `system.activationScripts.spore-shims` block that symlinks
  `/usr/local/bin/spore-{attach,coordinator-launch,worker-brief,
  fleet-tick,greet-coordinator,greet-worker}` -> the corresponding
  paths in the shims derivation. Idempotent: remove any non-symlink
  at the target (sweeps stale `install`-mode shims left over from
  infect), then create the symlink.
- The hooks under `share/spore/hooks/` and the settings.json have
  per-USER install paths today (`/home/spore/.claude/`); leave
  those alone in Phase 1. They are not the load-bearing surface
  for the cap-respawn flow.

Add the snippet to `docs/operations/host-nix-snippet.md` (new file)
and cross-reference from `bootstrap/flake/README.md` so an operator
upgrading an existing host can paste it in.

#### 1.3. Refresh existing hosts

After 1.1 + 1.2 ship in a release:

- Operator pastes the snippet into `/etc/nixos/configuration.nix`.
- `sudo nix flake update spore && sudo nixos-rebuild switch`.
- The activation script replaces `/usr/local/bin/spore-*` with
  symlinks into the nix store. Any hand-edits on disk are lost
  (which is the point).

### Phase 2: bundled flake + fresh infects (deferred)

Two open design questions, both unblocked once Phase 1 is in place:

A. **Bundled flake referencing spore.** The bundled flake at
   `bootstrap/flake/` would need spore as an input to install
   shims at `nixos-anywhere` time. Options:
   - `path:..` (relative). Breaks once `Stage()` copies the flake
     to `/tmp/<staging-dir>` for scp.
   - `github:marketertechnologies/spore/<rev>`. Pinned; bumping
     requires updating `bootstrap/flake/flake.lock` per release.
   - `Stage()` rewrites the bundled flake's `flake.lock` at infect
     time to pin spore to the running CLI's commit. Aligns "local
     spore -> infected host" but bigger code.

B. **Drop scp install from `Handoff()`.** Becomes possible once
   the bundled flake reliably delivers shims. Keep the spore
   binary scp+install: developers running infect from a
   local-ahead checkout still want their local binary, not the
   pinned one.

Acceptance criteria #1, #2, and #5 (below) are achievable in
Phase 1 alone. Criterion #4 (`spore infect` without the dropped
scp step) is Phase 2's.

## Acceptance

1. (Phase 1) Editing `bootstrap/handover/spore-worker-brief.sh`,
   tagging a release, then on a host running `sudo nix flake update
   spore && sudo nixos-rebuild switch` picks up the new shim with
   no manual `scp` or `install`.
2. (Phase 1) `readlink -f /usr/local/bin/spore-worker-brief`
   resolves into the nix store.
3. (DONE) The PATH-prepend hand-edit on this host's
   `spore-coordinator-launch` and `spore-worker-brief` is reconciled
   in source (PR #20).
4. (Phase 2) `spore infect` on a fresh box produces a working
   coordinator and worker fleet without the dropped scp step.
5. (every phase) `go test ./internal/infect/...` and
   `go test ./internal/fleet/...` stay green; `just check` passes.

## Cross-references

- `embed.go:53` (`BundledHandover`).
- `internal/infect/infect.go:321-490` (`Handoff`, `StageHandover`,
  install-shim commands).
- `internal/infect/infect_test.go:149-159` (staged-handover fixture).
- `bootstrap/flake/configuration.nix` (bundled flake's OS config;
  does NOT reference the parent spore flake today).
- `bootstrap/flake/flake.nix` (bundled flake; only nixpkgs + disko
  inputs today, no spore self-reference).
- `bootstrap/handover/spore-worker-brief.sh` (now retries with
  backoff; PR #19).
- `bootstrap/handover/spore-coordinator-launch.sh` (PATH-prepend
  folded into source; PR #20).
- 2026-05-25 smoke reports: `/tmp/marketer-findings.md`,
  `/tmp/crm-gateway-findings.md`.
- Related todo: `docs/todo/retire-bash-token-monitor.md` (also touches
  the shim + hook surface but sequenceable independently).
