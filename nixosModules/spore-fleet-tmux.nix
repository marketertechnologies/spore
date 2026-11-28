{ config, lib, pkgs, ... }:

let
  cfg = config.services.spore-fleet;
  agentSlice = "spore-agents.slice";

  # A server already on the socket (started from an SSH login before
  # this unit existed) cannot be adopted; wait for it to exit rather
  # than fail in a restart loop.
  tmuxServerScript = pkgs.writeShellScript "spore-tmux-server" ''
    tmux=${pkgs.tmux}/bin/tmux
    while ${pkgs.coreutils}/bin/timeout 10 "$tmux" list-sessions >/dev/null 2>&1; do
      ${pkgs.coreutils}/bin/sleep 10
    done
    exec "$tmux" -D
  '';
  tmuxServerReady = pkgs.writeShellScript "spore-tmux-ready" ''
    for _ in $(${pkgs.coreutils}/bin/seq 50); do
      ${pkgs.coreutils}/bin/timeout 10 ${pkgs.tmux}/bin/tmux list-sessions >/dev/null 2>&1 && exit 0
      ${pkgs.coreutils}/bin/sleep 0.2
    done
    echo "spore-tmux: server not answering after 10s" >&2
    exit 1
  '';
in
{
  options.services.spore-fleet = {
    agentMemoryMax = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "24G";
      description = ''
        systemd `MemoryMax` of `spore-agents.slice`, the user slice
        holding the tmux server (`tmux.server.enable`), every pane it
        runs (coordinators, workers, role panes, and pilot shells on
        the same server) and the reconcile passes. tmux puts each pane
        in a `tmux-spawn-*.scope` inside its server's slice. At the
        cap the kernel OOM-kills inside the slice, so a runaway agent
        cannot take the host down. Any `MemoryMax` value (`24G`,
        `75%`). Null leaves the slice unbounded. A changed value
        applies at the next boot, or at once with `systemctl --user
        set-property spore-agents.slice MemoryMax=<value>`.
      '';
    };

    tmux = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = cfg.enable;
        defaultText = lib.literalExpression "config.services.spore-fleet.enable";
        description = ''
          Prepend spore's tmux baseline to `/etc/tmux.conf`: deep
          scrollback, mouse wheel into copy-mode, Claude Code TUI
          passthrough and extended keys, and half-page wheel scroll
          in copy-mode. Lines the host adds to
          `environment.etc."tmux.conf".text` render after this block,
          so a host's own `set` lines win.
        '';
      };

      historyLimit = lib.mkOption {
        type = lib.types.ints.positive;
        default = 250000;
        description = ''
          Scrollback lines per pane. tmux's own default (2000) loses
          long agent output. Also seeds `programs.tmux.historyLimit`
          (at mkDefault) so enabling the stock NixOS tmux module does
          not reset it to 2000.
        '';
      };

      mouse = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          Wheel enters copy-mode and pages history. Shift+drag keeps
          terminal-native selection.
        '';
      };

      defaultShell = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = lib.literalExpression ''"''${pkgs.bashInteractive}/bin/bash"'';
        description = ''
          Pin tmux's default-shell. Set this when the operator user's
          login shell is `spore-attach`: tmux otherwise inherits it
          and mis-parses the `-c CMD` it uses for new panes.
        '';
      };

      extraConfig = lib.mkOption {
        type = lib.types.lines;
        default = "";
        description = "Extra tmux lines appended to spore's baseline block.";
      };

      server.enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          Run `services.spore-fleet.user`'s tmux server as the
          `spore-tmux` systemd user service in `spore-agents.slice`
          (needs `services.spore-fleet.enable`). The user lingers, so
          the server is up after a reboot with nobody logged in, and
          `agentMemoryMax` bounds every pane. The reconcile units
          order after it. A restart of the unit kills every session,
          so home-manager leaves a running one alone on switch.
        '';
      };
    };
  };

  config = lib.mkMerge [
    (lib.mkIf cfg.enable {
      home-manager.users.${cfg.user}.systemd.user.slices.spore-agents = {
        Unit = {
          Description = "spore agents: tmux server, panes and reconcile passes";
          # Stopping the slice stops every agent in it.
          X-SwitchMethod = "keep-old";
        };
        Slice = lib.optionalAttrs (cfg.agentMemoryMax != null) {
          MemoryMax = cfg.agentMemoryMax;
        };
      };
    })

    # The user's tmux server as a unit: back at boot (the user
    # lingers), and in agentSlice so agentMemoryMax bounds every pane.
    (lib.mkIf (cfg.enable && cfg.tmux.server.enable) {
      home-manager.users.${cfg.user}.systemd.user.services.spore-tmux = {
        Unit = {
          Description = "spore tmux server (agent and pilot sessions)";
          # A restart kills every session; a changed unit applies at
          # the next boot.
          X-SwitchMethod = "keep-old";
        };
        Service = {
          Slice = agentSlice;
          ExecStart = "${tmuxServerScript}";
          ExecStartPost = "${tmuxServerReady}";
          Restart = "on-failure";
          RestartSec = "5s";
          # The server's environment is every new pane's: give panes
          # the reconciler's tools plus the user and system profiles
          # a login shell would have.
          Environment = lib.mapAttrsToList (n: v: "${n}=${v}") ({
            PATH = "${cfg.agentPath}:/run/wrappers/bin:/etc/profiles/per-user/${cfg.user}/bin:/run/current-system/sw/bin";
            LANG = config.i18n.defaultLocale;
            LOCALE_ARCHIVE = "${config.i18n.glibcLocales}/lib/locale/locale-archive";
            SPORE_FLEET_MAX_WORKERS = toString cfg.maxWorkers;
            SPORE_HOST_ID = cfg.hostId;
          } // lib.optionalAttrs cfg.secrets.enable {
            SPORE_FLEET_FLAG = cfg.secrets.fleetFlag;
          });
        };
        Install.WantedBy = [ "default.target" ];
      };
    })

    (lib.mkIf cfg.tmux.enable {
      programs.tmux.historyLimit = lib.mkDefault cfg.tmux.historyLimit;

      environment.etc."tmux.conf".text = lib.mkBefore (
        lib.optionalString (cfg.tmux.defaultShell != null) ''
          set -g default-shell ${cfg.tmux.defaultShell}
        ''
        + ''
          set -g history-limit ${toString cfg.tmux.historyLimit}
          set -g mouse ${if cfg.tmux.mouse then "on" else "off"}
          set -g allow-passthrough on
          set -g detach-on-destroy off
          set -s extended-keys on
          set -as terminal-features 'xterm*:extkeys'
          bind -n S-PageUp copy-mode -eu
          bind -T copy-mode WheelUpPane send -N25 -X scroll-up
          bind -T copy-mode WheelDownPane send -N25 -X scroll-down
          bind -T copy-mode-vi WheelUpPane send -N25 -X scroll-up
          bind -T copy-mode-vi WheelDownPane send -N25 -X scroll-down
        ''
        + cfg.tmux.extraConfig
      );
    })
  ];
}
