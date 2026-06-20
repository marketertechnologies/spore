**Status**: in progress - kernel adopted. RESUME POINT below.
**Priority**: high

## RESUME POINT (read this first on a fresh session)

State lives on disk + the ROC board, not in any session's context. To
continue: read this doc, run `git log --oneline spore-upgrade-0.9.2`, and
read the ROC board (team ROC, newbuilds; token at `~/.config/spore/secrets.env`).
Memory files `three-spore-repos` + `rocky-linear-access` load every session.

- Integration branch: `spore-upgrade-0.9.2` (umbrella PR #43 -> main). Feature
  branches target it; merge each in as it goes green (operator does not review
  PRs - trust lint + `nix flake check`). `evolved` git remote -> ~/projects/spore.
- DONE + merged: ROC-2 (sandbox, #44), ROC-3/4/6/8/9 (evolved kernel adopted,
  #45), ROC-7 (matter delegates to rocky, not assign + ROC config in spore.toml,
  #46 - verified live against ROC), ROC-14 (mcom tier-1 wisdom: supervisor loop +
  token-cap rotation, 0/1/64 exit codes, `spore guard`, `spore statusline`, tmux
  config, #47), ROC-5 (single-coordinator collapse: NixOS module reverted to
  evolved single-`projectRoot` shape + fork extras re-applied; orphan
  spore-projects.nix deleted; VM test retargeted, #48). The fork's kernel IS
  evolved spore 0.9.2. E2E green: `nix flake check` runs the NixOS fleet VM test.
- ROC-5 scope note: the only real fork divergence was nixosModules/
  spore-fleet.nix (the `projects.<name>` attr). internal/fleet's
  `~/.config/wt/projects` list (liveness/reap/wake) and the spore-fleet-tick
  `$HOME` walk are UPSTREAM (identical in evolved), not divergences - left
  alone. internal/infect was already single-project and never generated
  spore-projects.nix. Topology decision (operator): single coordinator per
  host; multiple repos served by an umbrella `projectRoot` with sub-repos as
  subdirs (proto-monorepo), NOT a fleet fanned across independent repos.
- NEXT: cross-repo worker ship design (new ROC ticket). A worker is 1
  worktree of 1 git repo -> 1 wt/<slug> branch -> 1 PR; the whole ship cycle
  (wt-check gate, evidence contract, internal/merge, internal/gh) assumes one
  git history. A feature ticket spanning multiple repos needs a decision:
  true monorepo/subtree (spore-native, one PR covers all) vs submodules
  (per-repo PRs, ship cycle does NOT span repos today -> harness work or
  manual). Design before building.
- Then: ROC-10/11/12 (re-apply recipes/migrations/marketertechnologies-identity,
  set aside in #45), ROC-13 (shims reconcile - shims already present+building,
  likely just verify), ROC-15 (mcom tier-2: PreToolUse block suite + systemd
  restart-guard unit settings StartLimitBurst/RestartPreventExitStatus +
  spore-attach), ROC-16 (Hetzner, deferred).
- ROC-14 note: supervisor loop is opt-in (`[coordinator].supervise` /
  SPORE_COORDINATOR_SUPERVISE); off by default so the spawn settle-check stays
  sharp. `spore coordinator spawn` now returns 64 on unexpected session death;
  the fork's deployed model is still the watchdog timer (spore-fleet-tick), not
  the long-lived ExecStart - the exit-code contract just makes ExecStart viable.
- Known flake: internal/coordinator/spawn TestRunSignalShutdownKillsSession is
  tmux-timing-flaky under parallel `go test ./...`; passes in isolation and in
  the clean nix sandbox. Not a blocker; could harden later.
- Validate every commit: `nix develop -c go test ./...` + `spore lint` + the
  full `nix flake check`. Regenerate instruction files with
  `spore compose --consumer spore > CLAUDE.md` (and AGENTS.md) after rule edits.

# spore upgrade to 0.9.2 - distill evolved spore + mcom wisdom into this fork

## Goal

Turn this repo (`marketertechnologies/spore`, currently a stale 0.4.2 fork) into
a clean, standalone distillation of how spore is done in the mcom world: the
evolved spore kernel (`versality/spore` 0.9.2) as the new base, plus the
generalizable operational wisdom mcom built on top, minus mcom's product and
leak surface, plus this fork's own worthwhile features. End state: deployable to
Hetzner to autonomously serve `ROC` Linear tickets (newbuilds workspace). Actual deploy is deferred
(Phase 4).

## The three repos

- **this repo (fork)** = `marketertechnologies/spore`, v0.4.2. Old divergent
  fork. Built its own: multi-project fleet, `shims` flake pkg, infect
  commit-pinning, recipes library (Jira/GitHub/Sentry), migrations engine.
- **evolved** = `versality/spore`, v0.9.2 (checkout at `~/projects/spore`, wired
  as git remote `evolved` -> `evolved/main`). ~46k lines ahead on the kernel.
- **mcom** = `marketertechnologies/mcom` (`~/projects/mcom`), a downstream
  product consuming evolved spore + a heavy `harness/` + `nix/hosts/helm-coord/`
  layer. Source of distilled operational wisdom.

All three share the root commit `98115e7`, and fork + evolved share the same Go
module path (`github.com/versality/spore`), so evolved files can be pulled
subsystem-by-subsystem with **no import rewrites**.

## Decisions (locked)

- **Merge, not replace or hand-port.** Adopt evolved as kernel base; re-apply
  fork features; layer mcom wisdom.
- **Single-project.** Drop the fork's multi-project ("multiple coordinators")
  divergence; match evolved + mcom's single-project model. Stops carrying a
  divergence and keeps future evolved pulls cheap.
- **Keep** (fork features worth preserving): recipes library, migrations engine,
  `marketertechnologies` identity (infect `SporeOwner`, the remote), infect
  commit-pinning, the `shims` flake package.
- **Keep mcom-distilled wisdom** (generalizable only): tmux config, coordinator
  supervisor/restart contract, statusline token ramp, headless-claude guard,
  PreToolUse block suite (as `configs/claude` hooks).
- **Drop mcom leak/product surface**: marketer web app, Linear team `MCOM`,
  `marketer.dev`, real IPs, secrets, slack, the refinement/webhook daemons.

## Git workflow

- `spore-upgrade-0.9.2` = long-lived integration branch off `main`. Single
  umbrella PR `spore-upgrade-0.9.2 -> main`.
- One feature branch per work-unit, each targeting `spore-upgrade-0.9.2`.
- Merge integration into `main` when the whole upgrade is ready.

## Execution model - Linear-driven, then self-hosted

- **Linear is the driver.** Team `ROC` (newbuilds workspace). Each work-unit
  below becomes a `ROC` ticket; the ticket backlog is the source of truth, not
  this doc's lists. Needs a Linear API key for newbuilds wired into spore's
  `matter.linear` (operator-bound; not yet present in env).
- **Bootstrap by hand, then dogfood.** The fleet/matter/sandbox we would run is
  itself what Phase 1 fixes, so do Phase 1's foundation by hand (this session /
  operator-driven) to get a modern, reliable fleet first. THEN switch to "our
  own medicine": stand up the upgraded spore fleet on `ROC` and let its workers
  chew through the Phase 2/3 tickets, each targeting `spore-upgrade-0.9.2`.
- **Run the coordinator from a pinned spore binary, not the live-editing repo.**
  Workers rewrite this tree; the fleet that schedules them must not change under
  them. Avoids self-modification fragility.
- **Sequence by dependency.** Phase 2 depends on the Phase 1 base. Within a
  phase, hand the fleet only independent subsystems in parallel; the coordinator
  serializes the rest so parallel workers do not collide on the one branch.

## Merge mechanics

Per subsystem: `git checkout evolved/main -- <paths>` to pull evolved's version,
delete the fork's now-superseded files, fix integration seams, then
`spore lint && go test ./... && nix flake check` before commit. Small,
bisectable commits (blast-radius, not review). Keep-features get re-applied as
explicit follow-up commits where they touch files evolved also changed
(`flake.nix`, `internal/infect`, `nixosModules/spore-fleet.nix`).

## Phase 1 - adopt evolved kernel

Pull evolved wholesale where it is strictly ahead and the fork has no divergence
worth keeping. Suggested feature branches / commit units:

1. **Sandbox**: `cmd/spore-sandbox/`, `internal/sandboxcfg/`, `docs/sandbox.md`;
   wire into worker spawn (`internal/task/agent_command.go` `maybeSandboxWrap`),
   `[sandbox]` in spore.toml, `bubblewrap` in devShell.
2. **Hooks-as-code**: `configs/` tree (claude + codex hooks-config), evolved
   `internal/hooks/**` (inject, settings, codex, contexttee, stopwatchdog, ...),
   `spore hooks render` pipeline, `bootstrap/scripts/hooks-render.sh`.
3. **Lints**: evolved `internal/lints/**` (config.go + the ~21 portable lints +
   `tasks.go` helpers). Enable defaults; gate optional ones via spore.toml
   `[lint.*]`.
4. **Coordinator/fleet**: `internal/coordinator/{workerwatch,failuresummary,
   loopguard,tokenmonitor}`, `internal/evictor`, `internal/tmuxsess`,
   `internal/sessionkind`, `internal/agentpane`; evolved `internal/fleet/**`
   (single-project), hook pre-injection on spawn, `WT_SESSION_KIND`.
5. **Work-item layer**: evolved `internal/task/**` (string statuses, comments
   projection), `internal/matter/**` (Sync flips Ready->In Progress; no
   OnSpawn; `claim_label`), `internal/todo`, `internal/scout`,
   `internal/search`, `internal/transcript` (codex), `internal/secret`,
   `internal/gh`. Matters here for "serve Linear tickets."
6. **cmd surface**: evolved `cmd/spore/**` (todo, scout, search, secret, gh,
   merge, signal, init, sla-scan, evict-idle, etc.).
7. **Rules + instruction files**: evolved `rules/` (worker-autonomy/escalation,
   ask-via-tool, tier-policy, alignment-mode, expanded source-map), regenerate
   CLAUDE.md / AGENTS.md from fragments; refresh spore.toml `[lint]`/`[align]`.

## Phase 2 - re-apply fork features onto evolved base

1. **recipes**: `internal/recipes`, `bootstrap/recipes`, `spore recipes` cmd.
2. **migrations**: `internal/migrations`, `bootstrap/migrations`, `spore
   migrate` cmd.
3. **codexpolicy** (evaluate: keep or fold into evolved codex hooks).
4. **identity + infect**: re-apply `SporeOwner = marketertechnologies`,
   `SporeCommit` pinning, `PinBundledSpore`, `RequireSporeCommitOnOrigin` onto
   evolved `internal/infect`.
5. **shims**: re-apply the `shims` package + checks onto evolved `flake.nix`;
   wire into `bootstrap/flake/configuration.nix` activation.
   (Multi-project: intentionally NOT re-applied - dropped per decision.)

## Phase 3 - distill mcom operational wisdom

Generalizable only; translate names (helm->coordinator, rover->worker). Tiered:

- **Tier 1**: headless-claude guard (refuse no-TTY/no-tmux); statusline token
  ramp (`agent-statusline.sh` -> kernel); coordinator supervisor + exit-code
  respawn contract (0/1/64); tmux config (`TMUX_TMPDIR` socket pin,
  `detach-on-destroy off`, `mouse`).
- **Tier 2**: PreToolUse block suite (test-gate, secrets-inference,
  memory-writes, tasks-projection guard) as `configs/claude` hook entries;
  restart guards (`StartLimitBurst`, `RestartPreventExitStatus`); `linger` +
  `spore-attach` user shaping.
- **Tier 3 / out of scope**: crash-forensics, post-mortem dispatch (nice-to-have);
  webhook daemons, idle-watchdog, done-sweep (mcom product-specific - skip).

## Phase 4 - Hetzner deploy (DEFERRED)

Template mcom's deploy pattern, open-source-clean: agenix secrets +
`secrets/recipients.txt` (no real secrets in git), disko disk-config, colmena
targets, `nix/hosts/<host>/` modules, `nix/modules/nix-housekeeping.nix`. Real
IP/hostkey via gitignored `local.nix` (+ committed `local.nix.example`). Wire
`services.spore-fleet` + `matters.linear` (team `ROC`, newbuilds workspace). Do not start until
Phases 1-3 land.

## Validation gates (every commit)

`spore lint` + `go test ./...` + `nix flake check` green. The NixOS module test
(`checks.nixosModules-spore-fleet`) must pass after the single-project collapse.

## Open questions / risks

- Single-project collapse touches `nixosModules/spore-fleet.nix`,
  `bootstrap/flake/`, `internal/infect` (spore-projects.nix generation). Confirm
  no consumer depends on the `projects` attr before removing it.
- `codexpolicy` vs evolved codex hooks - dedupe decision in Phase 2.
- VERSION bump strategy: jump 0.4.2 -> 0.9.2-derived line, or restart. TBD.
