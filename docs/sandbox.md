# Claude Code sandbox

Every spore pane runs `claude --dangerously-skip-permissions`, so `permissions.deny` and the PreToolUse guard (docs/permissions.md) only match command text: `cat` through a variable, a copied file or a script still reaches a secret. Since 0.60.0 spore also runs every agent Bash command inside Claude Code's own sandbox (bubblewrap plus a network proxy on Linux), which enforces at the OS level what the deny rules only ask for.

Verified against Claude Code 2.1.329 (bubblewrap 0.13.1, socat 1.8.1.3, NixOS, Linux 6.18) on 2026-11-25, by reading the settings schema in the binary and by real `claude -p` runs (see "How it was verified"). Re-check this page when Claude Code changes minor version: several behaviours below are not in the public docs.

## How spore wires it

Two layers, because a managed setting cannot be lifted by any lower tier:

- **Floor, host-wide.** The managed drop-in (`/etc/claude-code/managed-settings.d/50-spore.json`, rendered by `spore hooks settings --managed` from `sandbox.ManagedBlock()` in `internal/sandbox`) carries every sandbox key except `enabled`. The same block is in `bootstrap/handover/settings.json` for hosts without the NixOS module; `handover_settings_test.go` pins the two together.
- **Switch, per pane.** Spore puts `SPORE_SANDBOX_SETTINGS` (`{"sandbox":{"enabled":true,"failIfUnavailable":true,...}}`) into the tmux session env of every coordinator, worker and role pane, and every launcher (`agentInvocations`, `spore-worker-brief`, `spore-coordinator-launch`) passes it as `--settings`. `--settings` sits above user and project settings, so an agent cannot turn it off from `.claude/settings.json`. `respawn-pane` keeps the session env, so a respawned coordinator stays sandboxed. Sessions spawned before the upgrade have no variable and stay unsandboxed until they are recreated.

`enabled` stays out of the managed file so that sessions spore did not launch (the operator's own `claude`) are unaffected and a project can opt out.

```toml
# spore.toml
[sandbox]
# Turn the sandbox off for this project's panes. `spore lint` warns.
enabled = false
# Extra domains for sandboxed commands, on top of the host list below.
allow_domains = ["rubygems.pkg.github.com"]
# Extra writable paths (the worktree, $TMPDIR and the floor's caches are already writable).
allow_write = ["~/.bundle"]
# Commands to run outside the sandbox. Each one is a hole; prefer allow_* entries.
excluded_commands = ["spore-with-secrets *"]
```

`spore sandbox settings --project DIR` prints the pane JSON for a project. A `[sandbox]` section spore cannot parse fails closed (sandbox on, no extras) with a warning at spawn and an error from `spore lint`.

### The floor

| Key | Value | Why |
| --- | --- | --- |
| `allowUnsandboxedCommands` | `false` | The model's `dangerouslyDisableSandbox: true` is ignored. Excluded commands still run outside (verified). |
| `excludedCommands` | `spore gh *` | `spore gh` mints the GitHub App token from the key under `~/.config/spore`, which the sandbox cannot read. The PreToolUse guard limits it to `spore gh pr|api|push`, `spore gh exec -- git|gh ...` and read-only `spore gh config`; `spore gh token`, `spore gh config --set` and `spore gh exec` around any other program are denied. |
| `filesystem.denyRead` | `~/.config/spore`, `~/.local/state/spore/*/github-token.json`, `~/.ssh`, `~/.claude/.credentials.json`, `~/.bundle/config` | Secrets, App keys and tokens, SSH keys, Claude's login, bundler's global config (registry and git-source credentials; bundler treats the masked file as empty, see Findings). The `Read(...)` deny rules are folded in as well. `~/.npmrc` is left readable (yarn 1 breaks on a denied one, see Findings); keep registry tokens out of it and `spore doctor` warns when one is there. |
| `filesystem.allowWrite` | `~/.cache`, `~/go`, `~/.local/state/spore` | Go build and module caches, the nix eval cache, inboxes and task state. |
| `filesystem.denyWrite` | `~/.local/state/spore/*/github.toml` | The unsandboxed `spore gh` loads this file; a sandboxed command must not repoint its key. |
| `network.strictAllowlist` | `true` | Without it the allowlist does not hold under bypass mode (below). |
| `network.allowedDomains` | `api.anthropic.com`, `github.com`, `api.github.com`, `codeload.github.com`, `objects.githubusercontent.com`, `raw.githubusercontent.com`, `proxy.golang.org`, `sum.golang.org`, `cache.nixos.org`, `channels.nixos.org`, `registry.npmjs.org`, `registry.yarnpkg.com`, `rubygems.org`, `index.rubygems.org` | What the fleet needs. Projects extend it with `allow_domains`. |
| `network.allowAllUnixSockets` | `true` | Linux blocks every `AF_UNIX` socket with seccomp unless this is on (no per-path filter), and `nix develop` needs the nix daemon socket. |

## Findings

Behaviour under `--dangerously-skip-permissions`, all observed in real runs:

- **The sandbox holds in bypass mode.** Bash commands run inside bwrap. Writes outside the working directory, `$TMPDIR` (`/tmp/claude-<uid>`) and `allowWrite` fail with `Read-only file system`; `rm -rf` of a path outside the worktree fails.
- **`autoAllowBashIfSandboxed`** only skips permission prompts, which bypass mode already skips. Spore does not set it.
- **`allowUnsandboxedCommands: false`** makes Claude Code ignore `dangerouslyDisableSandbox` (verified: the command still ran sandboxed and could not read the secret). It does not disable `excludedCommands`. It holds from managed, `--settings` or user settings.
- **`excludedCommands`** take a call out of the sandbox only when every part of a pipe or chain matches (`spore gh exec -- git push && echo ok` stays sandboxed because `echo ok` does not match) and never for a call with a `cd`, a command substitution or a redirect other than an fd duplication. Entries are honored only from managed, `--settings` and user settings, not project settings.
- **Network allowlist is not enforced in bypass mode by default.** For a host not in `allowedDomains` the proxy asks the permission callback, and bypass mode answers allow. Debug log (`SRT_DEBUG=1`): `No matching rule, asking user: example.com:443` then `User allowed: example.com:443`, and `curl https://example.com` returned 200. With `network.strictAllowlist: true` the proxy denies without asking (`CONNECT tunnel failed, response 403`). `strictAllowlist` is honored from managed, `--settings` and user settings; while it is on, project-tier `allowedDomains` are ignored, which is why project extras ride in `--settings`. `allowManagedDomainsOnly` is the managed-only alternative; it could not be exercised before deploy because this build hard-codes the managed path (`CLAUDE_CODE_MANAGED_SETTINGS_PATH` is ignored) and ignores the hidden `--managed-settings` flag outside SDK use.
- **Network namespace.** Sandboxed commands get their own network namespace. The host's `localhost` is unreachable (a server on host `127.0.0.1` refused the connection); traffic goes through an HTTP and SOCKS proxy at `127.0.0.1:3128` / `:1080` inside the namespace, set in `HTTP(S)_PROXY`, `ALL_PROXY` and a git `ProxyCommand`.
- **Network bridge needs socat on the profile PATH.** Inside bwrap a bash starts the `socat` bridge. Its stdin is a socket, so NixOS bash sources `/etc/bashrc` and, unless `__ETC_PROFILE_DONE` is set, `/etc/profile`, which resets `PATH` to the system profile. Claude Code's Nix wrapper puts socat only on its own `PATH`, so the bridge dies with exit 127 (`socat: command not found`, seen with strace) and every sandboxed connection fails with `Failed to connect ... over proxy`. Spore panes inherit `__ETC_PROFILE_DONE=1` from the tmux server, so they were not hit; the NixOS module now adds `socat` and `bubblewrap` to `environment.systemPackages` as well.
- **Unix sockets.** With `allowAllUnixSockets` off, seccomp blocks every `AF_UNIX` socket. With it on, sandboxed commands can connect to any socket path they can see, including the nix daemon, a Postgres or Redis unix socket, and the tmux server.
- **Read denies.** A denied directory is mounted empty (`No such file or directory`), a denied file is unreadable (`Permission denied`). Globs are expanded when each command is wrapped, so a file created later is covered from the next command on.
- **Denied files are masked even when absent.** Claude Code bind-mounts `/dev/null` (`nodev`) over every denied file path, whether or not the file exists (`/proc/self/mountinfo` inside the sandbox shows `/null /home/<user>/.npmrc ro,nosuid,nodev`), so opening it fails with `EACCES`. Yarn 1 reads `.npmrc` from every ancestor directory of the cwd, so with `~/.npmrc` in `denyRead` every yarn command in a project under `$HOME` failed (`EACCES: permission denied, open '/home/<user>/.npmrc'`; seen with yarn 1.22.22 on 0.61.0). Since 0.62.1 the floor no longer denies `~/.npmrc`. Before adding a file path to `denyRead`, check that no tool opens it unconditionally.
- **Bundler tolerates a masked `~/.bundle/config`.** Bundler loads its global config only when the file exists and is non-empty (`file.exist? && !file.size.zero?` in `Bundler::Settings#load_config`); the `/dev/null` mask is a zero-size character device, so bundler skips it without opening it and runs with no global settings. Verified with bundler 2.6.9 on ruby 3.4: `bundle install` and `bundle config` succeed with the global config pointing at a masked path, and a `BUNDLE_RUBYGEMS__PKG__GITHUB__COM` credential from the environment is still used. `bundle config set --global` fails (the mask is read-only); use `--local` or env instead.
- **Git worktrees.** `git commit` in a linked worktree works: Claude Code makes the main repo's git dir writable. `.git/hooks` and `.git/config` stay read-only.
- **Placeholder files.** For dotfiles that do not exist in the working directory (`.bashrc`, `.profile`, `.gitconfig`, `.gitmodules`, `.mcp.json`, `.vscode`, `.claude/settings.json`, `.claude/skills`, ...) Claude Code mounts a character device so a sandboxed command cannot create them. Git lists them as untracked and `git add -A` fails (`can only add regular files`); libgit2 fails to open `.gitmodules`, so `nix develop` on a git flake fails (`parsing .gitmodules file: ... Permission denied`). Spore adds the names to `<git-common-dir>/info/exclude` as root-anchored patterns and creates an empty `.gitmodules` when missing, at every spawn (`spore sandbox prepare [DIR]` does the same by hand). The list is `sandbox.PlaceholderPaths`; extend it when a Claude Code release adds names.
- **Background processes die with the command.** Each Bash call is its own bwrap PID namespace; a daemon started with `nohup setsid ... &` is killed when the call returns.
- **Environment.** The sandbox sets `GIT_CONFIG_COUNT` (two `safe.directory` entries), the proxy variables and `TMPDIR=/tmp/claude-<uid>`. `spore gh exec` appends its git config after existing entries; `TestGHExecInjectsToken` now pins the count instead of assuming zero.
- **Deny rules resolve simple variables.** `d=$HOME/.config/spore/x; cp "$d/secrets.env" k` is denied by `Read(~/.config/spore/**/secrets.env)` before it runs, so the probe builds paths inside Python to reach the sandbox layer.
- **Only Bash is sandboxed.** The Read, Edit and Write tools, MCP servers and hooks run outside the sandbox. `permissions.deny` still covers the tools.

## File tools versus the Bash sandbox

The sandbox wraps Bash only. Read, Write, Edit and NotebookEdit run in the Claude Code process and obey `permissions.deny` alone, so under `--dangerously-skip-permissions` they can write anywhere the agent's uid can. Secrets stay safe (the broker keeps them under another uid), but a written file that an unsandboxed process runs later is code execution outside the sandbox. Since 0.68.0 the managed floor carries `Edit(...)` deny rules for those files (`execVectorDeny` in `internal/hooks/permissions.go`; an Edit rule also covers Write and NotebookEdit):

- Any depth on disk (`//**/`): a `.git` directory or worktree pointer file and everything under it, a bare repo's `*.git/hooks/**` and `*.git/config`, `.envrc`, `.mcp.json`, `.claude/settings*.json`, `.claude/hooks/**`, `.claude/agents/**`, `.claude/commands/**`. These also cover the same paths under `~/.claude`. Subagent definitions can declare `hooks:` in their frontmatter, which run outside the sandbox like settings hooks.
- Home: `~/.claude.json`, `~/.claude/plugins/**`, the bash (`.bash_logout` included), zsh and fish (`~/.config/fish/**`) startup files, `~/.npmrc`, `~/.yarnrc` and `~/.yarnrc.yml` (`script-shell` and similar keys run code on the next npm or yarn), `~/.tmux.conf`, `~/.config/tmux/**`, `~/.config/direnv/**`, `~/.gitconfig`, `~/.config/git/**`, `~/.config/systemd/user/**`, `~/.config/environment.d/**`, the husky startup files (`~/.huskyrc`, `~/.config/husky/**`, sourced before every husky hook), `~/.local/bin/**`, `~/.ssh/**`, `~/.config/spore/**`.

Still writable by the tools: the project tree apart from the files above (`.claude/rules/` included), `~/.claude/projects/*/memory/**`, `~/.claude/skills/**`, `~/.local/state/spore/**` and `$TMPDIR`. `spore compose`, `spore install`, `spore hooks settings`, `spore sandbox prepare` and `spore infect` write their `.claude/` and `.git/info/` files from Go, not through the tools, so they are unaffected. Edit a project's `.claude/settings.json`, `.claude/agents/`, `.claude/commands/`, `.envrc` or `~/.npmrc` by hand, or ask the operator.

Rule syntax: `//` is the filesystem root and `~/` the home directory. A bare `**/x` only matches under the session's cwd, and a leading `/` in a managed file has no documented anchor, so the floor never uses either.

How the rules meet Bash:

- Claude Code also applies Edit deny rules to Bash file commands and redirects it recognizes, so `echo x >> .git/info/exclude`, `cp f .git/hooks/pre-commit`, `echo x >> ~/.bashrc` and `rm -rf repo/.git` are denied before they run. `git init`, `git commit`, `git config`, `git clone`, `git worktree add` and `rm -rf <repo>` are not file commands on those paths and keep working (probed 2026-11-26).
- Edit deny rules feed the sandbox's `denyWrite`, but on Linux the sandbox runtime drops a write path that is still a glob once a trailing `/**` is stripped. The `//**/` rules therefore never reach bwrap, and `git init` in `$TMPDIR` is unaffected. The `~/` rules do reach it, but none lies under a sandbox write root (the worktree, `$TMPDIR`, `~/.cache`, `~/go`, `~/.local/state/spore`), so they place no mount. Observed with Claude Code 2.1.329 and `SRT_DEBUG=1`: one `Skipping glob write pattern on Linux: /**/.git` line (and so on) per `//**/` rule, and `No placeholder for non-existent deny path ...: it is read-only there` or `Skipping deny path not within allowed paths` for each `~/` rule. `scripts/sandbox-probe.sh --files-only` checks this on every run. The same log shows that the floor's own `denyWrite` entry `~/.local/state/spore/*/github.toml` is a glob and is skipped too, so on Linux it does not protect that file (pre-existing; see Residual gaps).
- Codex does not read Claude Code's `permissions.deny`, and spore renders no file-path rules into the Codex config (`internal/hooks/codexconfig.go` carries hooks only). Spore launches Codex without sandbox flags, so what its file edits can reach is whatever the host's Codex config sets. Treat Codex panes as keeping this gap.

## Residual gaps (phase 2)

- The tmux server is reachable through its unix socket, and `tmux new-window <cmd>` runs `<cmd>` outside the sandbox. Coordinators need tmux, so phase 1 accepts this.
- `spore gh exec -- gh ...` runs unsandboxed with the App token; `gh` can read local files (`gh api --input`, `gh gist create`). The secrets broker (the next containment phase) replaces the excluded command.
- `spore-with-secrets` reads `~/.config/spore`, so it fails inside the sandbox. A project that needs it today adds `excluded_commands = ["spore-with-secrets *"]`, which lets that command print any secret.
- A coordinator can edit its project's `spore.toml` (it runs at the project root) and opt future panes out. `spore lint` flags it.
- Skills can declare `hooks:` in their frontmatter, and those run outside the sandbox. `~/.claude/skills/**` stays writable by the file tools because coordinators maintain skills there, and a project's `.claude/skills/` stays writable for `spore install` and project-owned skills. A Write to a skill can therefore still plant a hook that runs unsandboxed in a later session. Closing it needs skills maintained from a non-agent path.
- The floor's `denyWrite` entry `~/.local/state/spore/*/github.toml` is a glob, which the sandbox runtime skips on Linux (`Skipping glob write pattern on Linux`), so a sandboxed command can still rewrite a project's `github.toml` under the writable `~/.local/state/spore`. Listing each project's path literally would close it.
- Tool output is not redacted. Claude Code writes every tool result to the session transcript as is, and spore has no hook on tool output, so a credential a command prints (bundler's `Retrying download gem from https://x:<token>@...` line, a `cat` of a config file) lands in the transcript. Spore redacts only what it posts to GitHub (`internal/redact`: token shapes and the password of URL userinfo, `https://user:<anything>@host`) and what it writes to the event ledger (`events.Scrub`, which also catches URL userinfo). Keeping the credential unreadable (the `~/.bundle/config` mask) and narrowly scoped is the defence.
- Docker: with `allowAllUnixSockets`, a sandboxed `docker` CLI can reach a Docker socket and start containers outside the sandbox. None of the current hosts runs Docker.

## Consumer impact

Checked against marketer and crm-gateway (read only):

- **`bin/with-services`**: Postgres and Redis use unix sockets under `~/.local/state/spore/services/<slug>/` (writable, reachable). Three breaks: services started by `bin/with-services up` in one Bash call die when that call returns (run them inside one `bin/with-services exec <cmd>` call instead); the short-path fallback under `/tmp/spore-svc-<hash>/` is not writable (`allow_write` needs the exact directory, it has to exist before the call); crm-gateway's RabbitMQ listens on a per-slug TCP port, which a later call cannot reach from its own network namespace. Services started outside the sandbox (an operator shell) are reachable only over unix sockets.
- **Private gems** from `rubygems.pkg.github.com` (both projects' Gemfiles): add `allow_domains = ["rubygems.pkg.github.com"]`. If the credentials come from `spore-with-secrets`, see the gap above.
- **yarn / npm** (marketer): the public registries are in the floor. `~/.npmrc` is readable by sandboxed commands, so keep registry tokens out of it: put a narrowly scoped token (for GitHub Packages, `read:packages` only) in a separate file and point `NPM_CONFIG_USERCONFIG` at it, or use a `${VAR}` reference in `~/.npmrc`. Sandboxed yarn and npm still need to read whichever file holds the token, so scope it as if an agent can see it. `spore doctor` warns (`npmrc tokens`) when `~/.npmrc` holds a literal `_authToken`, `_auth` or `_password`. Yarn Berry's global cache (`~/.yarn/berry`) needs `allow_write = ["~/.yarn"]`; downloads that bypass the registry (Cypress binary, browsers) need their hosts in `allow_domains`.
- **bundler**: `rubygems.org` is in the floor. Gems installed outside the repo need their path in `allow_write` (`~/.gem`, `~/.local/share/gem`, `~/.bundle`). `~/.bundle/config` is masked (since 0.72.0), so sandboxed bundler sees no global settings and no credentials from it. Supply the private registry credential from the environment instead, and scope it as if an agent can read it: sandboxed bundler needs it, so the agent can see it too (`env`).
  1. Create a classic token with only `read:packages` (GitHub Packages takes no fine-grained token) for `rubygems.pkg.github.com`.
  2. Put `BUNDLE_RUBYGEMS__PKG__GITHUB__COM=x:<that token>` in a mode 0600 file outside every project tree and load it into the environment the project's panes start with (for example `dotenv_if_exists ~/.config/bundle/rubygems-pkg.env` in the project's `.envrc`, or the tmux session env). Never put it in a committed file or a project `.bundle/config`.
  3. Remove `BUNDLE_RUBYGEMS__PKG__GITHUB__COM` and `BUNDLE_GITHUB__COM` from `~/.bundle/config` (`bundle config unset --global <key>` in an operator shell), then `chmod 600 ~/.bundle/config` and `setfacl -b ~/.bundle/config`. Move any non-secret global setting the projects need (`BUNDLE_PATH`, `BUNDLE_JOBS`) to the project's `.bundle/config` or env.
  4. Give agents no git-source token (`BUNDLE_GITHUB__COM`): a classic token for `github.com` reaches every repo its owner can. Gems from private `github:` sources (marketer has several) are fetched only when a Gemfile ref changes; run that `bundle install` in an operator shell, or, if agents must, use a fine-grained token with `Contents: read` on only those gem repos, supplied the same way.
  5. `spore doctor` (`bundle tokens`) warns on a GitHub token in `~/.bundle/config` or on a group- or world-readable or ACL'd one, and fails on a token in a project's app config (`.bundle/config` or `BUNDLE_APP_CONFIG`) or a file `BUNDLE_CONFIG` / `BUNDLE_USER_CONFIG` / `BUNDLE_USER_HOME` names, which agents read directly.
- **`nix develop`** works once the checkout is prepared (spawns do it). A consumer flake that fetches inputs from hosts other than GitHub needs those hosts in `allow_domains`.
- **Docker scripts** (`bin/docker_postgres`, `bin/build_docker_image`, `bin/e2e-gateway-docker`): see the Docker gap; published ports are unreachable from the sandbox.

Suggested starting point for both, to be confirmed by each project's coordinator:

```toml
[sandbox]
allow_domains = ["rubygems.pkg.github.com"]
allow_write = ["~/.bundle", "~/.gem"]
```

Opting a project out (`enabled = false`) restores the old behaviour while its workflows are moved.

## How it was verified

`scripts/sandbox-probe.sh` runs real `claude -p` sessions in a scratch `HOME` under `/tmp` (outside the sandbox's writable `$TMPDIR`), with a fake API key and `ANTHROPIC_BASE_URL` pointing at `scripts/sandbox-probe-stub.py`, a mock Messages API that issues one scripted Bash tool call per run and echoes the tool result. Real Claude Code then builds the real sandbox around the call. The real `~/.claude` and its login are never touched.

- `--mode pre` (default): floor and hooks rendered by the binary under test, plus the pane settings, passed as `--settings`. Run before a deploy.
- `--mode post`: pane settings only; floor and hooks come from the installed drop-in. Run after `nixos-rebuild switch`.
- `--mode none`: `pre` without the sandbox block, the negative control. On 2026-11-25, 7 of the 9 containment checks failed and the script exited 1; the two that still hold are other layers (the `Read` deny rule stops `cat secrets.env`, the PreToolUse guard stops `spore gh exec` around a shell).
- `--mode pre` merges the host's installed drop-in, so until a release without `~/.npmrc` in `denyRead` is deployed, the `cat ~/.npmrc` check fails in `pre`.
- On 2026-11-25, `--mode pre --gh-project <spore checkout>` passed all 14 checks, `go test ./...` of this repo inside the sandbox included.
- `--gh-project ROOT` adds the `spore gh exec -- git push --dry-run` check with a real App token; the token cache stays in the scratch dir, which is deleted.
- File-tool checks (every mode): the stub scripts Read then Write or Edit, and the verdict is the file on disk. Write and Edit must be denied for the scratch repo's `.git/hooks/pre-commit` and `.git/config` and for `~/.bashrc` (the scratch `HOME`'s), and allowed for a memory file, a file under `~/.local/state/spore`, a project source file and `.claude/rules/x.md`. `--files-only` runs just these against a scratch `git init` repo with the rendered floor and the sandbox off. The file tools never enter bwrap, so it also runs from a sandboxed pane. It then runs one sandboxed `git init` with `SRT_DEBUG=1` and grades the sandbox runtime's log: every `//**/` Edit rule must be skipped as a glob, and every `~/` Edit rule must place no mount. The log is written before bwrap starts, so this part also holds inside a sandboxed pane, where the `git init` itself reports SKIP (nested bwrap fails with `ENOSPC`). On 2026-11-26 the 0.64.0 floor passed all 14 file checks and all 30 sandbox-log checks, and the 0.62.2 floor failed the 6 file deny checks.

Containment checks (secrets must not leak, the victim file must survive, the request must not get a 2xx/3xx): `cat` of `secrets.env`, copy then read of `secrets.env`, Python reading `secrets.env`, Python reading Claude's credentials, Python reading the GitHub token cache, an SSH key, Python reading a canary `~/.bundle/config`, `dangerouslyDisableSandbox`, `spore gh exec` around a shell, `rm -rf` outside the worktree, `curl` to a non-allowed domain. Must-work checks: `curl` to an allowed domain, `cat ~/.npmrc` (a non-secret one), `bundle install` with the canary `~/.bundle/config` masked (bundler must report no setting from that file and must take `BUNDLE_RUBYGEMS__PKG__GITHUB__COM` from env; SKIP without `bundle` on `PATH`), `git commit` in a worktree, `nix develop -c true`, `nix develop -c go test ./...`, `spore gh exec -- git push --dry-run`.

To debug the sandbox itself, run a probe with `SRT_DEBUG=1` in the environment: the sandbox runtime then logs every mount, glob expansion and network decision to stderr.
