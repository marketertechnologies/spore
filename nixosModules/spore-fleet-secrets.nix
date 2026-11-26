{ config, lib, pkgs, ... }:

# The secrets broker (docs/secrets.md): a socket-activated service
# running as its own user that holds project secrets and the GitHub
# App key. Agents (services.spore-fleet.user) reach it only through
# `spore secrets run`, the role-loop mirror and the kill switch.

let
  fleet = config.services.spore-fleet;
  cfg = fleet.secrets;
  flagDir = builtins.dirOf cfg.fleetFlag;

  commandType = lib.types.submodule {
    options = {
      argv = lib.mkOption {
        type = lib.types.nonEmptyListOf lib.types.str;
        example = lib.literalExpression ''[ "''${pkgs.postgresql}/bin/psql" "''${DATABASE_URL}" "-X" "-A" ]'';
        description = ''
          Fixed command prefix. `''${NAME}` expands from the profile's
          `vars`; caller arguments are appended after it and never
          expanded. argv[0] must be a literal program.
        '';
      };
      args = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = ''
          Positional regexps, each fully matching one caller argument.
          Empty means the caller passes no arguments.
        '';
      };
      variadic = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = "The last `args` pattern repeats for any further arguments.";
      };
      stdin = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = "Forward the caller's stdin (`spore secrets run --stdin`).";
      };
      timeout = lib.mkOption {
        type = lib.types.str;
        default = "60s";
        description = "Go duration, at most 10m.";
      };
    };
  };

  profileType = lib.types.submodule {
    options = {
      project = lib.mkOption {
        type = lib.types.str;
        description = "Project whose secrets.env the variables come from. Workers of this project may use the profile.";
      };
      description = lib.mkOption {
        type = lib.types.str;
        default = "";
      };
      vars = lib.mkOption {
        type = lib.types.attrsOf lib.types.str;
        default = { };
        example = { DATABASE_URL = "MARKETER_RO_DATABASE_URL"; };
        description = "Variable the command sees -> key in the layered secrets.env files.";
      };
      commands = lib.mkOption {
        type = lib.types.attrsOf commandType;
        description = "Allowed commands, by the name callers pass to `spore secrets run <profile> -- <name>`.";
      };
    };
  };

  brokerConfig = {
    agent_user = fleet.user;
    pilot_group = cfg.pilotGroup;
    secrets_dir = cfg.secretsDir;
    state_dir = cfg.stateDir;
    fleet_flag = cfg.fleetFlag;
    projects = lib.mapAttrs
      (name: _: {
        repo = cfg.projects.${name}.repo or "";
        push_branch = cfg.projects.${name}.pushBranch or "";
      })
      (fleet.projects // cfg.projects);
    profiles = lib.mapAttrs
      (_: p: {
        inherit (p) project description vars;
        commands = lib.mapAttrs (_: c: { inherit (c) argv args variadic stdin timeout; }) p.commands;
      })
      cfg.profiles;
  };

  configFile = pkgs.writeText "spore-secrets-broker.json" (builtins.toJSON brokerConfig);
in
{
  options.services.spore-fleet.secrets = {
    enable = lib.mkEnableOption ''
      the spore secrets broker. Moves project secrets and the GitHub
      App key to a user the agents cannot read as, and gives agents
      `spore secrets run` instead (docs/secrets.md)'';

    user = lib.mkOption {
      type = lib.types.str;
      default = "spore-secrets";
      description = "System user that owns the secrets and runs the broker.";
    };

    pilotGroup = lib.mkOption {
      type = lib.types.str;
      default = "spore-pilots";
      description = ''
        Group of human pilots: they can read the secrets files, grant
        profiles and re-enable the fleet. The agent user must not be in
        it.
      '';
    };

    pilots = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Users added to `pilotGroup`.";
    };

    secretsDir = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/spore-secrets/secrets";
      description = ''
        `secrets.env` (global), `<project>/secrets.env`, and
        `<project>/github.toml` plus its App key. Owned by `user`,
        group `pilotGroup`; keep files 0640.
      '';
    };

    stateDir = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/spore-secrets/state";
      description = "audit.log, grants.json and the broker's git mirrors.";
    };

    fleetFlag = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/spore-fleet/fleet-enabled";
      description = ''
        Kill-switch flag, owned by `user` in a directory the agent
        cannot write. Agents stop the fleet through the broker; only
        root or a pilot starts it again.
      '';
    };

    projects = lib.mkOption {
      type = lib.types.attrsOf (lib.types.submodule {
        options = {
          repo = lib.mkOption {
            type = lib.types.str;
            default = "";
            example = "github.com/acme/app";
            description = "HOST/OWNER/REPO the broker pushes to and pins gh calls to. Required for the draft-PR flow.";
          };
          pushBranch = lib.mkOption {
            type = lib.types.str;
            default = "";
            description = "Regexp every pushed branch must match. Empty: `^wt/[A-Za-z0-9._-]+$`.";
          };
        };
      });
      default = { };
      description = "Per-project broker settings, keyed like `services.spore-fleet.projects`.";
    };

    profiles = lib.mkOption {
      type = lib.types.attrsOf profileType;
      default = { };
      description = ''
        Named capabilities. The coordinator may use every profile;
        a worker only its own project's, plus `spore secrets grant`.
      '';
    };

    path = lib.mkOption {
      type = lib.types.listOf lib.types.package;
      default = [ ];
      example = lib.literalExpression "[ pkgs.postgresql pkgs.curl ]";
      description = "Extra packages on the broker's PATH for profile commands. git and gh are always there.";
    };
  };

  config = lib.mkIf (fleet.enable && cfg.enable) {
    assertions = [
      {
        assertion = !(builtins.elem fleet.user cfg.pilots);
        message = "services.spore-fleet.secrets.pilots must not contain the agent user ${fleet.user}.";
      }
      {
        assertion = fleet.user != cfg.user;
        message = "services.spore-fleet.secrets.user must differ from services.spore-fleet.user.";
      }
    ];

    users.users.${cfg.user} = {
      isSystemUser = true;
      group = cfg.user;
      home = "/var/lib/spore-secrets";
    };
    users.groups.${cfg.user} = { };
    users.groups.${cfg.pilotGroup}.members = cfg.pilots;
    users.groups.spore-secrets-clients.members = [ fleet.user ] ++ cfg.pilots;

    environment.etc."spore/secrets-broker.json".source = configFile;
    environment.variables.SPORE_FLEET_FLAG = cfg.fleetFlag;

    systemd.tmpfiles.rules = [
      "d /var/lib/spore-secrets 0750 ${cfg.user} ${cfg.pilotGroup} -"
      "d ${cfg.secretsDir} 0750 ${cfg.user} ${cfg.pilotGroup} -"
      "Z ${cfg.secretsDir} - ${cfg.user} ${cfg.pilotGroup} -"
      "d ${cfg.stateDir} 0750 ${cfg.user} ${cfg.pilotGroup} -"
      "d ${flagDir} 0755 ${cfg.user} ${cfg.user} -"
    ];

    systemd.sockets.spore-secrets = {
      description = "spore secrets broker socket";
      wantedBy = [ "sockets.target" ];
      listenStreams = [ "/run/spore-secrets/broker.sock" ];
      socketConfig = {
        SocketUser = cfg.user;
        SocketGroup = "spore-secrets-clients";
        SocketMode = "0660";
        DirectoryMode = "0755";
      };
    };

    systemd.services.spore-secrets = {
      description = "spore secrets broker";
      requires = [ "spore-secrets.socket" ];
      after = [ "spore-secrets.socket" "systemd-tmpfiles-setup.service" ];
      path = [ fleet.package pkgs.git pkgs.gh pkgs.coreutils ] ++ cfg.path;
      restartTriggers = [ configFile ];
      serviceConfig = {
        User = cfg.user;
        Group = cfg.user;
        ExecStart = "${fleet.package}/bin/spore secrets serve --config /etc/spore/secrets-broker.json";
        Restart = "on-failure";
        UMask = "0027";
        NoNewPrivileges = true;
        PrivateTmp = true;
        PrivateDevices = true;
        ProtectHome = true;
        ProtectSystem = "strict";
        ReadWritePaths = [ cfg.stateDir flagDir ];
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectControlGroups = true;
        RestrictSUIDSGID = true;
        LockPersonality = true;
        RestrictAddressFamilies = [ "AF_UNIX" "AF_INET" "AF_INET6" ];
      };
    };
  };
}
