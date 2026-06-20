{ config, lib, ... }:

# rocky: the steady-state spore coordinator host. Runs `services.spore-fleet`
# against the ROC Linear team (newbuilds workspace) as the agent "rocky".
# This is the open-source-clean distillation of mcom's helm-coord deploy
# pattern, minus the product surface (no web app, postgres, caddy, or
# runtime container): one box, one coordinator, the worker fleet it
# dispatches, and the agenix-decrypted Linear token they consume.
#
# Phase 4 of the spore-upgrade-0.9.2 plan. Real per-host values (public
# IP, SSH host key fingerprint, disk device, operator pubkey) live in a
# gitignored ./local.nix; copy ./local.nix.example to start. Building
# system.build.toplevel does NOT decrypt secrets or touch hardware, so
# the config evaluates and builds clean before any box exists; only a
# `colmena apply` / `nixos-rebuild switch` on the live host activates it.

{
  imports = [
    ../../modules/nix-housekeeping.nix
    ./hardware.nix
    ./disk-config.nix
    ./networking.nix
    ./users.nix
    ./secrets.nix
  ]
  ++ lib.optional (builtins.pathExists ./local.nix) ./local.nix;

  nixpkgs.hostPlatform = lib.mkDefault "x86_64-linux";

  time.timeZone = lib.mkDefault "UTC";
  i18n.defaultLocale = "en_US.UTF-8";

  # Persistent journal so a coordinator or worker crash leaves a record
  # that survives a host reboot (default Storage=auto is tmpfs-backed
  # until /var/log/journal exists).
  services.journald.storage = "persistent";

  # The fleet itself. The package / shims / claude-code defaults come
  # from self.nixosModules.spore-fleet (wired in flake.nix); the host
  # only sets policy. matters.linear points the loader at the ROC team
  # and feeds it the agenix-decrypted Linear token by file path, never
  # by value (see ./secrets.nix for the age.secret declaration).
  services.spore-fleet = {
    enable = true;
    user = "spore";
    projectRoot = "/home/spore/project";
    hostId = config.networking.hostName;
    matters.linear = {
      enable = true;
      settings = {
        team = "ROC";
        ready_state = "Todo";
        in_progress_state = "In Progress";
        done_state = "Done";
        delegate = "true";
      };
      credentialFiles.api_key = config.age.secrets.linear-api-key.path;
    };
  };

  # spore-fleet drives its systemd-user units through home-manager.
  home-manager.useGlobalPkgs = true;
  home-manager.useUserPackages = true;
  home-manager.users.spore.home.stateVersion = config.system.stateVersion;

  system.stateVersion = lib.mkDefault "25.05";
}
