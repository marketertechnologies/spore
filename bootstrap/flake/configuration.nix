{ config, lib, modulesPath, pkgs, inputs, ... }:
let
  sporePkgs = inputs.spore.packages.${pkgs.stdenv.hostPlatform.system};
  # Host project list. Written by `spore infect`'s Stage() at infect
  # time with a single entry derived from the operator's --repo, or
  # left as the bundled `{ }` default when infect runs without a repo.
  # Edit-in-place post-install to add more repos; one-line append plus
  # `sudo nixos-rebuild switch --flake /etc/nixos#spore-bootstrap`.
  sporeProjects = import ./spore-projects.nix;
  sporeAttach = pkgs.writeShellScriptBin "spore-attach" ''
    set -e

    if [ "''${1:-}" = "-c" ]; then
      set -- ''${2:-}
      shift
    fi

    mode="''${1:-coord}"

    attach_pilot() {
      name="''${1:-default}"
      exec ${pkgs.tmux}/bin/tmux new-session -A -s "spore/pilot/$name" ${pkgs.bashInteractive}/bin/bash -l
    }

    case "$mode" in
      coord)
        sessions="$(${pkgs.tmux}/bin/tmux ls -F '#{session_name}' 2>/dev/null | ${pkgs.gnugrep}/bin/grep '/coordinator$' || true)"
        n="$(printf '%s' "$sessions" | ${pkgs.gnugrep}/bin/grep -c . || true)"
        case "$n" in
          1)
            exec ${pkgs.tmux}/bin/tmux attach -t "$sessions"
            ;;
      0)
        printf '%s\n' \
          "spore-attach: no coordinator session running on this host." \
          "spore-attach: dropping into a default pilot session so you can recover." \
          "spore-attach: if Claude or Codex is not logged in yet, run its login command here first." \
          "spore-attach: then run: spore fleet enable && spore fleet reconcile" >&2
        attach_pilot default
        ;;
          *)
            echo "spore-attach: multiple coordinator sessions present:" >&2
            printf '  %s\n' $sessions >&2
            echo "spore-attach: ssh in as root to reconcile." >&2
            exit 1
            ;;
        esac
        ;;
      pilot)
        attach_pilot "''${2:-default}"
        ;;
      *)
        echo "spore-attach: unknown mode: $mode" >&2
        echo "usage: spore-attach [coord | pilot [<name>]]" >&2
        exit 2
        ;;
    esac
  '';
in
{
  imports = [
    (modulesPath + "/installer/scan/not-detected.nix")
    (modulesPath + "/profiles/qemu-guest.nix")
    ./disk-config.nix
  ] ++ lib.optional (builtins.pathExists ./local.nix) ./local.nix;

  nixpkgs.config.allowUnfreePredicate =
    pkg: builtins.elem (lib.getName pkg) [ "claude-code" ];

  boot.loader.grub = {
    efiSupport = true;
    efiInstallAsRemovable = true;
  };

  services.openssh = {
    enable = true;
    settings.PasswordAuthentication = false;
    settings.KbdInteractiveAuthentication = false;
    settings.PermitRootLogin = "prohibit-password";
  };

  # Operator-facing account. No shell prompt, no sudo, no wheel. SSH
  # in as spore -> the login shell (spore-attach) attaches you to a
  # tmux session and exits when you detach.
  #
  # Two landing modes, picked by the per-key authorized_keys command:
  # - bare key (no command=): primary-operator path. Attaches to the
  #   singleton coordinator session; falls back to a default pilot
  #   session if the coordinator is down (so SSH never bounces).
  # - command="/usr/local/bin/spore-attach pilot <name>": gives that
  #   key its own private session at spore/pilot/<name>. Use this for
  #   secondary pilots so they neither share a pane with the
  #   coordinator nor with each other.
  #
  # Authorized keys come from local.nix. Root SSH stays open for
  # emergency reconcile.
  users.users.spore = {
    isNormalUser = true;
    home = "/home/spore";
    shell = "${sporeAttach}/bin/spore-attach";
  };

  environment.systemPackages = [
    sporePkgs.spore
    sporePkgs.shims
  ] ++ (with pkgs; [
    claude-code
    codex
    git
    rsync
    curl
    gnumake
    htop
    tmux
    vim
  ]);

  # Symlink the six host shims into /usr/local/bin/ so the kernel's
  # hard-coded paths (e.g. tokenmonitor's respawn-pane message and the
  # entries written to /etc/spore/coordinator.env by `spore infect`)
  # keep resolving. Sweeps any non-symlink target left over from an
  # earlier `install -m 0755` so a re-deploy converges.
  system.activationScripts.spore-shims = ''
    install -d -m 0755 /usr/local/bin
    for f in spore-attach spore-coordinator-launch spore-worker-brief \
             spore-fleet-tick spore-greet-coordinator spore-greet-worker; do
      target="${sporePkgs.shims}/bin/$f"
      link="/usr/local/bin/$f"
      if [ ! -L "$link" ] || [ "$(readlink "$link")" != "$target" ]; then
        rm -f "$link"
        ln -s "$target" "$link"
      fi
    done
  '';

  # linger keeps the spore user's systemd --user instance running
  # without an attached login, so the home-manager-rendered fleet
  # reconcile units fire from boot.
  users.users.spore.linger = true;

  # home-manager wiring required by services.spore-fleet, which
  # renders the per-project reconcile units under
  # home-manager.users.spore.systemd.user.{services,timers,paths}.
  home-manager.useGlobalPkgs = true;
  home-manager.useUserPackages = true;
  home-manager.users.spore.home.stateVersion = config.system.stateVersion;

  # Fleet reconciler. Replaces the legacy systemd.services.spore-
  # coordinator + timer pair that ticked /usr/local/bin/spore-fleet-
  # tick: the upstream NixOS module generates one per-project
  # reconcile service + timer + path watcher under home-manager.
  #
  # enable is gated on a non-empty projects attrset so `spore infect`
  # without --repo (no project to host yet) still builds.
  #
  # SPORE_COORDINATOR_AGENT / SPORE_AGENT_BINARY keep the local
  # claude-wrapping shims (spore-coordinator-launch / spore-worker-
  # brief) in the loop; they implement site-local role-seeding,
  # brief-piping, transcript logging, and sentinel-file behavior the
  # upstream kernel does not ship.
  services.spore-fleet = {
    enable = sporeProjects != { };
    user = "spore";
    package = sporePkgs.spore;
    claudeCodePackage = pkgs.claude-code;
    hostId = config.networking.hostName;
    projects = sporeProjects;
    extraEnv = {
      SPORE_COORDINATOR_AGENT = "/usr/local/bin/spore-coordinator-launch";
      SPORE_AGENT_BINARY = "/usr/local/bin/spore-worker-brief";
      # Override the upstream module's curated PATH (spore + claude-code
      # + bash + coreutils + git + tmux) so /run/wrappers, /usr/local/bin,
      # and the spore user's nix-profile are visible to the reconciler
      # and to the shims it spawns. extraEnv merges last in the module's
      # Environment block so this wins over the curated default.
      PATH = "/run/wrappers/bin:/run/current-system/sw/bin:/home/spore/.nix-profile/bin:/usr/local/bin:/usr/bin:/bin";
    };
  };

  system.stateVersion = "24.05";
}
