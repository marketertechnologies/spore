**Status**: done

# host shims: deliver bootstrap/handover via the spore nix derivation

## Problem (historical)

`bootstrap/handover/*.sh` shipped through two coupled mechanisms:

1. **Embedded into the binary** via `//go:embed all:bootstrap/handover`
   (`embed.go:53`, `BundledHandover`).
2. **Dropped onto the box once** by `spore infect`: the embed was
   staged to `/tmp/spore-handover`, scp'd, then `install -m 0755 ...`
   into `/usr/local/bin/spore-{attach,coordinator-launch,worker-brief,
   fleet-tick,greet-coordinator,greet-worker}`.

There was no refresh path. Once a host was infected, a binary upgrade
via `nix flake update spore && nixos-rebuild switch` picked up the new
`spore` (because nixpkgs built it from the flake input) but the shims
on disk stayed frozen at infect time. The motivating symptom on this
host: two shims (`spore-coordinator-launch`, `spore-worker-brief`)
carried a hand-edit (`export PATH="/run/current-system/sw/bin:${PATH}"`)
that did not exist in source. The operator added it because `which
spore` was finding a shadow binary; the fix never made it back into
the tree, and nothing would refresh the shim even if it had.

Goals: one delivery channel for binary + shims, refreshed by the
same `nixos-rebuild switch` the operator already runs; no hidden
on-disk drift from canonical source; a natural place to land the
worker-brief retry-with-backoff fix and have every host pick it up.

## What landed

### Prereqs (PR #19, #20)

- **`0a6ce2c`** (PR #19): `spore-worker-brief.sh` retries with backoff
  (~3s of 0.5s backoff) before falling through to the no-brief path.
  Reconciles the tmux-before-task-file race surfaced by the 2026-05-25
  smoke tests.
- **`6f4bed7`** (PR #20): `export PATH="/run/current-system/sw/bin:${PATH}"`
  folded into source for `spore-coordinator-launch.sh` and
  `spore-worker-brief.sh`. Protects against `/usr/local/bin/` shadows
  for the bare `claude` / `codex` calls in those shims.

### Phase 1: existing hosts (PR #22)

- **`68b42b5`** (PR #22): root `flake.nix` exposes
  `packages.${system}.shims` (six shims + `share/spore/hooks/`,
  `share/spore/settings.json`, `share/spore/systemd/`).
  `checks.shims-layout` asserts paths exist and are executable.
- `docs/host-nix-snippet.md` documents the operator-facing snippet
  for the per-host `/etc/nixos/configuration.nix`. The activation
  script symlinks `/usr/local/bin/spore-*` into the shims derivation
  and sweeps any non-symlink target.
- The existing `//go:embed all:bootstrap/handover` is retained: the
  embed still drives non-NixOS deploys and the staged-flake activation
  path on fresh infects.

### Phase 1.5: nixos-module surface (PR #23)

- **`2e4ead8`** (PR #23): `services.spore-fleet.shimsPackage` option
  added, defaulting to `inputs.spore.packages.${system}.shims`. The
  module wires both `systemPackages` and
  `system.activationScripts.spore-shims` so importing
  `nixosModules.spore-fleet` (Option A in
  `docs/host-nix-snippet.md`) covers shims + reconciler in one
  block. The activation script is ungated from `gracefulDeploy`
  via `lib.mkMerge`. The existing nixos VM test asserts the
  symlinks land.

### Phase 2: bundled flake + fresh infects (PR #24)

Resolved the two open design questions in favor of:

- **Bundled flake references spore via `github:` + Stage()-time lock
  rewrite.** Aligns with the "origin is source of truth" host model:
  every host (including the freshly-infected one) references spore
  from origin and rolls forward via `nix flake update spore`.
- **Drop binary + shim scp/install from `Handoff()`.** The bundled
  flake delivers both. A push-first guard makes the lock-rewrite
  fail fast when local HEAD is not on origin.

Commits on the branch (in landing order):

- **`b56e02f`** (steps 2.2 + 2.3): bundled flake declares
  `inputs.spore.url = github:marketertechnologies/spore` and inlines a
  `spore-shims` activation script in
  `bootstrap/flake/configuration.nix`. `Stage()` rewrites the staged
  `flake.lock` at infect time (`PinBundledSpore`) to pin `spore` to
  the local CLI's `BuildCommit` (stripped of `-dirty`). New
  `infect.Config.SporeCommit` field; `cmd/spore/main.go` populates it
  from `spore.BuildCommit()`. Inlined the activation rather than
  importing `nixosModules.spore-fleet` because the bundled flake has
  no projects configured at infect time and the module's
  empty-projects assertion would trip.
- **`cb83bfc`** (step 2.4): `RequireSporeCommitOnOrigin` HEADs
  `https://api.github.com/repos/marketertechnologies/spore/commits/<sha>`
  before `Stage()` and errors with a `git push` hint when the commit
  is not on origin. Skipped when `--flake` is set or `SporeCommit` is
  empty. Test-mockable via package vars `SporeOriginCommitsURL` and
  `SporeOriginHTTPClient`.
- **`663bb03`** (step 2.5): `Handoff()` and `InstallHandoverScript()`
  no longer scp+install the spore binary or the six shims.
  `TestRunWithRepoRunsHandoff` rewritten for the new 6-call shape
  with negative assertions on the source paths of the removed
  install commands so the contract does not silently regress.

Kept (still per-user, not yet covered by bundled flake): hooks under
`/home/spore/.claude/hooks/`, `settings.json`, per-user systemd
units, `/etc/spore/coordinator.env`, `/home/spore/.bashrc`, the
rsync of the repo, lingering enable, first reconcile, and the
spore-coordinator service/timer restart.

## Acceptance (verified)

1. (Phase 1) Editing `bootstrap/handover/spore-worker-brief.sh`,
   tagging a release, then on a host running `sudo nix flake update
   spore && sudo nixos-rebuild switch` picks up the new shim with
   no manual `scp` or `install`. DONE via PR #22 + #23.
2. (Phase 1) `readlink -f /usr/local/bin/spore-worker-brief` resolves
   into the nix store. DONE: activation script symlinks each shim.
3. The PATH-prepend hand-edit reconciled in source. DONE via PR #20.
4. (Phase 2) `spore infect` on a fresh box produces a working
   coordinator and worker fleet without the dropped scp step. DONE
   via PR #24: bundled flake's `inputs.spore` + activation owns
   binary + shims on first boot.
5. (every phase) `go test ./internal/infect/...` and
   `go test ./internal/fleet/...` stay green; `just check` passes.
   DONE on each merge.

## Follow-ups (not on this spec)

- **`spore-worker-brief` spawn race in the binary.** Order
  `tasks/<slug>.md` write before tmux spawn in
  `internal/fleet/lifecycle.go`. The shim retry now in place is the
  belt; this is the suspenders.
- **Bundled flake also delivers per-user hooks / settings / systemd
  units.** Still scp'd via `Handoff()`. Not load-bearing for
  cap-respawn; fold into a later phase if it becomes important.

## Cross-references

- `embed.go:53` (`BundledHandover`).
- `internal/infect/infect.go` (`Handoff`, `Stage`,
  `PinBundledSpore`, `RequireSporeCommitOnOrigin`).
- `bootstrap/flake/flake.nix` (`inputs.spore` declaration).
- `bootstrap/flake/configuration.nix`
  (`system.activationScripts.spore-shims`).
- `docs/host-nix-snippet.md` (operator-facing per-host snippet).
- `bootstrap/handover/spore-worker-brief.sh` (retries with backoff).
- `bootstrap/handover/spore-coordinator-launch.sh` (PATH-prepend in
  source).
- 2026-05-25 smoke reports: `/tmp/marketer-findings.md`,
  `/tmp/crm-gateway-findings.md`.
