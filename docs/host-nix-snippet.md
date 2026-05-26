# Host nix snippet: manage `/usr/local/bin/spore-*` via the spore flake

For hosts already infected with `spore infect`, the six host shims
under `/usr/local/bin/spore-*` started life as `install -m 0755`
files dropped by `Handoff()` in `internal/infect/infect.go`. There
was no refresh path: changes in `bootstrap/handover/*.sh` did not
reach existing hosts even after `nix flake update spore &&
nixos-rebuild switch`. This page wires the shims into the same
nix refresh that already updates the spore binary.

Two paths, pick one:

- **Option A (preferred): import `nixosModules.spore-fleet`.** Manages
  the shims AND the systemd-user fleet reconciler. Use this on hosts
  where the spore user runs a fleet (every host this repo targets
  today).
- **Option B: paste the snippet.** Manages the shims only, no
  reconciler. Use this on hosts that want the symlink refresh but do
  not run a fleet (rare).

## Option A: import the module

```nix
{ inputs, ... }:
{
  imports = [ inputs.spore.nixosModules.spore-fleet ];

  services.spore-fleet = {
    enable = true;
    user = "spore";
    projects = {
      crm-gateway.path = "/home/spore/crm-gateway";
      # ... one entry per project under /home/spore/<name> ...
    };
  };
}
```

The module defaults `services.spore-fleet.shimsPackage` to
`inputs.spore.packages.${system}.shims` and runs the same
`/usr/local/bin/spore-*` activation script as Option B.

## Option B: paste the snippet (shims only)

### Prereqs

Your `/etc/nixos/flake.nix` must:

- declare `spore` as an input (operator already has this; today's
  binary install comes via `inputs.spore.packages.${system}.spore`);
- pass `inputs` to the NixOS module set via `specialArgs`, so
  `configuration.nix` can read `inputs.spore.packages.<system>.*`.

### Snippet

Paste into `/etc/nixos/configuration.nix` (or an imported module):

```nix
{ inputs, pkgs, ... }:
let
  system = pkgs.stdenv.hostPlatform.system;
  sporeShims = inputs.spore.packages.${system}.shims;
in {
  environment.systemPackages = [
    inputs.spore.packages.${system}.spore
    sporeShims
  ];

  # Symlink the six host shims into /usr/local/bin/ so the kernel's
  # hard-coded path (e.g. tokenmonitor.Check's respawn-pane
  # message) and the /etc/spore/coordinator.env entries written by
  # `spore infect` keep resolving. Sweeps the install-mode files
  # left over from the original infect.
  system.activationScripts.spore-shims = ''
    install -d -m 0755 /usr/local/bin
    for f in spore-attach spore-coordinator-launch spore-worker-brief \
             spore-fleet-tick spore-greet-coordinator spore-greet-worker; do
      target="${sporeShims}/bin/$f"
      link="/usr/local/bin/$f"
      if [ ! -L "$link" ] || [ "$(readlink "$link")" != "$target" ]; then
        rm -f "$link"
        ln -s "$target" "$link"
      fi
    done
  '';
}
```

## Refresh procedure

```
cd /etc/nixos
sudo nix flake update spore
sudo nixos-rebuild switch
```

After the rebuild:

- `readlink -f /usr/local/bin/spore-coordinator-launch` resolves
  into the nix store.
- Any hand-edits to `/usr/local/bin/spore-*` are wiped (sweep is
  intentional; folded source is the new contract).
- The next coordinator hard-cap respawn uses the refreshed shim.

## What this snippet does NOT cover

- Per-user hooks at `/home/spore/.claude/hooks/` and
  `/home/spore/.claude/settings.json`: these still come from the
  one-shot infect install. Not load-bearing for the cap-respawn
  flow; revisit in Phase 2.
- Per-user systemd units at
  `/home/spore/.config/systemd/user/spore-fleet-reconcile.*`: same
  status as the hooks. The kernel-managed units via
  `services.spore-fleet` are the supported path going forward.
- Fresh infects: `spore infect` still scp+installs the shims via
  `Handoff()`. The bundled flake at `bootstrap/flake/` does not
  reference the parent spore flake. Phase 2 of
  `docs/todo/host-shims-via-nix.md` covers that case.
