# Deploying the rocky coordinator host

`nix/hosts/rocky/` is the steady-state deploy layer: one NixOS box that
runs the spore fleet against the ROC Linear team (newbuilds workspace)
as the agent "rocky". It is the open-source-clean distillation of mcom's
helm-coord pattern, minus the product surface (no web app, postgres,
caddy, or runtime container). One box, one coordinator, the worker fleet
it dispatches, and the agenix-decrypted Linear token they consume.

The layer builds before any box exists: `nix build
.#nixosConfigurations.rocky.config.system.build.toplevel` compiles with
placeholder secrets and a gitignored-but-absent `local.nix`. Activation
(decrypt + partition + reach the network) only happens on the live host.

## Layout

```
nix/
  modules/nix-housekeeping.nix   gc + optimise timers (shared)
  hosts/rocky/
    default.nix                  imports + services.spore-fleet + matters.linear
    hardware.nix                 cloud-VM profile + EFI GRUB
    disk-config.nix              disko: BIOS-boot + ESP + ext4 root
    networking.nix               DHCP, firewall (22 only), sshd
    users.nix                    spore (linger + spore-attach) + deploy (colmena)
    local.nix.example            real IP/pubkeys/disk - copy to local.nix
```

The Linear token is NOT in this repo, not even encrypted. It lives only
on the box at `/var/lib/spore-secrets/linear-api-key` (owner spore, 0400),
placed out-of-band at deploy time. The host config declares the directory
(`systemd.tmpfiles`) and references the path; the value never touches nix
or git.

`flake.nix` exposes `nixosConfigurations.rocky` (the eval / CI gate) and
a `colmena` node named `rocky` that targets the SSH alias `rocky`
(resolved operator-side, so the real IP stays out of git).

## First deploy

Operator-bound: needs a real box, the operator's keys, and the live ROC
token. None of this lives in the repo.

1. **Provision** a fresh root-reachable VM (Hetzner Cloud, x86_64).

2. **Install NixOS** over SSH with `spore infect` (wraps nixos-anywhere;
   see docs/infect.md), or `nixos-anywhere` directly against
   `.#nixosConfigurations.rocky`. disko partitions per `disk-config.nix`.

3. **Point the `rocky` SSH alias** at the box in `~/.ssh/config`:

   ```
   Host rocky
     HostName <public-ip>
     User deploy
   ```

4. **Fill `local.nix`** (gitignored) from the example:

   ```
   cp nix/hosts/rocky/local.nix.example nix/hosts/rocky/local.nix
   # set the operator + deploy authorized keys, and disk device / hostname
   # if they differ from the defaults.
   ```

5. **Deploy:**

   ```
   colmena apply --on rocky
   ```

6. **Place the ROC token on the box** (out-of-band, never in git). The
   token is the ROC Linear OAuth actor-app token (identity "rocky", team
   ROC, newbuilds), value including the `Bearer ` prefix:

   ```
   ssh deploy@rocky 'sudo install -d -o spore -g users -m 0700 /var/lib/spore-secrets'
   printf '%s' "$LINEAR_API_KEY" \
     | ssh deploy@rocky 'sudo install -o spore -g users -m 0400 /dev/stdin /var/lib/spore-secrets/linear-api-key'
   ```

7. **Bring the fleet up.** SSH in as `spore` (lands in the coordinator
   tmux session via spore-attach), make sure claude-code / codex are
   logged in, then:

   ```
   spore fleet enable && spore fleet reconcile
   ```

## How the token reaches the fleet

`matters.linear.credentialFiles.api_key =
"/var/lib/spore-secrets/linear-api-key"` wires the on-host token file into
the spore-fleet systemd-user unit via `LoadCredential`. The loader reads
it by path; the value never lands in the nix store, the unit environment,
or the repo. A `colmena apply` rebuild does not manage the file - it is
operator-placed state under /var/lib, declared only as a tmpfiles
directory.

## Optional: long-lived coordinator

By default the fleet runs the reconcile-timer watchdog model. To hold a
coordinator session open continuously with a bounded respawn, set
`services.spore-fleet.supervise.enable = true` (see
docs/coordinator-supervisor.md for the 0/1/64 exit-code contract).
