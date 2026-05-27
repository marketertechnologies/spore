{
  description = "spore bootstrap: minimal NixOS for a fresh cloud VM, installed via nixos-anywhere.";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  inputs.disko.url = "github:nix-community/disko";
  inputs.disko.inputs.nixpkgs.follows = "nixpkgs";

  # spore is pinned to the local CLI's HEAD commit by `spore infect`'s
  # Stage() at infect time (`nix flake lock --override-input spore
  # github:marketertechnologies/spore/<rev>`), so the freshly-installed
  # system runs the same spore the operator built locally. The bare
  # `github:` URL here is only the resolution path for the override;
  # the static lock entry is rewritten before nixos-anywhere reads it.
  inputs.spore.url = "github:marketertechnologies/spore";

  outputs =
    inputs@{ nixpkgs, disko, ... }:
    {
      nixosConfigurations.spore-bootstrap = nixpkgs.lib.nixosSystem {
        system = "x86_64-linux";
        specialArgs = { inherit inputs; };
        modules = [
          disko.nixosModules.disko
          ./configuration.nix
        ];
      };
    };
}
