# Secrets broker

Agents use project secrets (read-only database URLs, Sentry, NewRelic) and the GitHub App without ever reading a value. The values live with a separate system user; agents reach them only through a broker that runs allowlisted commands and returns redacted output.

## Why

Without the broker, agents run as the user that owns `~/.config/spore/**` and the App key, so permission deny rules are speed bumps: any path the rules miss (a copy, `base64`, an env dump, a script) reads the value, and from there it reaches transcripts, PRs and the model provider.

## Shape

- **Owner.** `spore-secrets` (a system user from the NixOS module) owns `/var/lib/spore-secrets`. The agent user (`services.spore-fleet.user`) cannot list or read it. Human pilots read it through the `spore-pilots` group, which the agent user must not join (the module asserts this; the broker refuses to start otherwise).
- **Broker.** `spore-secrets.socket` listens on `/run/spore-secrets/broker.sock` (mode 0660, group `spore-secrets-clients`: the agent user and the pilots) and starts `spore secrets serve` as `spore-secrets`. The service has `ProtectHome=true`, `ProtectSystem=strict` and a private `/tmp`, so it cannot read agent worktrees either. Each connection is identified by the kernel's peer credentials (`SO_PEERCRED`); uids other than root, pilots and the agent user are refused.
- **Config.** `/etc/spore/secrets-broker.json`, rendered from `services.spore-fleet.secrets`. It names files, profiles and commands, never values.
- **Audit.** Every call appends a JSON line to `/var/lib/spore-secrets/state/audit.log` (caller uid and pid, claimed project and task, op, profile, command, arguments redacted, exit code, refusal). Refusals also go to the journal (`journalctl -u spore-secrets`).

## Files

```
/var/lib/spore-secrets/secrets/secrets.env            global keys, layered under each project
/var/lib/spore-secrets/secrets/<project>/secrets.env  project keys (win over global)
/var/lib/spore-secrets/secrets/<project>/github.toml  app_id, installation_id, private_key (and api_url)
/var/lib/spore-secrets/secrets/<project>/app.pem      the App key; private_key = "app.pem" resolves here
/var/lib/spore-secrets/state/                         audit.log, grants.json, git/<project>.git
/var/lib/spore-fleet/fleet-enabled                    kill-switch flag
```

Keep files `spore-secrets:spore-pilots` mode 0640. tmpfiles re-applies the owner and group on boot and switch.

## Profiles

A profile is a project, some of its variables, and the commands allowed to see them. Adding one is a config entry:

```nix
services.spore-fleet.secrets = {
  enable = true;
  pilots = [ "alice" ];
  path = [ pkgs.postgresql pkgs.curl ];
  projects.marketer.repo = "github.com/acme/marketer";

  profiles.marketer-db = {
    project = "marketer";
    description = "read-only production replica";
    vars.DATABASE_URL = "MARKETER_RO_DATABASE_URL";
    commands.psql = {
      argv = [ "psql" "\${DATABASE_URL}" "-X" "-A" "-v" "ON_ERROR_STOP=1" ];
      args = [ "-c" "[^\\\\]*" ];   # -c <sql>, no backslash meta-commands
      stdin = false;
      timeout = "120s";
    };
  };

  profiles.marketer-sentry = {
    project = "marketer";
    vars.SENTRY_TOKEN = "SENTRY_AUTH_TOKEN";
    commands.get = {
      argv = [ "curl" "-sS" "--fail-with-body" "-H" "Authorization: Bearer \${SENTRY_TOKEN}" ];
      args = [ "https://sentry\\.example\\.com/api/0/[A-Za-z0-9_./?=&%-]*" ];
    };
  };

  profiles.marketer-newrelic = {
    project = "marketer";
    vars.NR_KEY = "NEW_RELIC_USER_KEY";
    commands.nrql = {
      argv = [ "curl" "-sS" "--fail-with-body" "-H" "API-Key: \${NR_KEY}" "-H" "Content-Type: application/json" "https://api.newrelic.com/graphql" "--data-binary" "@-" ];
      stdin = true;
    };
  };
};
```

Rules the broker enforces:

- The caller names a command by its key and supplies only trailing arguments. Each argument must fully match its positional regexp in `args` (`variadic` repeats the last one); no `args` means no arguments. Caller arguments are never `${}`-expanded, so an agent cannot echo a variable back.
- `${NAME}` in `argv` expands only from the profile's `vars`; `argv[0]` must be a literal program.
- The command runs in an empty scratch directory with `PATH`, a scratch `HOME` and the profile's variables only (not the rest of the project's secrets).
- stdout and stderr (each capped at 4 MiB) are redacted against every value of 8+ characters in the project's layered `secrets.env` (`[redacted:KEY]`), token shapes, PEM keys and home paths, as the PR mirror does (`internal/redact`).

Agents use them as:

```
spore secrets list
spore secrets run marketer-db -- psql -c "select count(*) from orders"
echo '{"query":"{ actor { user { name } } }"}' | spore secrets run --stdin marketer-newrelic -- nrql
```

### Writing safe commands

The broker hides values; it cannot make an unsafe command safe. Narrow every pattern:

- Refuse arguments that let the program read local files or run a shell: psql backslash meta-commands (`\!`, `\o |`, `\copy ... program`, `\setenv`), `curl -K`, `-o`, `-d @file`, `--config`. Redaction catches a value printed verbatim, not one that was reversed, encoded or sent elsewhere.
- Pin URLs with an anchored host so a credential header cannot be sent to another host. The sandbox egress allowlist (agent-sandbox) covers the agent's own processes, not the broker's.
- Prefer a fixed `argv` plus one constrained argument over `variadic` free text.

## Who may use what

- **Coordinator** (a caller with no `SPORE_TASK_SLUG`): every profile, every project.
- **Worker** (with `SPORE_TASK_SLUG`): its own project's profiles plus live grants.
- **Pilots and root**: everything, plus `grant`, `revoke` and re-enabling the fleet.

```
sudo spore secrets grant <task-slug> <profile> --for 2h [--project <name>]
sudo spore secrets revoke <task-slug> [<profile>]
spore secrets grants
```

Grants expire on their own (at most 7 days) and are logged. `--project` defaults to the project of the current directory; it is the worker's project, not the profile's.

Coordinator and workers run as one uid, so the broker cannot tell them apart: project and task are what the caller claims (from its environment). The scoping limits mistakes and prompt-injected sprawl; it is not a security boundary between agents. The boundary is the value: whatever profile a caller reaches, it gets command output, never credentials.

## GitHub

The App key and every installation token stay in the broker; tokens are minted on demand and cached in memory only.

- The role-loop mirror (`internal/fleet/rolegithub.go`) detects the broker socket and routes its token-bearing calls through it: `gh` calls (`op gh`) and pushes (`op push`). Local reads (`git rev-parse`, `merge-base`) stay local.
- `gh` calls are limited to what the mirror sends: `gh pr list|view|ready|edit|create|comment` (create must be `--draft`) and `gh api` on the project's own `repos/<owner>/<repo>/pulls|issues/<n>[/<sub>]` or a GraphQL query without `mutation`. `--repo`, file inputs (`--body-file`/`--input` other than `-`, `-F k=@file`) and foreign `--hostname` are refused. `GH_REPO` is pinned to `projects.<name>.repo`. Posted text (stdin, `--title`, `--body`) is redacted broker-side, since the agent can no longer read the secrets to redact against.
- Pushes travel as a git bundle: the client bundles the task branch (thin against its `origin/*` refs, the whole branch if the broker asks), the broker fetches it into its own bare mirror (`state/git/<project>.git`), checks the tip is the commit the client named, and pushes it. Only branches matching `pushBranch` (default `^wt/[A-Za-z0-9._-]+$`) are pushed; a rewrite needs `--force-with-lease` against the commit it replaces; a bare force is impossible.
- The base fetch before a new worktree (`fetchBase`) asks the broker for a bundle of `origin/<base>` (thin against the commit the repo already has).
- Coordinators and plain workers push and act on PRs with `spore gh push <branch> [--force-with-lease=<sha>]`, `spore gh pr <verb> ...` and `spore gh api <path> ...`. With the socket present these call the same `push` and `gh` ops, under the same allowlist; without it they run `git push` / `gh` with the App token, like `spore gh exec`, so the same commands work on broker-less hosts. `spore gh push` bundles the local branch tip from the cwd repo (worktrees included), then points `origin/<branch>` at the pushed commit and sets it as upstream. `--body-file <path>` (`api --input <path>`, `pr -F <path>`) is read by the client, only from the cwd repo or `$TMPDIR`, and sent as stdin; `--body-file -` forwards the caller's own stdin. The broker refuses `pr -F <file>`: under `gh pr` it is `--body-file`, and would read a file as the broker user.
- To let a coordinator push branches other than `wt/...`, set `services.spore-fleet.secrets.projects.<name>.pushBranch`, e.g. `^(wt|feature|fix)/[A-Za-z0-9._-]+$`.
- From a sandboxed pane: `spore gh *` is an excluded command, but Claude Code honours that only for a call with no redirect, pipe or chain (`spore gh pr ... < body.md`, `cat x | spore gh ...` and `cd w && spore gh ...` all run sandboxed). Without the broker a sandboxed call cannot read the App key and fails with a hint to run it as one command, which is why the client reads `--body-file <path>` itself. Behind the broker a sandboxed call still works, since it only needs the socket: `/run` is visible inside bwrap, `network.allowAllUnixSockets` is on, and the socket's group (`spore-secrets-clients`) holds the agent user.
- `spore gh token` and `spore gh exec` refuse while the broker socket exists: the App key is not readable. `spore gh config` still manages the agent-side `github.toml`, which now holds only `pr_flow`, `operator_login`, `commit_name` and `commit_email`; the broker-side copy holds the App ids and key.

Any agent can ask the broker to push a branch matching `pushBranch` or open a draft PR, since it shares the reconciler's uid. That is the same thing the role loop does; nothing else reaches GitHub as the App.

## Kill switch

With the broker on, the flag is `/var/lib/spore-fleet/fleet-enabled` (`SPORE_FLEET_FLAG`, set system-wide and in the reconcile units), in a directory owned by `spore-secrets`. `spore fleet disable` from an agent goes through the broker and works; `spore fleet enable` from an agent is refused. `sudo spore fleet enable` (root writes the flag directly) or a pilot's `spore fleet enable` turns it back on. The graceful-deploy scripts remove and recreate the flag as root.

## Claude Code login

`~/.claude/.credentials.json` stays readable by the agent user: Claude Code reads it as that user, and it holds the refresh token. The deny rules (`Read`, `Edit`, `Bash(*.claude/.credentials.json*)`) remain a speed bump. Moving the login behind a helper run as another user does not help: `apiKeyHelper` (and `CLAUDE_CODE_OAUTH_TOKEN`) hand the credential to the `claude` process, and the helper is a command the agent can run itself, so the agent can obtain the same value. Isolating the login needs a different uid for the agent's tool processes than for the `claude` process (sandboxing), not a broker.

## Migrating a host

After the switch that enables `services.spore-fleet.secrets`, move the files (as root) and restart the broker:

```
sd=/var/lib/spore-secrets/secrets
install -d -o spore-secrets -g spore-pilots -m 0750 $sd/<project>
install -o spore-secrets -g spore-pilots -m 0640 <agent-home>/.config/spore/secrets.env $sd/secrets.env
install -o spore-secrets -g spore-pilots -m 0640 <agent-home>/.config/spore/<project>/secrets.env $sd/<project>/secrets.env
install -o spore-secrets -g spore-pilots -m 0640 <app key>.pem $sd/<project>/app.pem
printf 'app_id = ...\ninstallation_id = ...\nprivate_key = "app.pem"\n' > $sd/<project>/github.toml
spore gh config --set app_id= --set installation_id= --set private_key=   # as the agent user
shred -u <agent-home>/.config/spore/secrets.env <agent-home>/.config/spore/*/secrets.env <old key> <agent-home>/.local/state/spore/*/github-token.json
sudo spore fleet enable
```

Tokens that ever sat in transcripts (P0.4) should also be revoked; App installation tokens expire within an hour on their own.

## Tests and sandboxes

The broker socket has a default (`/run/spore-secrets/broker.sock`), so a private HOME, private XDG dirs and a cleared `SPORE_*` do not isolate a process from the live broker: `spore` still finds the host socket and acts through the real GitHub App for any project name the broker knows.

- **Go tests.** A test binary never falls back to the default socket (`secrets.SocketPath` returns an absent path under `testing.Testing()`). A test that needs a broker starts its own and sets `SPORE_SECRETS_SOCKET`. A test that builds and execs the `spore` binary runs a non-test process, so it sets `SPORE_SECRETS_SOCKET` to an absent path in the child's environment (`noBrokerEnv` in `internal/migrations/managed_settings_prune_test.go`).
- **Sandbox recipe.** A sandbox that runs the `spore` CLI (manual validation, `scripts/pr-before-after.sh`, `bootstrap/smoke.sh`) sets `SPORE_SECRETS_SOCKET` to a path that does not exist (for example `$sandbox/no-broker.sock`) and `TMPDIR` to a writable private dir. `env -i` drops `TMPDIR`, and under bwrap the fallback `/tmp` is read-only, so commands that stage temp files fail and some fall back to the broker.

## Verifying

`nix build .#checks.x86_64-linux.nixosModules-spore-secrets` boots a VM with an agent user and a pilot and checks: the agent cannot read the secrets directory, the broker's environment or the audit log; `spore secrets run` returns redacted output and refuses other commands and arguments; agents cannot grant or re-enable the fleet; pilots can; `spore gh token` refuses; the audit log carries no value. `internal/secrets` tests cover the policy, the gh allowlist and push/fetch through bundles against a local origin.
