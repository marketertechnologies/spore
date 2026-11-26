---
name: secrets-profile
description: Add, widen or remove a secrets broker profile for the operator, so agents can use a new credential (database, API token, CLI) without ever reading it. Use when the operator runs `/secrets-profile`, or asks to "give agents access to X", "add a broker profile", "let the coordinator call the Y API", "add a secret to the broker". The agent designs the profile and writes a root apply script; the operator only types the secret value and runs the script.
---

# secrets-profile

Turn "agents should be able to do X with credential Y" into a broker profile (`services.spore-fleet.secrets.profiles`, see `docs/secrets.md`). The operator speaks in plain words; you do the low-level work. The operator does exactly two things: put the value in `secrets.env` and run one root script. Never ask for, accept, echo or log a secret value. If the operator pastes one into chat, tell them to rotate it.

## 1. Pin down the request

Ask only what you cannot infer, in one message:

- Which service, and what the agents need from it (read a list, query a DB, fetch one endpoint). Default to read-only.
- Which project's agents use it. This is the profile's `project`: the basename of that project's main checkout, the same name as its directory under `/var/lib/spore-secrets/secrets/`. Who reaches the profile is in "Who reaches it" below; say it to the operator if the request implies another project's agents.
- The name of the variable in `secrets.env` (for example `FOO_API_TOKEN`). Suggest one; never ask for the value.

Widening or removing an existing profile follows the same steps on the existing entry.

### Who reaches it

The broker scopes by the project and task the caller claims from its environment (`SPORE_PROJECT_ROOT`, `SPORE_TASK_SLUG`):

- Coordinators, workers and role-loop agents of the profile's `project`: yes. A coordinator pane carries `SPORE_TASK_SLUG=coordinator`, so it is scoped like a worker of its project.
- Any agent of another project, coordinator included: no, unless a pilot grants it (step 7).
- A plain shell with no `SPORE_TASK_SLUG`, pilots and root: every profile.

## 2. Read the current host config

- Find the file that sets the broker: `grep -rlE 'spore-fleet|secrets\.profiles|profiles\.' /etc/nixos --include='*.nix'`, then read each hit in full. Note `path` (packages on the broker's PATH), `projects`, the existing `profiles`, and any `let` helpers the file defines for building commands, to reuse.
- `/etc/spore/secrets-broker.json` is the rendered config the running broker loaded (names only, no values). Compare it with the source if they disagree.
- Find the flake attribute the host rebuilds with (`nixosConfigurations.<name>` in `/etc/nixos/flake.nix`, or project memory). Do not assume the hostname.
- `spore secrets list` shows what this caller already reaches.

## 3. Design the narrowest profile

Follow "Writing safe commands" in `docs/secrets.md`. The `args` regexps are the security boundary: the broker hides the value, but cannot make a loose command safe.

Schema per profile: `project`, `description`, `vars.<NAME> = "<secrets.env key>"`, and `commands.<name> = { argv; args; variadic; stdin; timeout; }`. Rules the broker checks at start:

- `project` must be a project the host declares (`services.spore-fleet.projects` or `services.spore-fleet.secrets.projects`). An unknown project, a bad name or regexp, or a `${VAR}` not in `vars` fails validation and the broker does not start, which breaks every broker call on the host (gh, push, other profiles). Step 4 catches this before the operator runs anything.
- Profile and command names match `^[A-Za-z0-9][A-Za-z0-9._-]*$`. `argv[0]` is a literal program; `${NAME}` in `argv` expands only from the profile's `vars`.
- Each `args` entry is an anchored regexp for one caller argument; no `args` means no caller arguments. `timeout` is a Go duration up to `10m` (default `60s`).

Design:

- One command per action, a fixed `argv`, and one constrained trailing argument. Avoid `variadic` free text.
- HTTP: `curl -sS --fail-with-body`, the credential in a header built from `${VAR}`, and a URL pattern on one host and path prefix (`https://api\\.example\\.com/v1/[A-Za-z0-9_./?=&%:+-]*`). Never allow `-K`, `-o`, `-d @file` or `--config` through `args`. Use `stdin = true` for a request body rather than an argument.
- SQL: a read-only role or replica in the URL; `args = [ "-c" "[^\\\\]*" ]` (no psql backslash meta-commands); a `timeout`.
- A program not yet on the broker's PATH: add its package to `path`.
- Name it `<project>-<service>[-<variant>]` and give it a `description` (shown by `spore secrets list`).
- The broker's own network is not limited by the agent sandbox, so no `allow_domains` change is needed for broker calls.

Show the operator the profile and say in one line what it allows and what it refuses. Wait for their OK.

## 4. Write and check the change

- Write the new config file and an apply script under `~/.local/state/spore/<project>/broker-profiles/<profile>/` (`new.nix`, `apply.sh`). You cannot write `/etc/nixos` or `/var/lib/spore-secrets`.
- `apply.sh` (run as root) must:
  1. `set -euo pipefail`. Copy the current file to `/root/spore-broker-backup-<timestamp>/` and print the one-line rollback (copy back, rebuild).
  2. Install `new.nix` over the host file with the same mode and owner.
  3. Stop with a message if the variable is still missing from `/var/lib/spore-secrets/secrets/<project>/secrets.env` (or the global `secrets.env` for a shared key). Use `grep -q '^NAME='` and never print the line.
  4. Reset that file to `spore-secrets:spore-pilots` mode 0640 (`chown`, `chmod`): `sudoedit` keeps both on an existing file, but creates a new one as root.
  5. `nixos-rebuild switch --flake /etc/nixos#<attr>`.
- Before handing it over, check the change on a copy: `cp -r /etc/nixos "$TMPDIR/nixos-check"`, apply `new.nix` there, then `nix-instantiate --parse` the file and, if the flake evaluates from the copy, `nix eval "$TMPDIR/nixos-check#nixosConfigurations.<attr>.config.services.spore-fleet.secrets.profiles" --apply builtins.attrNames`. When it evaluates, also build the rendered config (`nix build --no-link --print-out-paths "$TMPDIR/nixos-check#nixosConfigurations.<attr>.config.environment.etc.\"spore/secrets-broker.json\".source"`) and confirm the new profile's `project` is a key of its `projects`. Fix anything that fails before the operator sees it.

## 5. Hand over the two operator steps

Give exactly these, with `<project>` and the script's absolute path filled in, to paste into their own shell (not with `!`: they need sudo, which the agent's session does not have):

```
sudo install -d -o spore-secrets -g spore-pilots -m 0750 /var/lib/spore-secrets/secrets/<project> && sudoedit /var/lib/spore-secrets/secrets/<project>/secrets.env    # add: NAME=<value>
sudo bash /absolute/path/to/apply.sh
```

The `install -d` is a no-op when the directory exists. A key shared by every project goes in `/var/lib/spore-secrets/secrets/secrets.env` instead (project files win over it).

## 6. Verify yourself

The rebuild restarts the broker when its rendered config changes (`restartTriggers`), and the broker reads `secrets.env` on every call, so the new profile and value are live as soon as `apply.sh` finishes. After the operator says it ran:

- `grep -c '"<profile>"' /etc/spore/secrets-broker.json` shows the rebuild switched. If it is 0, the rebuild did not apply: read the operator's output before anything else.
- `spore secrets list` from a pane of the profile's project shows the profile and its commands. If the config has it but the list does not, check the caller's claim (step 1, "Who reaches it") before suspecting the broker. Only if the broker still serves the old config does the operator run `sudo systemctl restart spore-secrets.service`.
- Make one cheap, harmless call through the profile and check the output: the expected data, and the value redacted wherever it would have been echoed. `<KEY> is not set in the <project> secrets` means the `sudoedit` step went to the wrong file or key name.
- Make one call that must be refused (another host, a forbidden flag) and quote the refusal.
- Report to the operator in two or three lines: profile name, what works, what is refused.

## 7. Related levers

- An agent of another project, temporarily: `sudo spore secrets grant <task-slug> <profile> --for 2h --project <that agent's project>` (at most 7 days). For another project's coordinator the slug is `coordinator`. `spore secrets grants` lists live grants.
- A new website for the agents' own commands (not broker calls): `[sandbox] allow_domains` in that project's `spore.toml`, then recreate the coordinator and its workers.
- New branch names the broker may push: `services.spore-fleet.secrets.projects.<name>.pushBranch` in the same host file, then rebuild.
- New `gh` subcommands or API paths: those are in spore's code (`checkGHArgs` in `internal/secrets/github.go`), so they need a spore PR, not a profile.
