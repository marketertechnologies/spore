**Status**: unit 1 (hook parity) landed on wt/codex-hook-parity; units 2-5 open

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
  daemon (`codex app-server daemon start`). With the daemon, the agent
  loop and its env live in the daemon process.

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
- Unit 2 must register `spore hooks codex-session-start` as a Codex
  SessionStart hook; until then the sidecar idles.

## Open risks

- UNVERIFIED: which env Codex hooks see when the TUI is attached to the
  daemon (the daemon's or the TUI's). The session key comes from env;
  if hooks see the daemon env, two coordinators sharing one
  `CODEX_HOME` daemon would record under the first launcher's key.
  Verify live in unit 2; parallel coordinators may need one
  `CODEX_HOME` per session.
- Upstream 8301262 (codex hooks config scoped per session kind) is a
  candidate crib for unit 2. d0564b8 and d9f2ab2 target packages this
  tree lacks (`internal/agentpane`, `internal/hooks/codex`).
