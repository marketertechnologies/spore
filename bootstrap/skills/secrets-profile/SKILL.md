---
name: secrets-profile
description: Add, widen or remove a secrets broker profile for the operator, so agents can use a new credential (database, API token, CLI) without ever reading it. Use when the operator runs `/secrets-profile`, or asks to "give agents access to X", "add a broker profile", "let the coordinator call the Y API", "add a secret to the broker". The agent designs the profile and writes a root apply script; the operator only types the secret value and runs the script.
---

# secrets-profile

Turn "agents should be able to do X with credential Y" into a broker profile (`services.spore-fleet.secrets.profiles`, see `docs/secrets.md`). The operator speaks in plain words; you do the low-level work. The operator does exactly two things: put the value in `secrets.env` and run one root script. Never ask for, accept, echo or log a secret value. If the operator pastes one into chat, tell them to rotate it.

## 1. Pin down the request

Ask only what you cannot infer, in one message:

- Which service, and what the agents need from it (read a list, query a DB, fetch one endpoint). Default to read-only.
- Which project's agents use it (`project = ...`). Coordinators and that project's workers get it. Another project's worker needs a grant (step 7).
- The name of the variable in `secrets.env` (for example `FOO_API_TOKEN`). Suggest one; never ask for the value.

Widening or removing an existing profile follows the same steps on the existing entry.

## 2. Read the current host config

- Find the file that sets the broker: `grep -l 'spore-fleet.secrets' /etc/nixos/*.nix`. Read it in full. Note `path` (programs the broker can run), the existing `profiles`, and any `let` helpers (`psqlSql`, `sentryGet`, ...) to reuse.
- Find the flake attribute the host rebuilds with (`nixosConfigurations.<name>` in `/etc/nixos/flake.nix`, or project memory). Do not assume the hostname.
- `spore secrets list` shows what this caller already reaches.

## 3. Design the narrowest profile

Follow "Writing safe commands" in `docs/secrets.md`. The `args` regexps are the security boundary: the broker hides the value, but cannot make a loose command safe.

- One command per action, a fixed `argv`, and one constrained trailing argument. Avoid `variadic` free text.
- HTTP: `curl -sS --fail-with-body`, the credential in a header built from `${VAR}`, and an anchored URL pattern on one host and path prefix (`https://api\\.example\\.com/v1/[A-Za-z0-9_./?=&%:+-]*`). Never allow `-K`, `-o`, `-d @file` or `--config` through `args`. Use `stdin = true` for a request body rather than an argument.
- SQL: a read-only role or replica in the URL; `args = [ "-c" "[^\\\\]*" ]` (no psql backslash meta-commands); a `timeout`.
- A program not yet in `path`: add its package to `path`.
- Name it `<project>-<service>[-<variant>]` and give it a `description` (shown by `spore secrets list`).
- The broker's own network is not limited by the agent sandbox, so no `allow_domains` change is needed for broker calls.

Show the operator the profile and say in one line what it allows and what it refuses. Wait for their OK.

## 4. Write and check the change

- Write the new config file and an apply script under `~/.local/state/spore/<project>/broker-profiles/<profile>/` (`new.nix`, `apply.sh`). You cannot write `/etc/nixos` or `/var/lib/spore-secrets`.
- `apply.sh` (run as root) must:
  1. `set -euo pipefail`. Copy the current file to `/root/spore-broker-backup-<timestamp>/` and print the one-line rollback (copy back, rebuild).
  2. Install `new.nix` over the host file with the same mode.
  3. Stop with a message if the variable is still missing from `/var/lib/spore-secrets/secrets/<project>/secrets.env`. Use `grep -q '^NAME='` and never print the line.
  4. `nixos-rebuild switch --flake /etc/nixos#<attr>`.
- Before handing it over, check the change on a copy: `cp -r /etc/nixos "$TMPDIR/nixos-check"`, apply `new.nix` there, then `nix-instantiate --parse` the file and, if the flake evaluates from the copy, `nix eval "$TMPDIR/nixos-check#nixosConfigurations.<attr>.config.services.spore-fleet.secrets.profiles" --apply builtins.attrNames`. Fix anything that fails before the operator sees it.

## 5. Hand over the two operator steps

Give exactly these, ready to paste into their own root shell (not with `!`, because they need root):

```
sudoedit /var/lib/spore-secrets/secrets/<project>/secrets.env    # add: NAME=<value>
bash /home/<user>/.local/state/spore/<project>/broker-profiles/<profile>/apply.sh
```

Keep that file owned by `spore-secrets:spore-pilots` with mode 0640 (`sudoedit` preserves both). A key shared by every project goes in `/var/lib/spore-secrets/secrets/secrets.env` instead.

## 6. Verify yourself

After the operator says it ran (or when `apply.sh`'s rebuild has finished):

- `spore secrets list` from a pane of that project shows the profile and its commands. If it does not, the operator runs `systemctl restart spore-secrets.service` and you list again.
- Make one cheap, harmless call through the profile and check the output: the expected data, and the value redacted wherever it would have been echoed.
- Make one call that must be refused (another host, a forbidden flag) and quote the refusal.
- Report to the operator in two or three lines: profile name, what works, what is refused.

## 7. Related levers

- One worker from another project, temporarily: `sudo spore secrets grant <task-slug> <profile> --for 2h [--project <worker's project>]` (at most 7 days). `spore secrets grants` lists live grants.
- A new website for the agents' own commands (not broker calls): `[sandbox] allow_domains` in that project's `spore.toml`, then recreate the coordinator and its workers.
- New branch names the broker may push: `projects.<name>.pushBranch` in the same host file, then rebuild.
- New `gh` subcommands or API paths: those are in spore's code (`checkGHArgs` in `internal/secrets/github.go`), so they need a spore PR, not a profile.
