{ config, lib, ... }:

# Two accounts: `spore` runs the coordinator + worker fleet, `deploy` is
# the colmena push target. The spore-fleet module deliberately does NOT
# shape the user (it owns only the systemd-user units), so the login
# shell, linger, and authorized keys live here.
#
# Operator and deploy SSH pubkeys are per-deployment and never enter this
# repo, the nix build, or the store. sshd reads them at login from
# /etc/spore-ssh/<user>, placed out-of-band on the box at deploy time -
# the same pattern as the on-host Linear token under /var/lib/spore-secrets.
# Because a `nixos-rebuild switch` never writes these files, a deploy can
# no longer wipe the keys and lock the box out, so the empty-keys footgun
# that once needed a build-time assertion is gone. The fail-closed guard
# now lives in `just deploy`, which refuses to switch unless
# /etc/spore-ssh/<user> is present and non-empty.

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

  # Operator pubkeys reach sshd at login from /etc/spore-ssh/<user>, in
  # addition to the NixOS-managed /etc/ssh/authorized_keys.d/%u (which
  # stays empty here - there are no declarative keys). The directory is
  # created root-owned so sshd StrictModes accepts a file for any target
  # user; the key files themselves are dropped out-of-band, root-owned
  # 0644 (see docs/deploy.md). A switch never touches these files, so it
  # cannot lock the box out.
  services.openssh.authorizedKeysFiles = lib.mkAfter [ "/etc/spore-ssh/%u" ];
  systemd.tmpfiles.rules = [
    "d /etc/spore-ssh 0755 root root -"
  ];
}
