# spore-fleet NixOS module

Once a project reaches `worker-fleet-ready`, a downstream NixOS host
can autostart the fleet reconciler by importing
`nixosModules.spore-fleet` from this flake.

The module declares one systemd-user oneshot per project, each driven
by:

- a 60-second timer;
- a path watch on that project's `tasks/` directory;
- a path watch on the host-wide kill-switch flag at
  `~/.local/state/spore/fleet-enabled` (a flip there triggers every
  project's reconciler).

This keeps `spore fleet enable` and new active tasks responsive even
when the timer has not ticked yet. home-manager wiring for the target
user is assumed.

## Multiple projects on one host

Set `services.spore-fleet.projects` to an attrset (`name → { path }`)
to reconcile multiple projects under the same `user`. Each entry
generates its own `spore-fleet-reconcile-<name>.service` and timer,
so tasks/, worktrees, and the per-project tmux session prefix
(`spore/<name>/...`) stay isolated.

The single-project shorthand `projectRoot = "..."` is kept for
backward compatibility; new consumers should use `projects` directly.

## Example

```nix
{
  inputs = {
    spore.url = "github:versality/spore";
    home-manager.url = "github:nix-community/home-manager";
  };

  outputs = { self, nixpkgs, spore, home-manager }: {
    nixosConfigurations.worker = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        spore.nixosModules.spore-fleet
        home-manager.nixosModules.home-manager
        ({ ... }: {
          users.users.spore = {
            isNormalUser = true;
            linger = true;
          };

          home-manager.users.spore.home.stateVersion = "25.11";

          services.spore-fleet = {
            enable = true;
            user = "spore";
            projects = {
              project.path = "/home/spore/project";
              # extra-project.path = "/home/spore/extra-project";
            };
            maxWorkers = 6;
          };
        })
      ];
    };
  };
}
```

`package` and `claudeCodePackage` default to this flake's outputs.
Override either option to pin a specific build.

Runners spawn `claude` (claude-code), which manages its own credential
lifecycle inside the client. The module deliberately exposes no
Anthropic API key slot.

`credentialFiles` is for non-claude secrets the runners may need, such
as MCP server keys or git-push tokens. It wires them through systemd
`LoadCredential=` so values never enter Nix evaluation or the
`/nix/store`.

## Skills

With `services.spore-fleet.enable`, home-manager links each skill the
spore binary embeds (`bootstrap/skills/`: diagram, select-agent,
spore-bootstrap) into the operator user's scopes, refreshed on every
rebuild:

- `~/.claude/skills/<name>`: Claude Code personal scope.
- `~/.agents/skills/<name>`: Codex user scope. Codex resolves it from
  `$HOME`, not `CODEX_HOME`, so coordinators on a per-session
  `CODEX_HOME` see it with no shim change.

The bodies are rendered at build time by `spore install --dest` from
`services.spore-fleet.package`. Each skill is a directory symlink into
the store: Codex 0.162 skips file symlinks during discovery, and
per-name links leave unmanaged siblings (`~/.claude/skills/synced`,
`~/.codex/skills/.system`) alone. Nothing is written into project
checkouts.

`spore bootstrap` (repo-mapped stage) skips its per-project skill
install when the host already provides every embedded skill: each
name has a `SKILL.md` under both `~/.claude/skills/` and
`~/.agents/skills/` (`install.HostProvides`). A partial host set, or
a host without the module, keeps the project install into
`.claude/skills` and `.agents/skills`.

When a project also ships a skill of the same name:

- Claude Code runs the personal copy: personal beats project
  (code.claude.com/docs/en/skills, "Resolve skills that share a
  name"). marketer and publishing commit older diagram and
  spore-bootstrap copies under `.claude/skills`; the host-wide copy
  wins there.
- Codex reads project skills from `.agents/skills` only, which those
  consumers do not ship, so they see one copy. On a real collision
  Codex 0.162.1 keeps both; it dedupes by path only
  (`ext/skills/src/loader/host_merge.rs`). The catalog lists the repo
  copy first, and a plain `$name` mention is ambiguous, so it selects
  neither (`skills/src/selection.rs`). The bootstrap skip above keeps
  new consumers out of that state.

## Claude Code managed settings

`services.spore-fleet.claudeManagedSettings.enable` (default true, active with `services.spore-fleet.enable`) writes `/etc/claude-code/managed-settings.d/50-spore.json`. Claude Code merges `managed-settings.json` and then every `*.json` in `managed-settings.d/` alphabetically, and managed settings sit above user and project settings (code.claude.com/docs/en/settings). The drop-in carries:

- the host hooks: Notification `spore hooks notify-coordinator`, PreToolUse (`Bash`) `block-bg-bash.pl` and `spore hooks pretooluse` (force-push guard), SessionStart `load-state-md.pl`, and the five Stop hooks (`coordinator token-monitor`, `worker token-monitor`, `fleet replenish-hook`, the self-gated `hooks engineer-check`, `hooks watch-inbox`);
- `permissions.deny`: the default set from `hooks.DefaultDeny` (`SendMessage`, reads of `~/.config/spore/**/secrets.env`, `**/*.pem` and `~/.claude/.credentials.json`, edits of that credentials file, and the `git push` force rules). A project's `spore.toml` `[permissions] default_deny = false` cannot lift these: managed rules win over project settings.

Commands run through the system profile: `/run/current-system/sw/bin/spore` and `/run/current-system/sw/share/spore/hooks/`. The option adds `services.spore-fleet.package` to `environment.systemPackages` and links `/share/spore/hooks` from `shimsPackage`, so every rebuild refreshes them. Store paths are avoided on purpose: Claude Code snapshots hooks at session start, and a coordinator that outlives a rebuild plus `nix-collect-garbage` would call deleted paths. Nothing under `~/.claude/hooks/` is read any more.

Scope: the drop-in applies to every Claude Code session of every account on the host, and user or project settings cannot override it. The watch-inbox, token-monitor, replenish, notify-coordinator and load-state-md hooks no-op outside spore sessions, but the `block-bg-bash.pl` guard (denies `Bash` `run_in_background`), the force-push guard and the deny rules hold for everyone. Set `claudeManagedSettings.enable = false` on hosts shared with non-spore Claude Code users who need either.

The JSON comes from `spore hooks settings --managed` (`internal/hooks/hostsettings.go`, `HostEvents`). That is the one source: `bootstrap/handover/settings.json` (the file `spore infect` copies once) is pinned to it by `TestHandoverSettingsMatchHostEvents`, and the Codex Stop set shares the same list.

Hooks from every scope run; managed hooks do not replace user or project ones, and only byte-identical handlers are deduplicated (code.claude.com/docs/en/hooks). So the module also prunes the old copies:

- Migration `004-managed-settings-prune.sh` runs on the next `nixos-rebuild switch` (activation now orders `spore-migrate` after `etc`). It backs up `~/.claude/settings.json` to `settings.json.spore-bak-<stamp>`, then removes every hook whose event, group matcher and command match one in the drop-in (command by program base name plus arguments, so `/home/spore/.claude/hooks/load-state-md.pl` and a bare `spore hooks notify-coordinator` match their store counterparts; a hook under a different matcher, such as `Bash|Edit`, stays) and the deny rules the drop-in carries. Everything else stays. A symlinked `settings.json` (for example from home-manager `programs.claude-code`) is left alone with a message; prune its source instead, since its owner would restore the entries and a replaced link aborts its next switch. Keys are rewritten in sorted order. Without the drop-in it changes nothing; run `spore hooks prune-settings --file ~/.claude/settings.json` by hand if you enable the option later.
- Project `.claude/settings.json` files are not touched by the migration. Drop their duplicated Stop hooks with `spore hooks prune-settings --file .claude/settings.json` and commit the result; project-specific hooks stay. `spore hooks settings` (stdin mode) also leaves out any binding the drop-in already provides.

Stale `~/.claude/hooks/*.pl` copies become unreferenced; delete them once the pruned file looks right.

Set `claudeManagedSettings.enable = false` to keep managing hooks in `~/.claude/settings.json`.

### spore plugin

The repo root is a Claude Code plugin marketplace (`.claude-plugin/marketplace.json`) holding one plugin, `spore` (`.claude-plugin/plugin.json`), whose skills are `bootstrap/skills/` (no copy). Outside NixOS:

```
/plugin marketplace add marketertechnologies/spore
/plugin install spore@spore
```

Plugin skills are namespaced: `/spore:diagram`, `/spore:select-agent`, `/spore:spore-bootstrap`.

`claudeManagedSettings.plugin.enable` (default false) builds a store copy of the marketplace (manifests from the repo, skills from `spore install --dest`), registers it in the drop-in under `extraKnownMarketplaces.spore` as a `directory` source and sets `enabledPlugins."spore@spore" = true`. It is off by default because the namespaced names differ from the bare `/select-agent` and `/spore-bootstrap` that roles and docs use, and the home-manager links in `~/.claude/skills` stay regardless (Codex needs `~/.agents/skills`). With both on, each skill is listed twice.

## Deploying

The agent has no sudo. After `nix flake update spore`, the operator runs `sudo nixos-rebuild switch`. Check afterwards:

```
cat /etc/claude-code/managed-settings.d/50-spore.json
grep 004-managed-settings-prune ~/.local/state/spore/migrations.applied
ls ~/.claude/settings.json.spore-bak-*
```

In a Claude Code session, `/status` lists the managed source as `(file + drop-ins)` or `(drop-ins)`, and `/hooks` shows each spore hook once.

## tmux baseline

`services.spore-fleet.tmux` prepends a block to `/etc/tmux.conf`:

- `history-limit` from `tmux.historyLimit` (default 250000; tmux's
  own 2000 loses long agent output);
- `mouse` from `tmux.mouse` (default on: wheel enters copy-mode);
- `allow-passthrough`, `extended-keys`, and the `xterm*:extkeys`
  terminal feature, for the Claude Code TUI;
- `detach-on-destroy off`: a killed session moves its clients to
  another session instead of detaching them. `spore-attach` execs
  the tmux client straight under sshd, so a detach closes the SSH
  connection;
- `S-PageUp` enters copy-mode a page up without the prefix, and the
  wheel scrolls a half-page in copy-mode;
- `default-shell` when `tmux.defaultShell` is set (needed when the
  operator user's login shell is `spore-attach`);
- `tmux.extraConfig`, verbatim.

`tmux.enable` defaults to `services.spore-fleet.enable`. Set it
explicitly to get the baseline on a host that does not run the fleet
yet; `bootstrap/flake/` does this for `spore infect` targets.

The block is merged with `lib.mkBefore`, so a host's own
`environment.etc."tmux.conf".text` lines render after it and win.
`programs.tmux.historyLimit` is seeded at `mkDefault` so enabling the
stock NixOS tmux module does not reset scrollback to 2000.

The full option reference lives in `nixosModules/spore-fleet.nix`.

## Horizontal Scale

Capacity scales additively by enabling the module on multiple hosts
that all see the same project tree, either through a shared filesystem
or through per-host checkouts of one branch.

Each reconciler picks up active tasks it notices first.
`services.spore-fleet.hostId` defaults to `networking.hostName` and
surfaces in `SPORE_HOST_ID` so logs and status displays can identify a
host.

The kill-switch flag is per-host and per-user. Pausing one machine
does not pause another.

There is no cross-host lock layer in v0. Races on `tasks/<slug>.md`
frontmatter are tolerated by Spore's file-based communication shape,
not arbitrated.
