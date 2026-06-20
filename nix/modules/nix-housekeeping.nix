{
  # Shared nix-store housekeeping. Every host under nix/hosts/ imports
  # this so auto-optimise plus weekly gc and optimise timers are
  # declared uniformly; without periodic gc the store grows unbounded
  # and disk pressure becomes a deploy blocker on a small cloud VM.
  nix = {
    settings.auto-optimise-store = true;

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
