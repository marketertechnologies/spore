{ modulesPath, ... }:

# Generic cloud-VM hardware profile (Hetzner / nixos-anywhere target).
# qemu-guest pulls the virtio kernel modules a Hetzner Cloud VM needs;
# not-detected stands in for a generated hardware-configuration.nix on
# a box that has not been scanned yet. EFI GRUB matches the ESP in
# ./disk-config.nix. Override any of this from ./local.nix once the box
# is provisioned and `nixos-generate-config` has run against the real
# disk controller.

{
  imports = [
    (modulesPath + "/installer/scan/not-detected.nix")
    (modulesPath + "/profiles/qemu-guest.nix")
  ];

  boot.loader.grub = {
    efiSupport = true;
    efiInstallAsRemovable = true;
  };

  # claude-code is the only unfree package the fleet pulls in.
  nixpkgs.config.allowUnfreePredicate =
    pkg: builtins.elem (pkg.pname or "") [ "claude-code" ];
}
