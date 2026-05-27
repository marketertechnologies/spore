{
  description = "spore bootstrap: minimal NixOS for a fresh cloud VM, installed via nixos-anywhere.";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  inputs.disko.url = "github:nix-community/disko";
  inputs.disko.inputs.nixpkgs.follows = "nixpkgs";

  # home-manager is required by services.spore-fleet (the module renders
  # the per-project reconcile units under home-manager.users.<user>.
  # systemd.user.{services,timers,paths}). Pinned to follow the bundled
  # nixpkgs so a single nixpkgs eval drives the whole closure.
  inputs.home-manager.url = "github:nix-community/home-manager";
  inputs.home-manager.inputs.nixpkgs.follows = "nixpkgs";

  # spore is pinned to the local CLI's HEAD commit by `spore infect`'s
  # Stage() at infect time (`nix flake lock --override-input spore
  # github:marketertechnologies/spore/<rev>`), so the freshly-installed
  # system runs the same spore the operator built locally. The bare
  # `github:` URL here is only the resolution path for the override;
  # the static lock entry is rewritten before nixos-anywhere reads it.
  inputs.spore.url = "github:marketertechnologies/spore";

  outputs =
    inputs@{ nixpkgs, disko, home-manager, spore, ... }:
    {
      nixosConfigurations.spore-bootstrap = nixpkgs.lib.nixosSystem {
        system = "x86_64-linux";
        specialArgs = { inherit inputs; };
        modules = [
          disko.nixosModules.disko
          home-manager.nixosModules.home-manager
          spore.nixosModules.spore-fleet
          ./configuration.nix
        ];
      };
    };
}
