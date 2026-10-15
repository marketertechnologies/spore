{ lib, ... }:

# DHCP system-wide so the box picks up its address whatever the cloud
# names the NIC (eth0, enp1s0, ens3, ...). Only sshd is exposed; the
# fleet talks out (Linear, GitHub, Anthropic) and nothing talks in but
# the operator over SSH. hostName defaults to "rocky"; edit this value if
# a deployment wants a different name.

{
  networking = {
    hostName = lib.mkDefault "rocky";
    useDHCP = true;
    firewall = {
      enable = true;
      allowedTCPPorts = [ 22 ];
    };
  };

  services.openssh = {
    enable = true;
    settings = {
      PermitRootLogin = "prohibit-password";
      PasswordAuthentication = false;
      KbdInteractiveAuthentication = false;
    };
  };
}
