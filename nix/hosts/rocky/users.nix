{ config, lib, ... }:

# Two accounts: `spore` runs the coordinator + worker fleet, `deploy` is
# the colmena push target. The spore-fleet module deliberately does NOT
# shape the user (it owns only the systemd-user units), so the login
# shell, linger, and authorized keys live here.
#
# Operator and deploy SSH pubkeys are per-deployment, so they come from
# the gitignored ./local.nix (pubkeys are public, but the operator's key
# set is not something this open-source repo should pin). Without a
# local.nix the box builds but is unreachable; that is the intended
# safe default.

{
  # Login shell: spore-attach (from the shims package the fleet module
  # already references) attaches an SSH session to the live coordinator
  # tmux session, or a fallback pilot session when the coordinator is
  # down, then exits on detach. Linger keeps the fleet's systemd-user
  # units alive with no interactive session present.
  users.users.spore = {
    isNormalUser = true;
    home = "/home/spore";
    shell = "${config.services.spore-fleet.shimsPackage}/bin/spore-attach";
    linger = true;
  };

  # colmena re-execs `nixos-rebuild switch` as this user over SSH; the
  # NOPASSWD wheel grant lets that run unattended. Keep it deploy-only.
  users.users.deploy = {
    isNormalUser = true;
    description = "colmena deployment user";
    extraGroups = [ "wheel" ];
  };

  security.sudo.extraRules = [
    {
      users = [ "deploy" ];
      commands = [
        {
          command = "ALL";
          options = [ "NOPASSWD" ];
        }
      ];
    }
  ];

  # Placeholder so the option type-checks with no local.nix present;
  # ./local.nix appends the operator + deploy pubkeys.
  #
  # WARNING: ./local.nix is gitignored, and `nixos-rebuild switch --flake`
  # only sees git-tracked (or staged) files. A plain deploy therefore does
  # NOT see local.nix, leaving these lists empty and wiping every
  # authorized key (NixOS manages /etc/ssh/authorized_keys.d/%u). That is
  # how a switch can silently lock the box out. `just deploy` force-stages
  # local.nix so the flake reads it; the assertion below fails the build
  # closed if it still resolves empty, so a keyless switch can never ship.
  users.users.spore.openssh.authorizedKeys.keys = lib.mkDefault [ ];
  users.users.deploy.openssh.authorizedKeys.keys = lib.mkDefault [ ];

  assertions = [
    {
      assertion = config.users.users.spore.openssh.authorizedKeys.keys != [ ];
      message = ''
        rocky: no SSH authorized keys resolved for the `spore` user.
        local.nix was almost certainly not visible to the flake build (it
        is gitignored; flakes only read tracked/staged files). Refusing to
        build a host nobody can log into. Fix: ensure
        nix/hosts/rocky/local.nix exists, then deploy via `just deploy`
        (it force-stages local.nix), or run
        `git add -f nix/hosts/rocky/local.nix` before `nixos-rebuild switch`.
      '';
    }
  ];
}
