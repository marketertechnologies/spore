{ lib, ... }:

# disko layout for the rocky coordinator host: one GPT disk, a 1M BIOS
# boot partition, a 512M ESP, and an ext4 root filling the rest. Matches
# the bundled bootstrap flake (bootstrap/flake/disk-config.nix) so a
# nixos-anywhere install and a later colmena rebuild agree on the disk.
# The device defaults to /dev/sda; edit this value for a box whose root
# disk enumerates differently (e.g. /dev/nvme0n1). It is read only by
# disko at install time, not by a steady-state `nixos-rebuild switch`.

{
  disko.devices = {
    disk.disk1 = {
      device = lib.mkDefault "/dev/sda";
      type = "disk";
      content = {
        type = "gpt";
        partitions = {
          boot = {
            name = "boot";
            size = "1M";
            type = "EF02";
          };
          esp = {
            name = "ESP";
            size = "512M";
            type = "EF00";
            content = {
              type = "filesystem";
              format = "vfat";
              mountpoint = "/boot";
            };
          };
          root = {
            name = "root";
            size = "100%";
            content = {
              type = "filesystem";
              format = "ext4";
              mountpoint = "/";
              mountOptions = [ "defaults" ];
            };
          };
        };
      };
    };
  };
}
