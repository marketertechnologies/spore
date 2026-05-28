# Pilot shell access for remote IDE tooling

Notes on extending the spore harness host with per-pilot Linux
accounts so whitelisted operators can drive remote IDE tooling
(Emacs TRAMP, VSCode Remote, etc.) against `/home/spore/`, alongside
the existing `spore` forced-command tmux flow.

## Goal

Each whitelisted SSH key already has a forced-command entry on the
`spore` user that drops the operator into a private tmux pilot
session. That path is interactive-only: it does not give a remote
filesystem, so IDEs that talk over SSH (TRAMP, VSCode Remote, JetBrains
Gateway) cannot browse the projects living under `/home/spore/`.

We wanted a second login path, gated on the same key set: SSH in as
the operator's own username, land in a plain bash shell, and be able
to read (not write) the spore home tree.

## Shape

Driven from the host's `local.nix` so the pilot list stays a single
source of truth.

- A `pilots` list of `{name, key}` records drives both:
  - the existing forced-command entries on the `spore` user (one per
    pilot, `command="spore-attach pilot <name>"`),
  - one normal `users.users.<name>` per pilot. `isNormalUser`, bash
    login, no sudo, no wheel, own `authorized_keys` carrying the
    pilot's key with no forced command.
- A `pilots` group; every pilot account is a supplementary member.
- `users.users.spore.homeMode = "0750"` plus a system-activation
  script that runs `setfacl -R -m g:pilots:rX /home/spore` and a
  default ACL (`-dR`) on the same path. The default ACL means files
  written later by the spore user inherit the group-read grant
  without re-running the script.

Same SSH key on both surfaces. The pilot can SSH `spore@host` for the
tmux session or `<name>@host` for a normal shell; sshd picks the
right `authorized_keys` based on the target username.

## Issues encountered

- **Git dubious-ownership check (CVE-2022-24765).** `/home/spore` and
  its repos are owned by the `spore` uid; a pilot running `git` from
  TRAMP sees `fatal: detected dubious ownership` and exits 128. Magit
  silently swallows the non-zero exit and falls back to asking for
  the repo path. Fix: render `~/.gitconfig` per pilot via
  `home-manager`, with `safe.directory = *`. Scoped to the pilot
  accounts, so root and spore keep stock git behaviour.
- **TRAMP `$PATH` on NixOS.** Git lives at
  `/run/current-system/sw/bin/git`, which is not in TRAMP's hardcoded
  `tramp-remote-path`. Each operator needs
  `(add-to-list 'tramp-remote-path 'tramp-own-remote-path)` in their
  Emacs init for TRAMP to inherit the remote login shell's `$PATH`.
  Client-side only; nothing to do on the host.
- **Activation-script ACL cost.** The recursive `setfacl -R` walks
  the entire spore home on every `nixos-rebuild switch`. Idempotent,
  but acceptable only while the tree is small; revisit if /home/spore
  grows past a few GB.

## Usage

TRAMP, from Emacs:

```
C-x C-f /ssh:<name>@<host>:/home/spore/<repo>/ RET
M-x magit-status RET
```

VSCode Remote-SSH, in `~/.ssh/config` on the operator's machine:

```
Host spore-host
  HostName <host>
  User <name>
```

Then `Remote-SSH: Connect to Host... -> spore-host`, and open
`/home/spore/<repo>` as a workspace. Edits will fail (read-only ACL);
the pilot can copy a file into their own home for scratch work or
fall back to the tmux session for anything that needs to mutate
spore-owned state.
