# Events ledger and `spore report`

Spore writes one append-only ledger per project and reads it back as a morning digest.

## Ledger

Path: `<state dir>/spore/<project>/events.jsonl`, where the state dir is `$XDG_STATE_HOME` or `~/.local/state` and `<project>` is the main repo's basename. One JSON object per line:

```json
{"ts":"2026-11-26T03:12:09Z","kind":"worker.crashloop","project":"spore","slug":"demo","detail":"3 fast deaths in a row; respawn stopped"}
```

`fields` carries short structured values (role, round, verdict, url, error kind). The writer is `internal/events`; every call site goes through `events.Record` (or `task.RecordEvent`, which resolves a project root to its name and records the root in `project-root` next to the ledger for `spore report --all`).

Bounds: past 1 MiB the file rotates to `events.jsonl.1`, so at most two generations exist. A write takes `events.jsonl.lock` (flock) for the rotate plus append. Writes are best-effort: a failure prints `events: ...` to stderr and never fails the caller.

Content rules: no prompt text, no model output, no secret values. `detail` and `fields` are flattened to one line, capped (300 and 200 bytes) and scrubbed for token shapes (GitHub tokens, `sk-ant-` keys, Slack tokens, `Bearer ...`, `*_TOKEN=` / `*_SECRET=` assignments, credentials in URLs, PEM headers). The StopFailure alert records the error kind and the parsed reset time, never the assistant message.

| kind | written by | when |
| --- | --- | --- |
| `task.start` | `spore task start` | status flipped to active and the session spawned |
| `task.pause`, `task.block` | `spore task pause` / `block` | status flipped |
| `task.done` | `spore task done`, `spore task merge` | status flipped to done (`fields.unmerged_discarded` on `--force`) |
| `worker.spawn`, `worker.respawn` | reconcile pass | a worker session was started; respawn when the previous run was a fast death |
| `worker.crashloop` | reconcile pass | `MaxFastDeaths` fast deaths in a row; respawn stopped |
| `coordinator.spawn`, `coordinator.respawn` | reconcile pass | coordinator started (respawn once it has run before; `fields.error` when the spawn failed) |
| `coordinator.crashloop` | reconcile pass | coordinator respawn stopped |
| `role.respawn`, `role.crashloop` | role-loop drive | a dead role pane was respawned / the per-round cap was hit |
| `role.phase` | role-loop drive | the derived phase changed since the last drive (`fields.from`) |
| `role.verdict` | role-loop drive | once per valid review file (reviewer, round, verdict, grade counts) |
| `alert` | `spore hooks stop-failure` | any Claude Code `StopFailure` (`fields.error` is the kind) |
| `reconcile.skipped` | reconcile pass, replenish hook | lock held, fleet disabled, or budget advice; at most once per hour per reason |
| `reconcile.failed` | `spore fleet reconcile`, replenish hook | the pass returned an error or blew its time budget |
| `pr.opened` | role-loop PR mirror, `spore gh pr create`, `spore gh exec -- gh pr create` | a PR was created (`fields.url`) |

## `spore report`

```
spore report [--since 12h] [--project <name> | --all] [--full]
```

Prints the window, the kill-switch state and active alerts, then per project: tasks by status (live from `tasks/`), tasks done and started, PRs opened, an attention block (escalated role loops, blocked tasks, coordinator down, crash loops, alerts, failed or skipped passes), what is running now, role-loop progress and respawn counts. The default view stays under one screen; `--full` appends every event in the window.

`--project` and `--all` read live state from the root the ledger recorded; a project whose root was never recorded shows `tasks: unknown`.
