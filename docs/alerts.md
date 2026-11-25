# Usage-limit and login alerts

A usage limit and an expired login belong to the Claude account, not to one pane: when one pane hits them, every Claude Code pane on the host stops. Spore detects both from Claude Code's `StopFailure` hook and tells the coordinator and the operator once per incident.

## Detection

Claude Code fires `StopFailure` instead of `Stop` when an API error ends a turn. The payload carries `error` (`rate_limit`, `authentication_failed`, `server_error`, ...) and `last_assistant_message` (the banner text, for example `You've hit your limit - resets 2:15pm`). `spore hooks stop-failure` is bound host-wide (managed settings drop-in and `bootstrap/handover/settings.json`) and, in a spore pane (`$WT_PROJECT` set):

- puts the failed turn's inbox messages back (see "Inbox messages" below), for any API error;
- for `rate_limit` raises a `usage-limit` alert, for `authentication_failed` or `oauth_org_not_allowed` a `login` alert;
- records the pane under `$XDG_STATE_HOME/spore/alerts/panes/` (default `~/.local/state/spore/alerts/`) for `spore fleet nudge`.

Panes read hook settings at start, so a pane started before the hook was bound has no `StopFailure` hook. `spore fleet nudge` also covers those by reading the screen.

## Notices and dedupe

Each alert is claimed with an exclusive file under the alerts dir:

- `<kind>@<project>`: one `spore-alert` notice in that project's coordinator inbox. The coordinator is usually stopped too, so it reads the notice after recovery.
- `<kind>`: one notice per host to the operator, shown with `tmux display-message` on every attached client until a key is pressed, and listed by `spore fleet status` as `alert: <time> <text>`.

A successful turn in any spore pane (its Stop hook, `spore hooks watch-inbox`) drops that pane's record and releases claims older than 15 minutes, so the next incident alerts again while a per-minute 429 burst does not re-alert.

A usage-limit notice quotes the reset time when the banner has one. Claude Code resumes those panes itself about a minute after the reset. A 429 without a reset time ("not your usage limit") is never retried.

## Recovery

Login expired:

1. Run `/login` once in any Claude Code pane (for example `tmux attach -t spore/<project>/coordinator`). It rewrites `~/.claude/.credentials.json` for every pane. Do not restart panes: they pick up the new login on their next request and keep their context.
2. Run `spore fleet nudge` from a shell on the host. It types a "continue" message into every pane the hook recorded and into any other `spore/` or `spore-role/` pane whose screen shows `Please run /login` or an `API Error` that is not being retried. It never types into `spore/pilot/` sessions. `--dry-run` lists the panes.

Usage limit without a reset time: wait for the limit to clear, then run `spore fleet nudge`.

Check: `spore fleet status` (alerts), and the panes' inboxes drain within a minute.

## Inbox messages

`spore hooks watch-inbox` claims messages into `inbox/inflight/`, not `inbox/read/`. The next Stop hook only runs after a successful turn, so it acks them into `read/`. On `StopFailure` they go back into the inbox as `redeliver-<name>.json`, and the next drain (after the nudge or the automatic resume) delivers them again, marked `redelivered`. A message Claude Code already kept in its context may therefore arrive twice.

## Blank credentials file

On a failed OAuth refresh Claude Code itself writes an empty login into `~/.claude/.credentials.json` (`accessToken: ""`, `expiresAt: 0`), which logs out every pane on the host. This is Claude Code's behavior, reproduced with a standalone `claude -p` and no spore code involved. Spore never writes that file: `spore budget` reads it and, on a 401, marks the budget stale instead of refreshing (see `docs/budget.md`). Recovery is the `/login` plus `spore fleet nudge` above. To check the file without printing tokens:

```
jq '.claudeAiOauth | {expiresAt, has_access: ((.accessToken // "") | length > 0)}' ~/.claude/.credentials.json
```
