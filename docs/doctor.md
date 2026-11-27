# `spore doctor`

Read-only drift checks for a spore host. Each check prints one `PASS`, `WARN`, `FAIL` or `SKIP` line; the command exits 1 when any check fails. `--json` prints `{"results": [...], "fail": N, "warn": N}`. `scripts/smoke-post-upgrade.sh` runs it as its last step.

| check | fails when | warns when |
| --- | --- | --- |
| `binary` | `<sw>/bin/spore --version` does not run | the deployed binary differs from the one running doctor |
| `binary vs repo VERSION` | | the deployed version is behind (deploy pending) or ahead of a spore checkout's `VERSION` (`--repo`, default the cwd's repo when it is spore) |
| `binary vs flake input` | the deployed commit is not the spore rev locked in `--flake-lock` (default `/etc/nixos/flake.lock`): the switch was not applied | the deployed binary reports no commit |
| `managed settings: hooks`, `: deny`, `: other keys` | the live drop-in differs from what the running binary renders with `--managed` (system-profile paths, the live file's plugin marketplace if any) | |
| `managed settings: sandbox` | the live and rendered sandbox blocks differ | skipped while the binary renders none |
| `claude code version` | | `claude --version` is not `VerifiedClaudeCode`, or does not run |
| `fleet units: <project>` | the project's reconcile timer or path units are not loaded and active, or its last reconcile run did not succeed | no `spore-fleet-reconcile-*` user units at all |
| `secrets file modes` | a `secrets.env`, `*.pem`, `*.key`, `github-token.json` or `github.toml` under `~/.config/spore` or the spore state dir is readable by group or others | |
| `npmrc tokens` | | `~/.npmrc` sets `_authToken`, `_auth` or `_password` to a literal value (not a `${VAR}` reference); the sandbox leaves that file readable. A file named by `NPM_CONFIG_USERCONFIG` is readable too and not checked, so only a narrowly scoped token belongs there. Names the line, never the value |
| `bundle tokens` | a GitHub token (`ghp_`, `github_pat_`, `gho_`, `ghs_`, `x-access-token`) in the cwd project's `.bundle/config` (worktree and main checkout) or in a file `BUNDLE_CONFIG` / `BUNDLE_USER_CONFIG` names: agents read those directly | a GitHub token in `~/.bundle/config` (masked from sandboxed commands, still a broad credential on the host), or that file is group- or world-readable or carries an ACL. Names the line and key, never the value. From a sandboxed pane `~/.bundle/config` is the sandbox mask, so the check skips it with a note (and reports skip when nothing else is found); run it from an operator shell to grade that file |
| `kill switch` | | the fleet is disabled |

Run the binary you want to judge the host against: the deployed one after a switch (the smoke script does), or a fresh build before a deploy to see what the switch will change.

## Claude Code version

`internal/doctor.VerifiedClaudeCode` is the Claude Code release spore was last verified against. Bump it only after re-running, against that release in a sandbox (scratch `HOME`, `TMUX` and `TMUX_PANE` unset, tmux on a `-S` socket in a temp dir):

- the PreToolUse guard and `permissions.deny` probes (force push, `+refspec` push, credentials and `secrets.env` reads);
- the Stop hooks: `watch-inbox` delivery, `engineer-check` exit 2, the replenish hook;
- the `StopFailure` hook payload (`error` values, reset banner text);
- a throwaway role-loop task end to end (spawn, wake, verdict files, done).

Record the run here.

| version | verified | notes |
| --- | --- | --- |
| 2.1.329 | 2026-11-25 | P0 re-run against live spore 0.40.0 with the managed drop-in: hooks, deny rules, role loop |
| 2.1.317 | 2026-11-24 | P0 and P1 validation runs (spore 0.34.0) |
