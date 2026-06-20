{ config, lib, pkgs, ... }:

# rocky: the steady-state spore coordinator host. Runs `services.spore-fleet`
# against the ROC Linear team (newbuilds workspace) as the agent "rocky".
# This is the open-source-clean distillation of mcom's helm-coord deploy
# pattern, minus the product surface (no web app, postgres, caddy, or
# runtime container): one box, one coordinator, the worker fleet it
# dispatches, and the agenix-decrypted Linear token they consume.
#
# Phase 4 of the spore-upgrade-0.9.2 plan. Real per-host values (public
# IP, SSH host key fingerprint, disk device, operator pubkey) live in a
# gitignored ./local.nix; copy ./local.nix.example to start. The Linear
# token is NOT in this repo (not even encrypted): it lives only on the
# box at /var/lib/spore-secrets/linear-api-key, placed out-of-band at
# deploy time. The repo references that path, never the value. Building
# system.build.toplevel touches no hardware and reads no secret, so the
# config evaluates and builds clean before any box exists.

{
  imports = [
    ../../modules/nix-housekeeping.nix
    ./hardware.nix
    ./disk-config.nix
    ./networking.nix
    ./users.nix
  ]
  ++ lib.optional (builtins.pathExists ./local.nix) ./local.nix;

  nixpkgs.hostPlatform = lib.mkDefault "x86_64-linux";

  time.timeZone = lib.mkDefault "UTC";
  i18n.defaultLocale = "en_US.UTF-8";

  # Persistent journal so a coordinator or worker crash leaves a record
  # that survives a host reboot (default Storage=auto is tmpfs-backed
  # until /var/log/journal exists).
  services.journald.storage = "persistent";

  # Interactive system PATH for an operator SSH session. The spore-attach
  # login shell calls tmux, and a hands-on operator runs spore / git /
  # claude directly; the fleet systemd units carry these on their own
  # PATH, but a login shell does not see that, so put them here too.
  # Without tmux the login shell (spore-attach) cannot attach at all, and
  # without claude the first-run login cannot be performed.
  environment.systemPackages = [
    config.services.spore-fleet.package
    config.services.spore-fleet.claudeCodePackage
    pkgs.git
    pkgs.tmux
  ];

  # The Linear token lives only on the host. Declare the directory (perms,
  # ownership) here; the secret file itself is placed out-of-band at
  # deploy time (see docs/deploy.md), so nothing secret-bearing is in nix
  # or the repo. systemd LoadCredential reads it by path at unit start.
  systemd.tmpfiles.rules = [
    "d /var/lib/spore-secrets 0700 spore users -"
  ];

  # The fleet itself. The package / shims / claude-code defaults come
  # from self.nixosModules.spore-fleet (wired in flake.nix); the host
  # only sets policy. matters.linear points the loader at the ROC team
  # and feeds it the on-host Linear token by file path, never by value.
  services.spore-fleet = {
    enable = true;
    user = "spore";
    projectRoot = "/home/spore/project";
    hostId = config.networking.hostName;
    # Hold the coordinator session open under a long-lived
    # systemd-user unit (`spore-coordinator`) instead of relying
    # only on the reconcile-timer watchdog. ExecStart blocks until
    # the tmux session dies and exits with the 0/1/64 contract:
    # exit 64 (unexpected session death) triggers a respawn within
    # ~1s, bounded by StartLimitBurst. Combined with the in-pane
    # supervisor loop (spore.toml [coordinator].supervise = true),
    # a token-cap flush stays in-pane (no detach) and a catastrophic
    # session loss is recovered by systemd, not just by the next
    # reconcile tick. See docs/coordinator-supervisor.md.
    supervise.enable = true;
    matters.linear = {
      enable = true;
      settings = {
        team = "ROC";
        ready_state = "Todo";
        in_progress_state = "In Progress";
        done_state = "Done";
        delegate = "true";
      };
      credentialFiles.api_key = "/var/lib/spore-secrets/linear-api-key";
    };
  };

  # spore-fleet drives its systemd-user units through home-manager.
  home-manager.useGlobalPkgs = true;
  home-manager.useUserPackages = true;
  home-manager.users.spore.home.stateVersion = config.system.stateVersion;

  # tmux config for the spore user's sessions (coordinator, workers, and
  # the operator's spore-attach): mouse scroll + the operator theme +
  # the spore token-status context panel. See ./tmux.conf.
  home-manager.users.spore.xdg.configFile."tmux/tmux.conf".source = ./tmux.conf;

  system.stateVersion = lib.mkDefault "25.05";
}
