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
- DONE (this session): **ROC-18** cross-repo ship design written
  (`docs/cross-repo-ship.md`): decision is independent sibling repos
  (spore harness repo stays code-free; code repos are separate siblings in
  `~/.config/wt/projects`). The ship cycle is already repo-relative
  (projectRoot = parent of the tasks dir, lifecycle.go:691), so a ticket
  targets one code repo and ships there unchanged; a cross-repo feature is
  decomposed by the coordinator into per-repo child tickets (1:1, blocked-by
  ordered). No task/ship/merge/gh change - only coordinator routing + a
  `repo:` ticket field. ROC-18 left OPEN for operator review.
  Also ported the **ctx context statusline** from mcom/wt-go as `spore
  token-status --statusline | --fleet` (internal/tokenstatus): claude-code
  statusLine reader for the contexttee Stop-hook tees, renders
  `ctx 87k / 190k (43%) max`. Live transcript parse -> tee fallback ->
  zero render. The user's `~/.claude/settings.json` can swap
  `wt-task token-status` for `spore token-status` once deployed.
- DONE (this session): **ROC-13** shims verified + closed on the board.
  flake.nix `shims` builds (7 host-shim bins + hooks + settings + reconcile
  units), `shims-layout` check + the spore-fleet VM-test activation assert
  /usr/local/bin/spore-* symlinks; all three gates green. Minor non-blocking
  note: handover/systemd has an evict-idle pair the shims package omits;
  harmless (module declares both units inline).
- DONE (this session): **ROC-10** recipe library re-applied. internal/recipes
  (List/Get over embedded tree), bootstrap/recipes/{jira,github,sentry}.md,
  `spore recipes ls|show` wired into main dispatch + usage, embed.go
  BundledRecipes, bundled coordinator role Recipes pointer. Internal Jira/Sentry
  project keys (MT3, MARKETER) genericized to PROJ in worked examples
  (opensource-bound). All three gates green. Commit a45d8d4.
- DONE (this session): **ROC-11** migrations engine re-applied. internal/migrations
  (idempotent NNN-slug.sh runner + ledger), bootstrap/migrations/{001,README},
  `spore migrate [--auto] [--dry-run]` wired into main dispatch + usage, embed.go
  BundledMigrations, docs/migrations.md. No deployed hosts yet (Phase 4); engine
  lands ahead of its first live consumer. All three gates green. Commit 22a034d.
- DONE (this session): **ROC-12** marketertechnologies fleet identity + infect
  push-first guard. internal/infect gains SporeOwner=marketertechnologies /
  SporeFlakeURL consts, RequireSporeCommitOnOrigin (aborts the wipe when the
  build commit is not on origin, keeping the deployed binary reproducible),
  Config.SporeCommit wired from spore.BuildCommit(). PinBundledSpore + a
  bundled-flake spore input deliberately NOT re-applied: the bundled bootstrap
  flake is throwaway, steady-state delivery is the host flake's job (inputs.spore,
  ROC-13); pinning it would fight ROC-13. codexpolicy already folded into
  internal/agentpolicy/codex (decision: fold). Fixed docs/infect.md (described
  the obsolete flake-input pinning) + docs/fleet-module.md/README.md (pointed
  fleet hosts at upstream versality, not this fork's origin). All three gates
  green. Commit aa09295.
- DONE (this session): **ROC-15** mcom tier-2 wisdom distilled. Three
  commits on the integration branch:
  - PreToolUse block suite, Go-native in internal/hooks/pretooluse.go
    (new `Decide(req, PreToolUseConfig)`; `PreToolUse(req, forbidden)`
    kept as the bash+AskUserQuestion subset). Four mcom blocks
    translated, generalized, name-swept: secrets-inference (env-dump /
    set-x / proc-environ bash patterns + secret-path reads of the XDG
    decrypt scratch, ~/.config/spore/secrets.env, age keys),
    memory-writes (deny claude auto-memory writes; context tiers into
    CLAUDE.md/docs), test-gate (deny hand-run `gh pr create` in a worker
    worktree -> `spore task ship`), tasks-projection (deny minting a NEW
    tasks/<slug>.md but ALLOW editing an existing one - spore workers
    report through their own task file's plan section, so mcom's blanket
    tasks/ write-block would have conflicted). Wired `spore hooks
    pretooluse` into configs/claude PreToolUse. Commit 20455d7.
  - Opt-in long-lived coordinator systemd-user service in
    nixosModules/spore-fleet.nix (`services.spore-fleet.supervise.enable`,
    default off). ExecStart `spore coordinator spawn` + the 0/1/64
    exit-code contract (ROC-14) wired to Restart=on-failure /
    RestartPreventExitStatus=1 / StartLimitBurst over
    StartLimitIntervalSec, KillMode=process. Default off preserves the
    bundled reconcile-timer deployed model. VM module test opts in and
    asserts the guards render. Commit 2d3fe3c.
  - Declared `users.users.spore.linger = true` in the bootstrap
    configuration.nix (spore-attach login shell was already there; linger
    was only set imperatively by `spore infect`). Bootstrap
    nixosConfiguration evaluates clean. Commit on branch.
  All gates green incl. full `nix flake check`. ROC-15 closed on the
  board.
- DONE (this session): **ROC-16** Phase 4 deploy LAYER (open-source-clean,
  not the live deploy). Commit 5e05f43. nix/hosts/rocky/ (one coordinator
  box: disko BIOS-boot+ESP+ext4, EFI GRUB cloud-VM profile, DHCP+sshd,
  spore user with linger+spore-attach + deploy user for colmena, agenix
  linear-api-key, services.spore-fleet + matters.linear ROC) +
  nix/modules/nix-housekeeping.nix + secrets/ (agenix recipients/rules +
  PLACEHOLDER linear-api-key.age so the tree builds before any box) +
  flake.nix disko/agenix inputs, nixosConfigurations.rocky (eval/CI gate)
  + colmena node targeting SSH alias `rocky` (real IP stays out of git
  via gitignored nix/hosts/rocky/local.nix). docs/deploy.md = operator
  runbook. Templated lean from mcom helm-coord, dropping the product
  surface (web/postgres/caddy/container). Token reaches the fleet by file
  path via LoadCredential, never the store/env (agenix lint guards it).
  Verified: rocky toplevel BUILDS with placeholders; spore lint + go test
  + nix flake check all green. The ticket's stated acceptance (3 green
  gates) is MET. The LIVE deploy (provision Hetzner box, ssh-keyscan the
  host key, agenix -e the real ROC token, colmena apply) is operator-bound
  and remains TODO per docs/deploy.md - not blocking ticket closure.
- DONE (this session): **rocky LIVE** on Hetzner 178.105.117.8. Installed
  via nixos-anywhere from `.#nixosConfigurations.rocky`; coordinator
  spawns, persists, and serves the ROC board; matter sync runs clean.
  Bringing up the first real fleet host surfaced four deploy-blocking
  fleet-module bugs (all fixed declaratively + VM-test green, commits
  3466b45 + the TMUX pin):
  - fleetBinPath: unit PATH lacked grep/sed/awk/ripgrep/find; claude-code
    shells out to them on startup, so the agent died before settling.
  - SHELL=bash: tmux ran the coordinator command via the user's passwd
    shell (spore-attach on a deployed host), which hijacked it; pin a
    real bash.
  - KillMode=process: the oneshot reconcile's default control-group reap
    killed the coordinator tmux server the instant reconcile exited.
  - mkEnvList: unquoted systemd Environment= truncated matter values with
    spaces ("In Progress" -> "In"), breaking matter sync.
  - TMUX_TMPDIR=%t: coordinator was on /tmp, spore-attach on /run/user;
    pin both to the runtime dir so the operator can attach.
  Auth: Claude Code **subscription only** (operator logged in on the box
  as spore; org had to enable Claude Code access). HARD RULES now in
  memory: never `claude -p`, never the Anthropic API, claude always in a
  tmux session. Secrets host-only (Linear token + the on-box claude
  creds); nothing secret in the repo. Box state lives in
  /var/lib/spore-secrets + /home/spore/.claude (not nix). Remaining
  operability follow-ups (not blocking): the spore-fleet module does not
  yet provision the spore user's ~/.claude settings/hooks (done manually
  on rocky) or run `spore migrate` with bash on PATH (activation warning,
  non-fatal); fold these into the module for a clean reimage.
- Remaining: ROC-18 (cross-repo ship design) open for operator review.
  Phases 1-4 complete; rocky is serving ROC. Feed it `Todo` tickets to
  watch end-to-end worker pickup.
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
- VERSION: bumped 0.4.2 -> 0.9.2 (the fork's kernel IS evolved spore
  0.9.2). RESOLVED.
