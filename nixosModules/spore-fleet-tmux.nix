{ config, lib, ... }:

let
  cfg = config.services.spore-fleet;
in
{
  options.services.spore-fleet = {
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
    };
  };

  config = lib.mkIf cfg.tmux.enable {
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
  };
}
