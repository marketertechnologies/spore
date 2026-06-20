_:

# agenix wiring for rocky. Each `age.secrets.<name>` declares a secret
# the host decrypts at activation time from the matching .age file under
# ../../../secrets/, encrypted to the recipients in secrets/recipients.txt
# (the host's own ssh_host_ed25519_key plus the operator's age key).
# Building system.build.toplevel does NOT decrypt; only `nixos-rebuild
# switch` / `colmena apply` on the live host does. So this evaluates and
# builds before the box (and its host key) exist; the operator runs the
# one-time encrypt step (see docs/deploy.md) before the first deploy.

let
  secretsDir = ../../../secrets;
in
{
  age.identityPaths = [ "/etc/ssh/ssh_host_ed25519_key" ];

  # The ROC Linear OAuth actor-app token the fleet drives as "rocky".
  # Consumed by the spore-fleet systemd-user unit via LoadCredential
  # (matters.linear.credentialFiles.api_key in ./default.nix), so the
  # value reaches the loader by file path and never sits in the unit
  # environment or the nix store.
  age.secrets.linear-api-key = {
    file = "${secretsDir}/linear-api-key.age";
    owner = "spore";
    group = "users";
    mode = "0400";
  };
}
