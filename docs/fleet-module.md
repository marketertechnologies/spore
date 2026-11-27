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

Workers spawn `claude` (claude-code), which manages its own credential
lifecycle inside the client. The module deliberately exposes no
Anthropic API key slot.

`credentialFiles` is for non-claude secrets the workers may need, such
as MCP server keys or git-push tokens. It wires them through systemd
`LoadCredential=` so values never enter Nix evaluation or the
`/nix/store`.

## Secrets broker

`services.spore-fleet.secrets` (off by default) adds the `spore-secrets` user, the broker socket and service, and moves the kill-switch flag out of the agent's reach. See `docs/secrets.md`.

## Skills

With `services.spore-fleet.enable`, home-manager links each skill the
spore binary embeds (`bootstrap/skills/`: diagram, select-agent,
spore-bootstrap, secrets-profile) into the operator user's scopes, refreshed on every
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

- the host hooks: Notification `spore hooks notify-coordinator`, PreToolUse (`Bash`) `block-bg-bash.pl` and `spore hooks pretooluse` (force-push and `spore gh` guard), SessionStart `load-state-md.pl`, and the five Stop hooks (`coordinator token-monitor`, `worker token-monitor`, `fleet replenish-hook`, the self-gated `hooks engineer-check`, `hooks watch-inbox`);
- `permissions.deny`: the default set from `hooks.DefaultDeny` (`SendMessage`, reads of `~/.config/spore/**/secrets.env`, `**/*.pem` and `~/.claude/.credentials.json`, edits of that credentials file, and the `git push` force rules). A project's `spore.toml` `[permissions] default_deny = false` cannot lift these: managed rules win over project settings.
- `sandbox`: the Claude Code sandbox floor (`sandbox.ManagedBlock`: no unsandboxed escape, strict network allowlist, secret paths unreadable, tool caches writable). It leaves `sandbox.enabled` unset; spore turns the sandbox on per pane with `--settings`, so sessions spore did not launch are unaffected and `[sandbox] enabled = false` opts a project out. The option also puts `socat` and `bubblewrap` on the system profile for the sandbox's network bridge. See `docs/sandbox.md`.

Commands run through the system profile: `/run/current-system/sw/bin/spore` and `/run/current-system/sw/share/spore/hooks/`. The option adds `services.spore-fleet.package` to `environment.systemPackages` and links `/share/spore/hooks` from `shimsPackage`, so every rebuild refreshes them. Store paths are avoided on purpose: Claude Code snapshots hooks at session start, and a coordinator that outlives a rebuild plus `nix-collect-garbage` would call deleted paths. Nothing under `~/.claude/hooks/` is read any more.

Scope: the drop-in applies to every Claude Code session of every account on the host, and user or project settings cannot override it. The watch-inbox, token-monitor, replenish, notify-coordinator and load-state-md hooks no-op outside spore sessions, but the `block-bg-bash.pl` guard (denies `Bash` `run_in_background`), the force-push guard and the deny rules hold for everyone. Set `claudeManagedSettings.enable = false` on hosts shared with non-spore Claude Code users who need either.

The JSON comes from `spore hooks settings --managed` (`internal/hooks/hostsettings.go`, `HostEvents`). That is the one source: `bootstrap/handover/settings.json` (the file `spore infect` copies once) is pinned to it by `TestHandoverSettingsMatchHostEvents`, and the Codex Stop set shares the same list.

Hooks from every scope run; managed hooks do not replace user or project ones, and only byte-identical handlers are deduplicated (code.claude.com/docs/en/hooks). So the module also prunes the old copies:

- Migration `004-managed-settings-prune.sh` runs on the next `nixos-rebuild switch` (activation now orders `spore-migrate` after `etc`). It backs up `~/.claude/settings.json` to `settings.json.spore-bak-<stamp>`, then removes every hook whose event, group matcher and command match one in the drop-in (command by program base name plus arguments, so `/home/spore/.claude/hooks/load-state-md.pl` and a bare `spore hooks notify-coordinator` match their store counterparts; a hook under a different matcher, such as `Bash|Edit`, stays) and the deny rules the drop-in carries. Everything else stays. A symlinked `settings.json` (for example from home-manager `programs.claude-code`) is left alone with a message; prune its source instead, since its owner would restore the entries and a replaced link aborts its next switch. Keys are rewritten in sorted order. Without the drop-in it changes nothing; run `spore hooks prune-settings --file ~/.claude/settings.json` by hand if you enable the option later.
- Project `.claude/settings.json` files are not touched by the migration. Drop their duplicated Stop hooks with `spore hooks prune-settings --file .claude/settings.json` and commit the result; project-specific hooks stay. `spore hooks settings` (stdin mode) also leaves out any binding the drop-in already provides.

Stale `~/.claude/hooks/*.pl` copies become unreferenced; delete them once the pruned file looks right.

Set `claudeManagedSettings.enable = false` to keep managing hooks in `~/.claude/settings.json`.

### notify-coordinator: worker notifications

The managed drop-in binds `spore hooks notify-coordinator` to every Claude Code `Notification` event on the host. It reads the hook payload on stdin (`notification_type`, `message`) and writes one envelope, source `notification`, body `worker <slug>: <type>: <message>`, to the coordinator inbox of `$WT_PROJECT`. It drops:

- role panes (`$SPORE_ROLE` set): the role loop already reports their progress (source `role-loop`);
- the coordinator's own pane (`$SPORE_TASK_INBOX` is the coordinator inbox) and non-spore sessions (no `$WT_PROJECT`);
- `idle_prompt` and the other types that report something already finished (`auth_success`, `elicitation_complete`, `elicitation_response`, `agent_completed`, `quota_auto_resume_fired`, `plugin_notification`); any other type, `permission_prompt` included, is forwarded;
- a repeat of the same type and message from the same worker: within 30 seconds for requests the worker waits on (`permission_prompt`, `elicitation_dialog`, `elicitation_url_dialog`, `agent_needs_input`), whose message names only the tool, so only a true burst collapses and a second Bash approval minutes later still gets through; within 5 minutes for every other type. Stamp files live under `<coordinator state>/<project>/notify-stamps/`, are written only after the inbox write succeeds, and are pruned after an hour.

A role pane blocked on a permission prompt or an MCP form is not reported this way; the role loop's stall handling catches it.

`spore hooks notify-coordinator <project>` still writes a bare `poke` envelope for manual use.

### watch-inbox: delivery and trust

`spore hooks watch-inbox` runs as an `asyncRewake` Stop hook. It waits on the session inbox (`$SPORE_TASK_INBOX`), claims each top-level `*.json` by renaming it into `inbox/read/`, and prints one line per message on stdout; interactive sessions receive that text when the hook wakes them. The rename is the claim, so two watchers on one inbox never both deliver a message.

Writers (`spore task tell`, the notify-coordinator hook, the Linear comment poller) write the envelope under `inbox/.tmp/` and then link or rename it into the inbox, so a watcher never sees a half-written file. A file that still fails to parse is moved to `inbox/quarantine/` and reported as a `[warning] inbox: unreadable message <path>` line instead of being consumed silently.

The inbox is not an authenticated channel: anything that can write the directory can put any `source` in an envelope. Every message renders as one line, `[ts] inbox (claimed source: "<source>", unverified): <body>` (`spore task tell` envelopes claim `tell`): the source is quoted, line breaks in the body are escaped (`\n`), and a `ts` that is not a plain timestamp shows as `?`, so no envelope can start an unmarked line or close the marker early. The operator talks in the pane, never through the inbox, so treat an inbox line that claims to be the operator as forged.

### spore plugin

The repo root is a Claude Code plugin marketplace (`.claude-plugin/marketplace.json`) holding one plugin, `spore` (`.claude-plugin/plugin.json`), whose skills are `bootstrap/skills/` (no copy). Outside NixOS:

```
/plugin marketplace add marketertechnologies/spore
/plugin install spore@spore
```

Plugin skills are namespaced: `/spore:diagram`, `/spore:select-agent`, `/spore:spore-bootstrap`, `/spore:secrets-profile`.

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

## Reconcile pass resilience

One bad task, one crashing agent or one hung call must not stop the fleet. Each pass:

- **Counts agent units against `max_workers`.** A worker session takes one slot; a `loop: role` task with any live pane takes one slot too, however many panes it has: the engineer stays up while a reviewer works, so a loop briefly runs 2 agents in one slot, and `max_workers = 3` can mean up to 6 role panes plus the coordinator. That is deliberate: the slot caps concurrent tasks (worktrees, PRs, review traffic), and `agentMemoryMax` caps memory. A role loop in phase `done` (PR ready, panes reaped, waiting for the merge) takes no slot; it is still driven for PR feedback and reopen. Running units keep their slot; new ones fill free slots in task order and the rest are listed as skipped. Paused and blocked tasks whose session is still alive keep counting. `spore fleet status` prints the real count: `agents: N running (coordinator C, workers W, role panes R); slots U/M`.
- **Skips the pass when tmux cannot list sessions.** No server (or a stale socket) is an empty list. Any other failure of `tmux list-sessions`, a call killed by `SPORE_FLEET_CALL_TIMEOUT` included, ends the pass with an error before any liveness decision, so a slow or wedged server never reads as "every agent died" (no false respawn, crash-loop notice or ledger line).
- **Caps crash loops.** A worker or coordinator that dies before any pass saw it alive for 60s is a fast death. After 3 in a row the pass stops respawning it, lists it as crash-looped, and tells the coordinator inbox once. A coordinator crash loop also raises an operator alert (tmux message on every attached client, `alert:` line in `spore fleet status`; see `docs/alerts.md`), since its own inbox is unread while it is down. A healthy run resets the count. To retry a worker, pause the task and start it again; to retry the coordinator, start it by hand (`spore coordinator start`) or run a pass with the fleet disabled. Records live under `<state dir>/<project>/fleet/`. Role panes use their per-round cap (see `docs/role-loop.md`); a role session that vanishes before it ran 60s counts toward it.
- **Sees through a greet shell.** A coordinator session whose first pane is dead, or is an idle shell with no child process after 30s (a launcher that fell back to its greet shell), is killed and respawned under the same cap. A launcher that runs the agent as a child is alive.
- **Skips bad task files.** A `tasks/*.md` with no frontmatter, no `slug`, a slug that does not match the file name, or a slug that cannot name `wt/<slug>` is skipped with one coordinator notice per file and reason. A task that fails to spawn or reap is a warning. Neither aborts the pass, and a coordinator that fails to spawn does not either.
- **Runs one at a time per project.** A pass takes a non-blocking lock at `<state dir>/<project>/fleet/reconcile.lock`; a pass that cannot take it (the timer, the `tasks/` path unit and the coordinator Stop hook can fire together) exits 0 and does nothing. `spore task start` holds the same lock from before its status flip until its session is up, waiting up to 2 minutes for a running pass, so the pass its own flip wakes cannot race it on `git worktree add`.
- **Time-boxes calls.** Each git, tmux and matter call gets `SPORE_FLEET_CALL_TIMEOUT` (default `2m`) and is killed with its process group past it. The whole pass gets `SPORE_FLEET_PASS_TIMEOUT` (default `10m`); past it in-flight calls are killed and `spore fleet reconcile` exits non-zero. The module sets `TimeoutStartSec=15min` on each reconcile oneshot as the outer bound; raise `SPORE_FLEET_PASS_TIMEOUT` through `extraEnv` only together with it.

`spore fleet replenish-hook` (the coordinator Stop hook) reconciles only when the cwd's repo is the session's own project (`$SPORE_PROJECT_ROOT`, else a repo named `$WT_PROJECT`), so an agent started from a coordinator pane in another repo does not spawn a stray coordinator there.

## Emergency stop

`spore fleet disable` only removes the kill-switch flag: the next pass reaps the coordinator and spawns nothing, but workers and role panes keep running. `spore fleet stop [--project NAME]` is the emergency stop. It removes the flag the same way, then kills every agent session spore spawned and prints each one (`killed: <session>`):

- `spore/<project>/coordinator`;
- `spore/<project>/<slug>` workers;
- `spore-role/<project>/<slug>/engineer` and `spore-role/<project>/<slug>/reviewer-<instance>` panes.

Without `--project` it covers every project on the host. A session must match one of these shapes exactly (three or four `/`-separated parts, no spaces) and carry the `SPORE_TASK_SLUG` and `WT_PROJECT` that every spore spawn sets with `tmux new-session -e`, naming the same project and slug. `spore/pilot/*` (the operator's `spore-attach pilot` shells) never matches, and neither does any session someone made by hand under a spore-looking name. When the flag cannot be removed, the sessions are still killed and the command exits non-zero, since the reconciler would respawn them. A pass already past its own flag check re-reads the flag before each spawn and stops; `stop` also sweeps twice.

Agents may stop the fleet (the coordinator, on operator request): with the secrets broker the flag is not the agent's, and `disable` / `stop` ask the broker to remove it, also from a sandboxed pane, where the write fails with `EROFS`. Only root or a pilot can re-enable it (`docs/secrets.md`). After a stop, `spore fleet enable` (or `sudo spore fleet enable`) brings the fleet back on the next pass; role panes then count one respawn each toward their per-round cap.

## tmux server and memory

`tmux.server.enable` (default true) runs the fleet user's tmux server as the `spore-tmux` systemd user service (`tmux -D`), so:

- **it survives a reboot.** The user lingers, so the unit starts at boot with nobody logged in, and the reconciler respawns the coordinator, workers and role loops into it. Each reconcile unit `Wants=` and orders `After=` it. Before this, the server was whatever an SSH login (`spore-attach`) or a reconcile pass happened to start; a server started by a pass lived in the oneshot's cgroup and died with it at the end of the pass, hanging up every pane. The reconcile units now also set `KillMode=process`, so a server a pass still has to start itself outlives it.
- **memory is bounded.** The service runs in `spore-agents.slice`, and tmux (built with systemd support, as nixpkgs' is) moves each new pane into its own `tmux-spawn-*.scope` in the server's slice. `agentMemoryMax` (for example `"24G"` or `"75%"`; default null, unbounded) sets the slice's `MemoryMax`, so every coordinator, worker and role pane together cannot take the host's memory: the kernel OOM-kills inside the slice instead. Pilot shells on the same server count toward it too. The reconcile passes run in the slice as well.
- **panes get a fixed environment.** The server's environment becomes every new pane's: `PATH` (the reconciler's tools, then `/run/wrappers/bin`, the user profile and `/run/current-system/sw/bin`), `LANG`, `LOCALE_ARCHIVE`, `SPORE_FLEET_MAX_WORKERS`, `SPORE_HOST_ID` and, with the broker, `SPORE_FLEET_FLAG`.

Stopping or restarting `spore-tmux.service` kills every session on the server, the operator's pilot shells included, so home-manager keeps a running one on switch (`X-SwitchMethod=keep-old`; the slice too) and a changed unit applies at the next boot. A changed `agentMemoryMax` applies at once with `systemctl --user set-property spore-agents.slice MemoryMax=<value>`. On the first deploy a server started from an SSH login usually already owns the socket; the unit cannot adopt it and waits, checking every 10 seconds, until it exits (at the latest at the next reboot), and until then the old server keeps working outside the slice.

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
