# Updating claude-code on a spore host

Three ways to move the claude-code CLI to a newer version on a
spore-managed host. They differ in where the version is pinned, how
fast new releases reach you, and blast radius. Pick by how much you
want claude-code's cadence coupled to nixpkgs vs. to spore.

## Where the version comes from today

On this host claude-code is sourced from **nixpkgs**
(`pkgs.claude-code`), wired in two places in
`/etc/nixos/configuration.nix`:

- `environment.systemPackages` -> the interactive CLI on `PATH`.
- `services.spore-fleet.claudeCodePackage = pkgs.claude-code` -> the
  binary the fleet shims spawn for coordinators and workers.

That second line is an explicit override. The spore module
(`nixosModules.spore-fleet`) already defaults `claudeCodePackage` to
`sadjow/claude-code-nix` (spore's `flake.nix` carries that input);
the host opts out of the default and tracks nixpkgs instead.
`claude-code` therefore appears in `/etc/nixos/flake.lock` anyway, but
only transitively through the `spore` input, unwired.

`nixos-rebuild switch` is operator-run on this host (the coordinator
has no sudo). The commands below are the operator's to execute; the
agent prepares the edits.

## Option 1: bump nixpkgs (status quo source)

Because `pkgs.claude-code` resolves through the `nixpkgs` input,
updating that input pulls whatever claude-code is current in
nixos-unstable.

```sh
cd /etc/nixos
nix flake update nixpkgs          # or: nix flake lock --update-input nixpkgs
nixos-rebuild switch --flake .#spore-bootstrap
```

- Pro: no new input, one knob, nothing to wire.
- Con: nixos-unstable's claude-code can lag the latest npm release by
  days. Bumping `nixpkgs` also moves every other package in the
  closure, so the rebuild is large and carries unrelated change.

Reach for this when you want claude-code current-ish and are due for a
general system bump anyway.

## Option 2: source from sadjow/claude-code-nix in the host flake

`sadjow/claude-code-nix` packages claude-code directly from upstream
releases and updates fast, often same-day. Declare it as a real input
of the host flake and wire it to both consumers.

In `/etc/nixos/flake.nix`, add the input:

```nix
inputs.claude-code = {
  url = "github:sadjow/claude-code-nix";
  inputs.nixpkgs.follows = "nixpkgs";
};
```

The host flake already passes `inputs` through `specialArgs`, so
`/etc/nixos/configuration.nix` can read it. Point both uses at the new
input:

```nix
# environment.systemPackages:
inputs.claude-code.packages.${pkgs.stdenv.hostPlatform.system}.default

# services.spore-fleet:
claudeCodePackage =
  inputs.claude-code.packages.${pkgs.stdenv.hostPlatform.system}.default;
```

Bump with:

```sh
cd /etc/nixos
nix flake update claude-code
nixos-rebuild switch --flake .#spore-bootstrap
```

- Pro: newest releases quickly, decoupled from the nixpkgs bump
  cadence. A claude-code bump touches only that input's lock entry.
- Con: one more input to own. The pin is host-local, so every host
  bumps independently and can drift from the rest of the fleet.

Reach for this when you want claude-code on its own fast track and are
fine managing the version per host.

## Option 3: attach the version to spore

Make spore the single source of truth: spore's `flake.nix` pins
claude-code, and every consumer (this host, marketer, crm-gateway)
inherits whatever spore ships. spore is already most of the way there
-- its `flake.nix` declares the `claude-code` input and the
`spore-fleet` module defaults `claudeCodePackage` to it.

What it takes:

1. In spore's `flake.nix`, expose claude-code as a package output (it
   is currently only an `app`), so consumers can put it on `PATH`:

   ```nix
   packages = {
     inherit spore shims;
     claude-code = claude-code.packages.${system}.default;
     default = spore;
   };
   ```

2. In `/etc/nixos/configuration.nix`, drop the
   `claudeCodePackage = pkgs.claude-code` override so the module
   default (spore's pinned claude-code) flows through, and point
   `environment.systemPackages` at spore's package:

   ```nix
   inputs.spore.packages.${pkgs.stdenv.hostPlatform.system}.claude-code
   ```

Bump by updating spore's input and releasing spore, then pulling the
new spore on the host:

```sh
# in the spore repo
nix flake update claude-code
just release X.Y.Z        # operator inspects, pushes commit + tag

# on the host
cd /etc/nixos
nix flake update spore
nixos-rebuild switch --flake .#spore-bootstrap
```

- Pro: one pinned claude-code version across the whole fleet, shipped
  and versioned with spore. Consumers stop managing it individually;
  the coordinator and the interactive CLI are guaranteed to match.
- Con: a bump rides a spore release cycle, coupling claude-code
  cadence to spore's. Every consumer moves together, so there is no
  per-host pinning.

Reach for this when fleet-wide consistency matters more than per-host
control, and you want claude-code's version to be a property of the
spore release.

## Related

- `docs/host-nix-snippet.md` -- how the host flake imports
  `nixosModules.spore-fleet` and where `claudeCodePackage` is set.
- `docs/migrations.md` -- host-state migration engine, if a future
  bump needs an activation-time fixup.
