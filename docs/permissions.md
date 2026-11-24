# Permission guardrails

Every spore pane runs `claude --dangerously-skip-permissions`, so prompt-level "never do X" rules are advice, not enforcement. Two harness layers hold regardless of what the model decides.

## permissions.deny

Claude Code evaluates `permissions.deny` in every mode, including `bypassPermissions`, and before any allow rule or PreToolUse hook result (see https://code.claude.com/docs/en/permissions). spore ships this deny set (`hooks.DefaultDeny()` in `internal/hooks/permissions.go`):

| Rule | Why |
| --- | --- |
| `SendMessage` | Inter-pane messaging goes through the file inbox only. Always emitted. |
| `Read(~/.config/spore/**/secrets.env)` | Keeps layered secrets out of the transcript. |
| `Read(~/.config/spore/**/*.pem)` | Keeps GitHub App private keys out of the transcript. |
| `Bash(git push --force *)`, `Bash(git push -f *)` | Force push with the flag first. |
| `Bash(git push * --force)`, `Bash(git push * --force *)`, `Bash(git push * -f)`, `Bash(git push * -f *)` | Force push with the flag after the remote or refspec. |

What still works:

- `spore-with-secrets <cmd>`: the shim sources the files in a subprocess. Read rules cover Claude's Read tool and file commands it recognizes on the command line (`cat`, `head`, `sed`, ...), not a process that opens the file itself.

The Read rules are a speed bump, not a fence: `grep . secrets.env`, `base64 key.pem`, `source secrets.env; env`, or a copy followed by `cat` still print the contents. They stop the accidental read, not a determined one.
- `~/.config/spore/coordinator-role.md` and other non-secret config: only `secrets.env` and `*.pem` are denied, not the whole directory.
- `git push --force-with-lease`: the trailing ` *` is word-bounded, so `--force` does not prefix-match `--force-with-lease`. The GitHub App mirror (`internal/fleet/rolegithub.go`) pushes from Go anyway, outside the Bash tool.
- Plain pushes, including `spore gh exec -- git push -u origin <branch>`.

Deny rules match each subcommand of a compound command and skip leading `VAR=value` assignments, but they do not see through wrappers such as `spore gh exec --` or `git -C dir`. The PreToolUse guard below covers those.

### Where it is rendered

- Host level: `bootstrap/handover/settings.json`, installed to `/home/spore/.claude/settings.json` by `spore infect` and shipped as `share/spore/settings.json` by the flake's shims package. `handover_settings_test.go` fails if its deny list drifts from `hooks.DefaultDeny()`.
- Project level: `spore hooks settings` (`hooks.SettingsWith`) merges the defaults with the repo root's `spore.toml`:

```toml
[permissions]
# Extra rules, merged after the defaults; duplicates are dropped.
# One-line array only.
deny = ["Bash(terraform apply *)", "Read(./.env)"]
# Drop the guardrail set from this file. SendMessage stays.
default_deny = false
```

Claude Code merges `permissions.deny` across user, project and local settings files instead of overriding. `default_deny = false` therefore only removes the defaults from the project-rendered file; on a host whose `~/.claude/settings.json` carries them (every infected host), they still apply. Edit the host file to opt out there. On a host running the spore-fleet module with `claudeManagedSettings.enable` (the default), the defaults ship in `/etc/claude-code/managed-settings.d/50-spore.json`, which no user or project file can lift; opting out there means `claudeManagedSettings.enable = false` (see `docs/fleet-module.md`).

## PreToolUse guard

`spore hooks pretooluse` (`internal/hooks/pretooluse.go`) runs on every Bash call from the host settings, next to `block-bg-bash.pl`. The two do not overlap: `block-bg-bash.pl` rejects `run_in_background`, the Go guard rejects command text. It denies:

- `sudo` at the start of a line.
- `rm -rf /`.
- `git push` with `--force`, `-f` or a short-flag cluster holding `f` (`-fu`) anywhere before the next `|`, `;` or `&`, including behind git options (`git -C dir`, quoted paths, `--no-pager`) and wrappers. `--force-with-lease` passes. The pattern matches inside quoted text so `bash -c "git push -f"` is caught; a command that only mentions a force push (`rg "git push -f" docs/`) is denied too.

The role loop is unaffected: role panes push through spore commands, which call git from Go, not through the Bash tool.

## Downstream impact

Consumers (marketer, crm-gateway) pick up both layers on their next settings render: the project file on the next `spore hooks settings` run, the host file when `/home/spore/.claude/settings.json` is reinstalled from `share/spore/settings.json` (a fresh `spore infect`, or by hand on an existing host; the nix module does not manage that file, see `docs/host-nix-snippet.md`). A running session reads settings at start, so panes pick the rules up on their next spawn or respawn. A workflow that relied on an agent running a raw force push or reading `secrets.env` with the Read tool now gets a deny; route it through the operator or `spore-with-secrets`.
