# agenix rules: which recipients encrypt which .age file. The `agenix`
# CLI reads this (not the NixOS build) to know who can decrypt; keep it
# in sync with recipients.txt. Run `agenix -e <file>.age` from this
# directory to edit a secret.
#
# Placeholder keys until the rocky box exists; see recipients.txt for
# the first-deploy flow that replaces them.
let
  operator = "age1placeholderoperatorxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx";
  rocky = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPLACEHOLDERrockyhostkeyxxxxxxxxxxxxxxxxxxxxxx rocky";

  all = [ operator rocky ];
in
{
  "linear-api-key.age".publicKeys = all;
}
