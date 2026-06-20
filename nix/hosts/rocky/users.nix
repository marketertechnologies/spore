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
  users.users.spore.openssh.authorizedKeys.keys = lib.mkDefault [ ];
  users.users.deploy.openssh.authorizedKeys.keys = lib.mkDefault [ ];
}
