{
  # Shared nix-store housekeeping. Every host under nix/hosts/ imports
  # this so auto-optimise plus weekly gc and optimise timers are
  # declared uniformly; without periodic gc the store grows unbounded
  # and disk pressure becomes a deploy blocker on a small cloud VM.
  nix = {
    settings.auto-optimise-store = true;
    # Required by the home-manager activation, which calls the modern
    # `nix profile install` during installPackages. Without these the
    # home-manager-<user>.service unit fails on every rebuild with
    # 'experimental Nix feature "nix-command" is disabled' and the
    # user's xdg.configFile entries (e.g. tmux.conf) never get linked.
    settings.experimental-features = [ "nix-command" "flakes" ];

    gc = {
      automatic = true;
      dates = "weekly";
      options = "--delete-older-than 7d";
    };

    optimise = {
      automatic = true;
      dates = [ "weekly" ];
    };
  };
}
