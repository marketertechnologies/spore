{ config, lib, pkgs, ... }:

let
  cfg = config.services.spore-fleet;
  stateRel = ".local/state/spore/fleet-enabled";

  # PATH for the fleet units and the coordinator / worker agent sessions
  # they spawn. claude-code shells out to grep / sed / awk / ripgrep /
  # find on startup and for its search tools; without them on PATH the
  # agent exits before its tmux session settles ("died on spawn"). spore
  # + claude-code + bash + the standard userland is the minimum a spawned
  # agent needs. Workers inherit this via the coordinator's tmux session.
  fleetBinPath = lib.makeBinPath [
    cfg.package
    cfg.claudeCodePackage
    pkgs.bashInteractive
    pkgs.coreutils
    pkgs.gnugrep
    pkgs.gnused
    pkgs.gawk
    pkgs.ripgrep
    pkgs.findutils
    pkgs.which
    pkgs.git
    pkgs.tmux
    # Frequently reached for by hooks, the dogfood role, and ad-hoc
    # operator one-liners. perl was the original SessionStart hook
    # interpreter; jq parses MCP / gh / settings.json output; python3
    # backs ad-hoc data work; go + just are the project's dev shell.
    pkgs.perl
    pkgs.jq
    pkgs.python3
    pkgs.go
    pkgs.just
    # gh: GitHub CLI for issue / PR / API access. curl: HTTP probes
    # against Linear, GitHub, and other matter backends. openssh:
    # ssh + scp for clone / fetch / push from inside the spawned
    # agent shell. All three are coordinator + worker hot paths.
    pkgs.gh
    pkgs.curl
    pkgs.openssh
  ];

  # Render an attrset into a systemd Environment= list, quoting each
  # assignment so values containing spaces survive (an unquoted
  # `Environment=KEY=In Progress` is parsed as KEY=In plus a stray
  # `Progress`; a matter state like Linear's "In Progress" needs the
  # quotes). systemd strips the outer quotes, so quoting space-free
  # values is harmless.
  mkEnvList = attrs: lib.mapAttrsToList (n: v: ''"${n}=${v}"'') attrs;

  # Common preamble: re-exec as cfg.user with a clean systemd-user
  # environment when invoked as root (system.activationScripts and
  # colmena pre/postActivation both run as root). When already running
  # as cfg.user (manual smoke test, or a user-context call), the
  # re-exec is skipped and the body runs in place.
  asUserPreamble = ''
    set -eu
    user='${cfg.user}'
    uid="$(${pkgs.coreutils}/bin/id -u "$user")"
    if [ "$(${pkgs.coreutils}/bin/id -un)" != "$user" ]; then
      home="$(${pkgs.getent}/bin/getent passwd "$user" | ${pkgs.coreutils}/bin/cut -d: -f6)"
      if [ -z "$home" ]; then
        echo "spore-fleet-graceful: cannot resolve home for user '$user'" >&2
        exit 1
      fi
      exec ${pkgs.util-linux}/bin/runuser -u "$user" -- ${pkgs.coreutils}/bin/env -i \
        HOME="$home" \
        USER="$user" \
        LOGNAME="$user" \
        XDG_RUNTIME_DIR="/run/user/$uid" \
        PATH="/run/current-system/sw/bin:/run/wrappers/bin" \
        "$0" "$@"
    fi
  '';

  preScript = pkgs.writeShellScriptBin "spore-fleet-graceful-pre" ''
    ${asUserPreamble}

    project_root='${toString cfg.projectRoot}'
    project="$(${pkgs.coreutils}/bin/basename "$project_root")"
    timeout=${toString cfg.gracefulDeploy.timeout}
    message='${cfg.gracefulDeploy.message}'
    sporecli='${cfg.package}/bin/spore'
    tmuxcli='${pkgs.tmux}/bin/tmux'

    cd "$project_root"

    echo "spore-fleet-graceful: disabling kill-switch" >&2
    "$sporecli" fleet disable || true

    # list_workers prints "session<TAB>slug" for every worker session
    # in this project, regardless of name shape (current wt-emoji or
    # legacy spore-prefixed). `spore fleet list-sessions` does the
    # parsing - the shell never greps tmux names directly.
    list_workers() {
      "$sporecli" fleet list-sessions --project "$project" --kind worker \
        2>/dev/null \
        | ${pkgs.gawk}/bin/awk -F'\t' '{ print $1 "\t" $3 }' || true
    }

    sessions="$(list_workers)"
    if [ -z "$sessions" ]; then
      echo "spore-fleet-graceful: no active workers" >&2
      exit 0
    fi

    while IFS=$'\t' read -r session slug; do
      [ -z "$session" ] && continue
      echo "spore-fleet-graceful: signalling $slug" >&2
      "$sporecli" task tell "$slug" "$message" || true
    done <<< "$sessions"

    deadline=$(( $(${pkgs.coreutils}/bin/date +%s) + timeout ))
    while [ "$(${pkgs.coreutils}/bin/date +%s)" -lt "$deadline" ]; do
      remaining="$(list_workers | ${pkgs.gnugrep}/bin/grep -c '^' || true)"
      if [ "$remaining" = "0" ]; then
        echo "spore-fleet-graceful: workers drained" >&2
        exit 0
      fi
      ${pkgs.coreutils}/bin/sleep 2
    done

    echo "spore-fleet-graceful: timeout (''${timeout}s); killing remaining workers" >&2
    list_workers | while IFS=$'\t' read -r session _; do
      [ -z "$session" ] && continue
      "$tmuxcli" kill-session -t "$session" || true
    done
  '';

  postScript = pkgs.writeShellScriptBin "spore-fleet-graceful-post" ''
    ${asUserPreamble}

    sporecli='${cfg.package}/bin/spore'
    echo "spore-fleet-graceful: re-enabling kill-switch" >&2
    "$sporecli" fleet enable
  '';

  # envSlug folds an attribute name into the SPORE_MATTER_<NAME>__<KEY>
  # shape the matter loader expects: upper-case, with non [A-Z0-9]
  # runes mapped to '_'. Mirrors internal/matter.envKey normalisation.
  envSlug = name:
    let upper = lib.toUpper name; in
    builtins.concatStringsSep "" (
      builtins.map
        (c: if (c >= "A" && c <= "Z") || (c >= "0" && c <= "9") then c else "_")
        (lib.stringToCharacters upper)
    );

  enabledMatters = lib.filterAttrs (_: m: m.enable) cfg.matters;

  # matterEnv flattens enabled matters into
  # SPORE_MATTER_<NAME>__<KEY> entries: one ENABLED=1 marker, one
  # entry per setting, and one per credentialFile (pointing at
  # $CREDENTIALS_DIRECTORY/matter-<name>-<key> via systemd's `%d`
  # specifier). The double underscore between name and key is the
  # matter loader's contract; see internal/matter/config.go envSep.
  matterEnv = lib.foldlAttrs
    (acc: name: m:
      let
        slug = envSlug name;
        prefix = "SPORE_MATTER_${slug}__";
        settingEnv = lib.mapAttrs'
          (k: v: lib.nameValuePair "${prefix}${envSlug k}" (toString v))
          m.settings;
        credEnv = lib.mapAttrs'
          (k: _: lib.nameValuePair
            "${prefix}CREDENTIAL_${envSlug k}"
            "%d/matter-${name}-${k}")
          m.credentialFiles;
      in
      acc // { "${prefix}ENABLED" = "1"; } // settingEnv // credEnv
    )
    { }
    enabledMatters;

  # matterCredentials renames per-matter credentialFiles into the flat
  # LoadCredential namespace using a matter-<name>-<key> prefix so the
  # SPORE_MATTER_<NAME>__CREDENTIAL_<KEY> env vars resolve under
  # $CREDENTIALS_DIRECTORY at runtime.
  matterCredentials = lib.foldlAttrs
    (acc: name: m:
      acc // (lib.mapAttrs'
        (k: path: lib.nameValuePair "matter-${name}-${k}" path)
        m.credentialFiles)
    )
    { }
    enabledMatters;

  matterSubmodule = lib.types.submodule {
    options = {
      enable = lib.mkEnableOption "this matter (rendered as SPORE_MATTER_<NAME>__ENABLED=1)";

      settings = lib.mkOption {
        type = lib.types.attrsOf (lib.types.oneOf [ lib.types.str lib.types.int lib.types.bool ]);
        default = { };
        example = lib.literalExpression ''{ team = "MAR"; ready_state = "Ready"; }'';
        description = ''
          Adapter-specific key-value pairs. Each entry is rendered
          to the unit env as SPORE_MATTER_<NAME>__<KEY>=<value>;
          the matter loader merges these on top of any
          [matter.<name>] section in the project's spore.toml.
        '';
      };

      credentialFiles = lib.mkOption {
        type = lib.types.attrsOf lib.types.path;
        default = { };
        example = lib.literalExpression ''{ api_key = config.age.secrets.linear-api-key.path; }'';
        description = ''
          Per-credential files exposed via systemd LoadCredential
          under the name `matter-<matter>-<key>`. The matter
          adapter receives the resolved path through
          SPORE_MATTER_<NAME>__CREDENTIAL_<KEY>=%d/matter-<matter>-<key>;
          the file itself is dereferenced by systemd at activation
          time, so secrets never enter Nix evaluation or /nix/store.
        '';
      };
    };
  };
in
{
  options.services.spore-fleet = {
    enable = lib.mkEnableOption "spore fleet reconciler (systemd-user)";

    package = lib.mkOption {
      type = lib.types.package;
      defaultText = lib.literalExpression "spore.packages.\${system}.spore";
      description = ''
        Spore CLI package. ExecStart runs `spore fleet reconcile`
        from this package on every timer tick.
      '';
    };

    claudeCodePackage = lib.mkOption {
      type = lib.types.package;
      defaultText = lib.literalExpression "claude-code.packages.\${system}.default";
      description = ''
        claude-code CLI placed on the unit's PATH so workers spawned
        by the reconciler can invoke `claude` directly.
      '';
    };

    shimsPackage = lib.mkOption {
      type = lib.types.package;
      defaultText = lib.literalExpression "spore.packages.\${system}.shims";
      description = ''
        Spore host shims package. The module adds it to
        `environment.systemPackages` and symlinks each shim into
        `/usr/local/bin/spore-*` via a system activation script,
        so the kernel's hard-coded paths (e.g. the respawn-pane
        message from `tokenmonitor.Check` and the entries written
        to `/etc/spore/coordinator.env` by `spore infect`) keep
        resolving. The activation script sweeps any non-symlink
        target left over from the original `install -m 0755` from
        `spore infect`'s `Handoff()`.
      '';
    };

    user = lib.mkOption {
      type = lib.types.str;
      example = "spore";
      description = ''
        User account the reconciler runs under. Required: a user-
        service needs a real account to install under (the module
        does not declare the user; ensure it exists via
        `users.users.<name>` outside this module). home-manager
        wiring for this user is assumed.
      '';
    };

    projectRoot = lib.mkOption {
      type = lib.types.path;
      example = "/home/spore/project";
      description = ''
        Project tree containing tasks/. The reconciler scans
        `''${projectRoot}/tasks` and creates worker worktrees under
        `''${projectRoot}/.worktrees/<slug>`. Must be writable by
        `services.spore-fleet.user`.
      '';
    };

    maxWorkers = lib.mkOption {
      type = lib.types.ints.positive;
      default = 3;
      description = ''
        Concurrency cap. Wired through SPORE_FLEET_MAX_WORKERS so
        an explicit `[fleet] max_workers` in the project's
        spore.toml still wins (matching `spore fleet reconcile`
        precedence: --max-workers > env > spore.toml > built-in
        default).
      '';
    };

    interval = lib.mkOption {
      type = lib.types.str;
      default = "60s";
      description = ''
        Timer interval between reconcile passes. Combined with the
        Path watchers on tasks/ and the kill-switch flag, so
        flipping `spore fleet enable` or committing a new active
        task is responsive even on a slow timer.
      '';
    };

    hostId = lib.mkOption {
      type = lib.types.str;
      default = config.networking.hostName;
      defaultText = lib.literalExpression "config.networking.hostName";
      description = ''
        Free-form identifier surfaced as SPORE_HOST_ID for logs and
        operator-facing chips when more than one host runs a fleet
        against the same project tree. Disambiguation only; spore
        does not coordinate across hosts.
      '';
    };

    evictIdle = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          Wire a sibling systemd-user timer (spore-fleet-evict-idle)
          that periodically flips genuinely idle workers to
          `status: blocked / blocker: auto:idle-no-progress`. Idle
          means: tmux pane inactive for longer than `idleSeconds`,
          inbox drained, and no commit on `wt/<slug>` within the same
          window. Pairs with the reconcile unit but fails
          independently (a broken sweep cannot mask reconcile).
        '';
      };

      interval = lib.mkOption {
        type = lib.types.str;
        default = "2min";
        description = ''
          Timer interval between eviction sweeps. The sweep is
          O(active tasks) and finishes in well under a second; a
          slower cadence than reconcile is fine because the soak
          window itself is much larger than the tick.
        '';
      };

      idleSeconds = lib.mkOption {
        type = lib.types.ints.unsigned;
        default = 600;
        description = ''
          Soak window in seconds. A worker must be inactive for at
          least this long across all three signals (tmux idle, no
          unread inbox, no recent commit) before the evictor flips
          it. Wired through `SPORE_EVICTOR_IDLE_SECS` so the same
          override works for ad-hoc CLI invocations.
        '';
      };
    };

    supervise = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Run the coordinator as a long-lived systemd-user service
          (`spore-coordinator`) instead of relying solely on the
          reconcile timer to respawn it. ExecStart is `spore
          coordinator spawn`, which ensures the tmux session is alive
          then blocks until it dies, returning the 0/1/64 exit-code
          contract. The restart guards below turn that contract into a
          bounded respawn:

            0  clean shutdown (SIGTERM/SIGINT)   -> no respawn
            1  preflight failure (tier, exec)    -> no respawn
                 (pinned by RestartPreventExitStatus=1)
            64 unexpected session death          -> respawn, bounded
                 by startLimitBurst within startLimitInterval

          Default off: the bundled deployed model is the reconcile
          timer (spore-fleet-tick), and the spawn settle-check stays
          sharper when nothing is racing to respawn the session. Turn
          this on for a host that should hold a coordinator session
          open continuously.
        '';
      };

      startLimitInterval = lib.mkOption {
        type = lib.types.str;
        default = "30s";
        description = ''
          Window (StartLimitIntervalSec) over which startLimitBurst
          respawns are counted. startLimitBurst failures inside this
          window put the unit in failed state, so an external
          kill-loop bottoms out instead of respawning forever.
        '';
      };

      startLimitBurst = lib.mkOption {
        type = lib.types.ints.positive;
        default = 3;
        description = ''
          Max respawns (StartLimitBurst) allowed within
          startLimitInterval before the unit fails. With restartSec at
          1s, three failed starts inside 30s is the no-storm guard.
        '';
      };

      restartSec = lib.mkOption {
        type = lib.types.str;
        default = "1s";
        description = ''
          Delay (RestartSec) before a respawn after an exit-64
          unexpected death.
        '';
      };
    };

    extraEnv = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = { };
      example = lib.literalExpression ''{ SPORE_LOG = "debug"; }'';
      description = ''
        Extra entries merged into the unit's Environment=. Values
        flow through Nix evaluation and the /nix/store; never put a
        secret here. Use `credentialFiles` for those.
      '';
    };

    credentialFiles = lib.mkOption {
      type = lib.types.attrsOf lib.types.path;
      default = { };
      example = lib.literalExpression ''
        {
          github-pat = config.age.secrets.spore-github-pat.path;
        }
      '';
      description = ''
        Per-credential files exposed to the unit via systemd
        LoadCredential=. The reconciler (and the workers it
        spawns under the same unit) read decrypted material from
        the directory pointed at by $CREDENTIALS_DIRECTORY. Values
        never appear in Nix evaluation or in /nix/store; the path
        is dereferenced by systemd at activation time, so an
        agenix-decrypted file at /run/agenix/<name> works as input.

        The reconciler does NOT take an Anthropic API key from
        here. Workers spawn `claude` (claude-code), which manages
        its own credential lifecycle inside the client; this slot
        is for non-claude secrets the workers happen to need (MCP
        server keys, git-push PATs, etc.).
      '';
    };

    gracefulDeploy = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          Wire pre/post-activation hooks that drain active workers
          before a `nixos-rebuild switch` (or colmena deploy) and
          re-enable the reconciler after. Disable when the host is
          a one-off worker tier whose tasks should never see a
          wrap-up signal.
        '';
      };

      timeout = lib.mkOption {
        type = lib.types.ints.positive;
        default = 60;
        description = ''
          Seconds to wait for active workers to flush after the
          wrap-up signal before the pre-activation script kills
          remaining tmux sessions with SIGTERM.
        '';
      };

      message = lib.mkOption {
        type = lib.types.str;
        default = "wrap-up: deployment incoming";
        description = ''
          Body of the inbox message dropped into each active
          worker's inbox during the pre-activation drain. Workers
          should treat it as a request to flush in-progress notes
          to the task file before they get torn down.
        '';
      };

      preScript = lib.mkOption {
        type = lib.types.str;
        readOnly = true;
        description = ''
          Absolute path to the pre-activation script. Drop into
          colmena's `deployment.preActivation` to drive the same
          drain remotely. The script re-execs as
          `services.spore-fleet.user` via `runuser` and is safe to
          call from a root shell.
        '';
      };

      postScript = lib.mkOption {
        type = lib.types.str;
        readOnly = true;
        description = ''
          Absolute path to the post-activation script. Drop into
          colmena's `deployment.postActivation` to re-enable the
          fleet kill-switch after a successful deploy.
        '';
      };
    };

    matters = lib.mkOption {
      type = lib.types.attrsOf matterSubmodule;
      default = { };
      example = lib.literalExpression ''
        {
          linear = {
            enable = true;
            settings = {
              team = "MAR";
              ready_state = "Ready";
              done_state = "Done";
            };
            credentialFiles = {
              api_key = config.age.secrets.linear-api-key.path;
            };
          };
        }
      '';
      description = ''
        External work-source adapters. Each `matters.<name>` is
        rendered into the unit's environment as
        `SPORE_MATTER_<NAME>__ENABLED=1` plus one
        `SPORE_MATTER_<NAME>__<KEY>=<value>` per setting. Any
        per-matter `credentialFiles` are exposed via
        LoadCredential under the prefixed name
        `matter-<matter>-<key>`, and the resolved paths are
        passed to the adapter as
        `SPORE_MATTER_<NAME>__CREDENTIAL_<KEY>=%d/matter-<matter>-<key>`.

        The matter loader merges these env entries on top of any
        `[matter.<name>]` section in the project's spore.toml,
        so the same adapter can be configured locally via TOML
        and on a NixOS deployment via this option set.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    services.spore-fleet.gracefulDeploy = {
      preScript = "${preScript}/bin/spore-fleet-graceful-pre";
      postScript = "${postScript}/bin/spore-fleet-graceful-post";
    };

    # jq + python3 are coordinator/worker workhorses (JSON parsing of
    # gh, MCP, and settings.json output; ad-hoc one-liners that outgrow
    # bash). Bundling them with the fleet module means every
    # spore-managed host has them on PATH without each host pinning its
    # own systemPackages list.
    environment.systemPackages = [
      cfg.shimsPackage
      pkgs.jq
      pkgs.python3
    ];

    # Wire the pre/post hooks into NixOS system activation so a
    # `nixos-rebuild switch` (and colmena, which lifts the same
    # activation flow on the remote) drains workers before the new
    # systemd-user units load and re-enables the kill-switch after.
    # The NIXOS_ACTION gate keeps boot-time activation a no-op: there
    # is nothing to drain on a fresh boot, and disabling the flag
    # there would leave the reconciler paused until the next deploy.
    #
    # spore-shims runs on every activation: it symlinks the six host
    # shims into /usr/local/bin/ and sweeps any non-symlink left over
    # from the original `install -m 0755` from `spore infect`.
    #
    # spore-migrate runs `spore migrate --auto` as cfg.user on every
    # activation so pending host-state migrations under
    # bootstrap/migrations/ converge on `nixos-rebuild switch`. The
    # engine is idempotent and ledger-gated; failures are suppressed
    # so a single broken migration cannot brick the rebuild path
    # (visible in `journalctl -u nixos-activation`). See
    # docs/migrations.md for the authoring contract.
    system.activationScripts = lib.mkMerge [
      {
        spore-shims = ''
          install -d -m 0755 /usr/local/bin
          for f in spore-attach spore-coordinator-launch spore-worker-brief \
                   spore-fleet-tick spore-greet-coordinator spore-greet-worker; do
            target="${cfg.shimsPackage}/bin/$f"
            link="/usr/local/bin/$f"
            if [ ! -L "$link" ] || [ "$(readlink "$link")" != "$target" ]; then
              rm -f "$link"
              ln -s "$target" "$link"
            fi
          done
        '';
        spore-migrate = ''
          ${pkgs.util-linux}/bin/runuser -u ${cfg.user} -- \
            ${pkgs.coreutils}/bin/env PATH=${pkgs.bashInteractive}/bin:${pkgs.coreutils}/bin \
            ${cfg.package}/bin/spore migrate --auto || \
            echo "spore migrate: failed (see journal); continuing rebuild" >&2
        '';
      }
      (lib.mkIf cfg.gracefulDeploy.enable {
        spore-fleet-pre.text = ''
          case "''${NIXOS_ACTION:-}" in
            switch|test) ${preScript}/bin/spore-fleet-graceful-pre || true ;;
          esac
        '';
        spore-fleet-post = {
          deps = [ "spore-fleet-pre" ];
          text = ''
            case "''${NIXOS_ACTION:-}" in
              switch|test) ${postScript}/bin/spore-fleet-graceful-post || true ;;
            esac
          '';
        };
      })
    ];

    home-manager.users.${cfg.user} = {
      systemd.user.services.spore-fleet-reconcile = {
        Unit = {
          Description = "spore fleet reconciler (host=${cfg.hostId})";
        };
        Service = {
          Type = "oneshot";
          WorkingDirectory = toString cfg.projectRoot;
          ExecStart = "${cfg.package}/bin/spore fleet reconcile";
          Environment = mkEnvList (
            {
              SPORE_FLEET_MAX_WORKERS = toString cfg.maxWorkers;
              SPORE_HOST_ID = cfg.hostId;
              # The shims the reconciler spawns (spore-coordinator-launch,
              # spore-worker-brief) resolve `#!/usr/bin/env bash` and call
              # the standard userland; the agent sessions need the search
              # tools too. See fleetBinPath.
              PATH = fleetBinPath;
              # tmux runs a new session's command via $SHELL, falling back
              # to the user's passwd shell - which is spore-attach on a
              # deployed host. spore-attach would hijack the coordinator /
              # worker command with its own attach logic, so the agent
              # never execs and the session dies on spawn. Pin a real bash.
              SHELL = "${pkgs.bashInteractive}/bin/bash";
            } // matterEnv // cfg.extraEnv
          );
          # The reconcile is a oneshot that spawns the coordinator (and
          # worker) tmux server as daemonized children. KillMode=process
          # leaves them running when the oneshot exits; the default
          # control-group would reap the whole tree, killing the
          # coordinator the instant reconcile finishes.
          KillMode = "process";
          NoNewPrivileges = true;
          LockPersonality = true;
          RestrictSUIDSGID = true;
          ReadWritePaths = [ (toString cfg.projectRoot) ];
          LoadCredential = lib.mapAttrsToList
            (name: path: "${name}:${toString path}")
            (cfg.credentialFiles // matterCredentials);
        };
      };

      systemd.user.services.spore-coordinator = lib.mkIf cfg.supervise.enable {
        Unit = {
          Description = "spore coordinator (long-lived, host=${cfg.hostId})";
          # StartLimit caps respawn bursts so an external-kill loop
          # bottoms out in failed state instead of looping forever.
          # These keys live in [Unit], not [Service]; systemd warns and
          # ignores them under [Service].
          StartLimitIntervalSec = cfg.supervise.startLimitInterval;
          StartLimitBurst = cfg.supervise.startLimitBurst;
        };
        Service = {
          Type = "simple";
          WorkingDirectory = toString cfg.projectRoot;
          ExecStart = "${cfg.package}/bin/spore coordinator spawn";
          Environment = mkEnvList (
            {
              SPORE_FLEET_MAX_WORKERS = toString cfg.maxWorkers;
              SPORE_HOST_ID = cfg.hostId;
              PATH = fleetBinPath;
              # Pin a real bash so tmux does not run the coordinator
              # command through the spore-attach login shell. See the
              # reconcile unit for the full rationale.
              SHELL = "${pkgs.bashInteractive}/bin/bash";
            } // matterEnv // cfg.extraEnv
          );
          # Exit-code contract from `spore coordinator spawn`:
          #   0  clean shutdown          -> no respawn (not on-failure)
          #   1  preflight failure       -> no respawn (pinned below)
          #   64 unexpected death        -> respawn, bounded by StartLimit
          Restart = "on-failure";
          RestartSec = cfg.supervise.restartSec;
          RestartPreventExitStatus = "1";
          # The spawn entry point tears down only the coordinator tmux
          # session on its TERM trap; KillMode=process keeps a unit
          # restart from reaping sibling worker panes on the shared
          # tmux server.
          KillMode = "process";
          NoNewPrivileges = true;
          LockPersonality = true;
          RestrictSUIDSGID = true;
          ReadWritePaths = [ (toString cfg.projectRoot) ];
          LoadCredential = lib.mapAttrsToList
            (name: path: "${name}:${toString path}")
            (cfg.credentialFiles // matterCredentials);
        };
        Install.WantedBy = [ "default.target" ];
      };

      systemd.user.timers.spore-fleet-reconcile = {
        Unit.Description = "Periodic spore fleet reconcile";
        Timer = {
          OnBootSec = "30s";
          OnUnitInactiveSec = cfg.interval;
          AccuracySec = "5s";
          Unit = "spore-fleet-reconcile.service";
        };
        Install.WantedBy = [ "timers.target" ];
      };

      systemd.user.services.spore-fleet-evict-idle = lib.mkIf cfg.evictIdle.enable {
        Unit = {
          Description = "spore fleet evict-idle (auto-block idle workers, host=${cfg.hostId})";
        };
        Service = {
          Type = "oneshot";
          WorkingDirectory = toString cfg.projectRoot;
          ExecStart = "${cfg.package}/bin/spore fleet evict-idle";
          Environment = mkEnvList (
            {
              SPORE_HOST_ID = cfg.hostId;
              SPORE_EVICTOR_IDLE_SECS = toString cfg.evictIdle.idleSeconds;
              PATH = lib.makeBinPath [
                cfg.package
                pkgs.git
                pkgs.tmux
              ];
            } // cfg.extraEnv
          );
          # No Restart= - a failed sweep must not propagate; the
          # next timer tick will retry. Mirrors the
          # SuccessExitStatus=0 contract on the bundled unit.
          NoNewPrivileges = true;
          LockPersonality = true;
          RestrictSUIDSGID = true;
          ReadWritePaths = [ (toString cfg.projectRoot) ];
        };
      };

      systemd.user.timers.spore-fleet-evict-idle = lib.mkIf cfg.evictIdle.enable {
        Unit.Description = "Periodic spore fleet evict-idle";
        Timer = {
          OnBootSec = "2min";
          OnUnitInactiveSec = cfg.evictIdle.interval;
          AccuracySec = "15s";
          Unit = "spore-fleet-evict-idle.service";
        };
        Install.WantedBy = [ "timers.target" ];
      };

      systemd.user.paths = {
        spore-fleet-reconcile-flag = {
          Unit.Description = "Trigger spore-fleet-reconcile when the kill-switch flag changes";
          Path = {
            PathChanged = "%h/${stateRel}";
            Unit = "spore-fleet-reconcile.service";
          };
          Install.WantedBy = [ "default.target" ];
        };

        spore-fleet-reconcile-tasks = {
          Unit.Description = "Trigger spore-fleet-reconcile when tasks/ changes";
          Path = {
            PathChanged = "${toString cfg.projectRoot}/tasks";
            Unit = "spore-fleet-reconcile.service";
          };
          Install.WantedBy = [ "default.target" ];
        };
      };
    };
  };
}
