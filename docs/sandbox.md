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
| `filesystem.denyRead` | `~/.config/spore`, `~/.local/state/spore/*/github-token.json`, `~/.ssh`, `~/.claude/.credentials.json`, `~/.npmrc` | Secrets, App keys and tokens, SSH keys, Claude's login. The `Read(...)` deny rules are folded in as well. |
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
- **Git worktrees.** `git commit` in a linked worktree works: Claude Code makes the main repo's git dir writable. `.git/hooks` and `.git/config` stay read-only.
- **Placeholder files.** For dotfiles that do not exist in the working directory (`.bashrc`, `.profile`, `.gitconfig`, `.gitmodules`, `.mcp.json`, `.vscode`, `.claude/settings.json`, `.claude/skills`, ...) Claude Code mounts a character device so a sandboxed command cannot create them. Git lists them as untracked and `git add -A` fails (`can only add regular files`); libgit2 fails to open `.gitmodules`, so `nix develop` on a git flake fails (`parsing .gitmodules file: ... Permission denied`). Spore adds the names to `<git-common-dir>/info/exclude` as root-anchored patterns and creates an empty `.gitmodules` when missing, at every spawn (`spore sandbox prepare [DIR]` does the same by hand). The list is `sandbox.PlaceholderPaths`; extend it when a Claude Code release adds names.
- **Background processes die with the command.** Each Bash call is its own bwrap PID namespace; a daemon started with `nohup setsid ... &` is killed when the call returns.
- **Environment.** The sandbox sets `GIT_CONFIG_COUNT` (two `safe.directory` entries), the proxy variables and `TMPDIR=/tmp/claude-<uid>`. `spore gh exec` appends its git config after existing entries; `TestGHExecInjectsToken` now pins the count instead of assuming zero.
- **Deny rules resolve simple variables.** `d=$HOME/.config/spore/x; cp "$d/secrets.env" k` is denied by `Read(~/.config/spore/**/secrets.env)` before it runs, so the probe builds paths inside Python to reach the sandbox layer.
- **Only Bash is sandboxed.** The Read, Edit and Write tools, MCP servers and hooks run outside the sandbox. `permissions.deny` still covers the tools.

## Residual gaps (phase 2)

- The tmux server is reachable through its unix socket, and `tmux new-window <cmd>` runs `<cmd>` outside the sandbox. Coordinators need tmux, so phase 1 accepts this.
- `spore gh exec -- gh ...` runs unsandboxed with the App token; `gh` can read local files (`gh api --input`, `gh gist create`). The secrets broker (the next containment phase) replaces the excluded command.
- `spore-with-secrets` reads `~/.config/spore`, so it fails inside the sandbox. A project that needs it today adds `excluded_commands = ["spore-with-secrets *"]`, which lets that command print any secret.
- A coordinator can edit its project's `spore.toml` (it runs at the project root) and opt future panes out. `spore lint` flags it.
- Docker: with `allowAllUnixSockets`, a sandboxed `docker` CLI can reach a Docker socket and start containers outside the sandbox. None of the current hosts runs Docker.

## Consumer impact

Checked against marketer and crm-gateway (read only):

- **`bin/with-services`**: Postgres and Redis use unix sockets under `~/.local/state/spore/services/<slug>/` (writable, reachable). Three breaks: services started by `bin/with-services up` in one Bash call die when that call returns (run them inside one `bin/with-services exec <cmd>` call instead); the short-path fallback under `/tmp/spore-svc-<hash>/` is not writable (`allow_write` needs the exact directory, it has to exist before the call); crm-gateway's RabbitMQ listens on a per-slug TCP port, which a later call cannot reach from its own network namespace. Services started outside the sandbox (an operator shell) are reachable only over unix sockets.
- **Private gems** from `rubygems.pkg.github.com` (both projects' Gemfiles): add `allow_domains = ["rubygems.pkg.github.com"]`. If the credentials come from `spore-with-secrets`, see the gap above.
- **yarn / npm** (marketer): the public registries are in the floor. Yarn Berry's global cache (`~/.yarn/berry`) needs `allow_write = ["~/.yarn"]`; downloads that bypass the registry (Cypress binary, browsers) need their hosts in `allow_domains`.
- **bundler**: `rubygems.org` is in the floor. Gems installed outside the repo need their path in `allow_write` (`~/.gem`, `~/.local/share/gem`, `~/.bundle`).
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
- On 2026-11-25, `--mode pre --gh-project <spore checkout>` passed all 14 checks, `go test ./...` of this repo inside the sandbox included.
- `--gh-project ROOT` adds the `spore gh exec -- git push --dry-run` check with a real App token; the token cache stays in the scratch dir, which is deleted.

Containment checks (secrets must not leak, the victim file must survive, the request must not get a 2xx/3xx): `cat` of `secrets.env`, copy then read of `secrets.env`, Python reading `secrets.env`, Python reading Claude's credentials, Python reading the GitHub token cache, an SSH key and `~/.npmrc`, `dangerouslyDisableSandbox`, `spore gh exec` around a shell, `rm -rf` outside the worktree, `curl` to a non-allowed domain. Must-work checks: `curl` to an allowed domain, `git commit` in a worktree, `nix develop -c true`, `nix develop -c go test ./...`, `spore gh exec -- git push --dry-run`.

To debug the sandbox itself, run a probe with `SRT_DEBUG=1` in the environment: the sandbox runtime then logs every mount, glob expansion and network decision to stderr.
