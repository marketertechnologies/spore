**Status**: unit 1 (hook parity) merged (PR #81); unit 2 (config render) landed on wt/codex-render-config; units 3-5 open

# codex coordinator swap

Operator-approved 2026-10-21. Goal: the operator can kill a Claude
coordinator and respawn a Codex one on the same session (and back)
without losing context.

Parallel coordinators (Claude and Codex at the same time on one
project) are a later program. Everything built here must be reusable
for it: key nothing on "the" coordinator session name. Per-session
state is keyed by the coordinator session (or its inbox), never by a
hard-coded `coordinator` slug.

## Units

One task and one PR each.

1. Hook parity. Token monitors read Codex rollouts; hook entry points
   tolerate the Codex payload; project-root fallback order
   `SPORE_PROJECT_ROOT`, `CLAUDE_PROJECT_DIR`, cwd; a Codex inbox wake
   (`spore hooks codex-session-start` records the thread id,
   `spore hooks watch-inbox --codex` delivers envelopes via
   `codex queue`); fix `spore task tell coordinator` writing to the
   wrong inbox; launch shim codex branch (hook-trust bypass, daemon,
   sidecar).
2. Render Codex config: `hooks.json`, `~/.codex` user config, and
   `pkgs.codex` in the NixOS module.
3. Context bridge: state.md plus the shared memory index injected on
   Codex SessionStart; skills mirrored into `.agents/skills`.
4. `spore coordinator swap --to codex|claude [--model m]`: forced
   wrap, flip `[coordinator]` driver/model, respawn-pane.
5. Marketer adoption.

## Decisions (do not re-litigate)

- Idle wake for Codex: `codex app-server daemon` plus
  `codex queue --thread <id> --message ...`. Codex hooks have no
  asyncRewake equivalent; the queue wakes an idle thread and defers
  delivery while a turn runs.
- Memory: Codex shares the Claude memory dir
  (`~/.claude/projects/-home-spore-<proj>/memory/`).
- Hook trust: launch with `--dangerously-bypass-hook-trust`.

## Codex facts the units rely on

Researched against codex-cli 0.162.1.

- Hook payloads carry the Claude fields plus `model`,
  `permission_mode`, `turn_id`; Stop adds `stop_hook_active` and
  `last_assistant_message`; SessionStart adds `source`
  (`startup|resume|clear|compact|fork`).
- Stop blocks via exit 2 with non-empty stderr (same as Claude).
  Exit 2 with empty stderr is a failed hook, not a block.
- Hooks see no `CODEX_THREAD_ID` env; the thread id is the payload
  `session_id`.
- Rollouts: `$CODEX_HOME/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl`,
  compressed to `.jsonl.zst` after 7 days. Current context size is
  `last_token_usage.total_tokens` of the latest
  `event_msg`/`token_count` record.
- `codex queue` needs the TUI attached to the per-`CODEX_HOME`
  daemon (`codex app-server daemon start`). Verified live: hooks run
  as children of the TUI process with the TUI's env, also on turns
  started by `codex queue`. The daemon's env does not reach hooks.

## Unit 1 as built

- Thread record: `<dir of $SPORE_TASK_INBOX>/codex-threads/<key>.json`,
  key = `$SPORE_COORDINATOR_SESSION` (the shim exports it, default the
  tmux session name) with `/` mapped to `_`. Every SessionStart source
  overwrites it, so `/clear` and fork are followed.
- Sidecar: claims with the Claude drain's rename into `inbox/read/`,
  so a Claude drain and any number of sidecars on one inbox deliver
  each envelope once. A failed `codex queue` renames the batch back.
  Batches split under the 128 KiB argv limit. Nothing is claimed until
  a thread is recorded.
- Per-session `CODEX_HOME`: the shim sets
  `CODEX_HOME=<coordinator state dir>/<project>/codex-home/<session>/`
  (session `/` mapped to `_`), creates it, and symlinks `auth.json`,
  `config.toml`, `hooks.json`, `AGENTS.md` from `~/.codex` (or the
  inherited `CODEX_HOME`) when present and not already there. It then
  stops and starts that home's daemon so the daemon runs with this
  pane's env. Each session owns its daemon, rollouts, queue and thread
  store; parallel coordinators need this. Codex writes `auth.json` in
  place and resolves symlinks before writing `config.toml`, so logins
  and config edits land in `~/.codex`. Without an inbox or session key
  the shim falls back to the inherited home and never stops its daemon.
- Unit 2 consequence: user-level Codex config (`config.toml`,
  `hooks.json`, `AGENTS.md`) lives in `~/.codex` and reaches sessions
  only through these links. A file unit 2 adds must join the shim's
  link list. `codex resume` sees only the session's own home.
- Unit 2 must register `spore hooks codex-session-start` as a Codex
  SessionStart hook; until then the sidecar idles.

## Unit 2 as built

Hook facts below read from the codex-rs 0.163.0 source (the nixpkgs
pin); not yet verified against a live session.

- `$CODEX_HOME/hooks.json` is the user layer; a trusted project's
  `.codex/hooks.json` is a further layer. Layers append, and duplicate
  handlers all run (no dedupe). Hooks are on by default (`hooks`
  feature, stable). The top level is `deny_unknown_fields`
  (`hooks`, `description`); handler keys are not, so a Claude
  `asyncRewake` is silently dropped and the hook runs synchronously.
  `async` is honored except on SessionEnd. Timeout defaults to 600s.
- Source of truth: `hooks.CodexUserEvents` in
  `internal/hooks/codexconfig.go`, rendered by `hooks.CodexHooks`
  (rejects `AsyncRewake`) and printed by
  `spore hooks codex-config [--spore PATH]`. Bindings: SessionStart
  `codex-session-start`; Stop `coordinator token-monitor`,
  `worker token-monitor`, `fleet replenish-hook`. No watch-inbox
  (the sidecar replaces it), no Notification (Codex has no such
  event), no Claude-tool PreToolUse perl hooks. `load-state-md` is
  unit 3.
- Who renders: the NixOS module, under
  `services.spore-fleet.codex.enable` (default off). It runs the
  package's own `spore hooks codex-config` at build time, with hook
  commands pinned to `cfg.package`, into home-manager
  `home.file.".codex/hooks.json"`; it also adds `codex.package`
  (default `pkgs.codex`) to systemPackages and the reconciler PATH.
  One opt-in knob: the coordinator driver lives in runtime config
  (`spore.toml`, `/etc/spore/coordinator.env`) that Nix cannot read
  at eval time, and Claude-only hosts skip the codex closure. The
  infect host flake sets it. Non-Nix hosts run
  `spore hooks codex-config > ~/.codex/hooks.json`.
- `config.toml`, `auth.json`, `AGENTS.md` stay unmanaged: Codex
  writes the first two in place, and a store symlink would break
  that. The shim's link list already carries `hooks.json`; no shim
  change.
- Removed the tracked repo `.codex/hooks.json`: with the user layer
  in place it double-fired Stop hooks in a trusted checkout and ran
  `watch-inbox` as a blocking Stop hook.
- Workers with `agent: codex` also get the user hooks. Their
  SessionStart writes a thread record next to the worker inbox;
  nothing reads it.

## Open risks

- Upstream 8301262 scopes Codex hooks per session kind via a
  spawn-rendered project `.codex/hooks.json`. Unit 2 took only its
  renderer shape; kind scoping is unneeded while every bound command
  self-gates on env. d0564b8 and d9f2ab2 target packages this tree
  lacks (`internal/agentpane`, `internal/hooks/codex`).
- Hook firing under Codex is unverified live until a host with
  `codex.enable` is rebuilt (operator-bound).
