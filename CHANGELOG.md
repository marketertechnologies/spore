# Changelog

## Unreleased

- `bootstrap/handover/spore-coordinator-launch.sh`: support a host-wide
  shared role file in addition to the per-project `$SPORE_COORDINATOR_ROLE`.
  The launcher now reads `$SPORE_COORDINATOR_ROLE_SHARED` (default
  `${XDG_CONFIG_HOME:-$HOME/.config}/spore/coordinator-role.md`) and
  concatenates the shared file and the project role file with a blank
  line between them before passing the result as the agent's first user
  message. Each part is optional; missing parts are skipped, and when
  both are missing the agent launches unseeded as before. Operators can
  factor common respawn / wrap-up / operating-regime boilerplate out of
  per-project role files and keep only project-specific identity and
  deltas in each `bootstrap/coordinator/role.md`. Backward compatible:
  hosts without a shared file behave exactly as today.
- `nixosModules/spore-fleet`: replaced single-project
  `services.spore-fleet.projectRoot` with the multi-project
  `services.spore-fleet.projects` attrset
  (`name → { path }`). The module now generates one
  `spore-fleet-reconcile-<name>` service + timer + path watchers
  per entry, so a single host can reconcile several project trees
  under the same `user` without external glue. Tmux session naming
  (`spore/<name>/coordinator`, `spore/<name>/<slug>`) is already
  namespaced by project, so coordinator and worker sessions stay
  isolated.

  `projectRoot` is kept as a deprecated alias: when set (and
  `projects` is empty), it surfaces as
  `projects.${baseNameOf projectRoot}.path`, with a deprecation
  warning. Setting both is an assertion error; setting neither
  with `enable = true` is also an assertion error.

  The kill-switch flag at `~/.local/state/spore/fleet-enabled`
  remains host-wide; flipping it triggers reconciles across every
  project. The pre/post graceful-deploy scripts now drain workers
  for every configured project sequentially.
- `matter.linear`: Sync now projects new Linear comments per active
  ticket as tell-envelope JSON files in the matching rover slug's
  spore inbox dir, so an operator commenting on a Linear ticket
  reaches the rover working it via the existing watch-inbox Stop
  hook. Per-ticket cursor lives at
  `<StateDirForProject>/<slug>/linear-comments.cursor`; first
  observation seeds at "now" so historical comments do not flood
  the inbox.
- Renamed the per-task inbox env var from `SKYBOT_INBOX` to
  `SPORE_TASK_INBOX` to drop a foreign prefix that had leaked in
  from a consumer harness. Spore sets and reads the new name only;
  consumers that exported `SKYBOT_INBOX` for operator-side hooks
  must rename on their side. No backward-compat alias.

## 0.4.2 - 2026-05-06

- Fixed `spore coordinator start` lying about success when the agent
  binary failed to exec. `driverToBinary("claude")` returned the
  package name `claude-code` instead of the actual binary name
  `claude`, so the inner shell command died on `exec` and tmux tore
  the session down before any `has-session` check ever ran. Two
  changes: (1) "claude" now maps to `claude` (the kernel default
  fallback also moves to `claude`); (2) `EnsureCoordinator` now
  waits a short settle window after `tmux new-session -d` and
  re-checks the session, returning a real error if the spawn died.
- Synced `VERSION` with the git tag scheme (was stuck at `0.0.3`
  while tags advanced to `v0.4.x`). New `just release X.Y.Z` recipe
  validates clean+green, bumps `VERSION`, commits, and tags so the
  two stay in step from now on.
- Refactored `embed_test.go` to read `VERSION` at test time instead
  of hardcoding the expected string, so a release no longer requires
  a paired test edit.

## 0.0.3 - 2026-05-05

Spore 0.0.3 lands the universal coordinator entry point. "How do I
start the coordinator?" now has the same answer for every consumer:
`spore coordinator start`.

- Added `spore coordinator start [--wait] [--poll-sec N]`,
  `stop`, `restart`, and `status` over the existing fleet
  coordinator helpers. `start` is idempotent; `--wait` blocks
  until the session exits (helm-spawn / skyhelm-spawn shape).
- Added the `[coordinator]` section in `spore.toml` with `driver`,
  `model`, and `brief` keys. Env vars still win; driver "claude"
  maps to `claude-code`, "codex" to `codex`, and any other value
  passes through (so a project can point at a launcher script).
- Injected `SPORE_COORDINATOR_PROVIDER` and
  `SPORE_COORDINATOR_MODEL` into the session env when configured,
  matching the bundled `spore-coordinator-launch` dispatch shape.
- `status` prints up/down, the configured driver/model/brief, and
  exits 3 when the coordinator session is down (0 when up).

## 0.0.2 - 2026-05-04

Spore 0.0.2 is a focused release: it can now run Codex-backed workers
as a first-class task option while keeping the
existing Claude Code path intact. Feedback and rough edges are welcome.

- Added Codex worker support through `agent: codex`, with task-level
  model and reasoning-effort selection.
- Kept mixed Claude Code and Codex workflows on the same task
  frontmatter, tmux worker sessions, inbox/tell protocol, and merge
  close path.
- Bumped the package and CLI version to `0.0.2` and added
  `spore version`.
- Smoothed the README opening flow by removing the "Choose a Path"
  table.
