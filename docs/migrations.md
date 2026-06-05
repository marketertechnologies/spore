# Host migrations

A Rails-style migration system for host state that lives outside the
nix derivation. Migrations cover the cases where `nixos-rebuild switch`
alone cannot converge a host: operator-owned files under
`~/.config/spore/` and `~/.claude/`, ledger entries the kernel reads,
or anything else that needs to be present-and-correct but is not pure
configuration.

## How a rebuild applies migrations

The bundled flake's `configuration.nix` carries a
`system.activationScripts.spore-migrate` entry that runs
`runuser -u spore -- /run/current-system/sw/bin/spore migrate --auto`
on every `nixos-rebuild switch`. The kernel walks the embedded
migration tree (`bootstrap/migrations/`), runs anything missing from
the ledger, and appends to the ledger on success.

This means: ship a migration in the spore repo, the next
`nix flake update spore && nixos-rebuild switch` on a downstream host
runs it, no operator action required.

Hosts whose `/etc/nixos/configuration.nix` is a hand-edited copy of
the bundled file pre-activation-hook need a one-time sync to pick up
the activation entry. After that, future migrations land
automatically.

## Authoring a migration

1. Create `bootstrap/migrations/NNN-slug.sh` where `NNN` is the next
   3-digit ordinal in the directory.
2. Write idempotent bash. The script must succeed when re-run on a
   host that's already at the target state.
3. Test locally:
   ```
   spore migrate --dry-run    # lists 'NNN-slug.sh' as pending
   spore migrate              # runs it, records in ledger
   spore migrate              # second run: no-op
   ```
4. Commit alongside the change that requires it.

## Idempotency patterns

Most migrations fall into one of three shapes. Pick the closest match
and copy.

**Append-once to a file**:

```bash
target="$HOME/.config/spore/coordinator-role.md"
marker="# spore-recipes-pointer"
grep -q "$marker" "$target" 2>/dev/null && exit 0
{
  printf '\n%s\n' "$marker"
  cat <<'EOF'
Recipes for external systems are available; run `spore recipes ls` to
list them and `spore recipes show <name>` to read one.
EOF
} >> "$target"
```

**Ensure a directory exists with given mode**:

```bash
install -d -m 0700 "$HOME/.config/spore/eslint-config_nb"
```

**Rewrite a file from a known-good template**:

```bash
target="$HOME/.claude/hooks/load-state-md.pl"
desired="$(spore-with-secrets cat /run/current-system/sw/share/spore/load-state-md.pl)"
current="$(cat "$target" 2>/dev/null || true)"
[ "$current" = "$desired" ] && exit 0
install -D -m 0755 /dev/stdin "$target" <<<"$desired"
```

## Ledger

Plain text at `$XDG_STATE_HOME/spore/migrations.applied`, one line per
applied migration:

```
NNN-slug.sh<TAB><utc-rfc3339-timestamp><TAB><spore-version>
```

Hand-edit only to recover: drop a line and the next `spore migrate`
re-runs that migration. Never reorder; chronological order is the
contract.

## Failure policy

The first non-zero-exit migration halts the run. The ledger is not
updated for the failing migration. Subsequent migrations queue as
Pending. Fix the underlying issue and re-run; the engine resumes from
where it stopped.

A migration that fails on activation does not block `nixos-rebuild
switch` itself: the activation script suppresses the engine's exit
code so a single broken migration cannot brick a host's rebuild path.
The failure is visible in `journalctl -u nixos-activation`.

## What migrations are not

- Not a replacement for `system.activationScripts.spore-shims` and
  similar nix-level convergence. Anything expressible as a nix
  derivation belongs there.
- Not data migrations. Spore does not own user data; migrations only
  touch host configuration that is conceptually nix-shaped but lives
  outside the store.
- Not a hook for ad-hoc cron jobs. Migrations run exactly once per
  host per name. Use a systemd timer for recurring work.

## Related specs

This generalises and (once mature) supersedes the open
`host-settings-via-nix` follow-up (`~/.claude/settings.json` and
`~/.claude/hooks/` refresh under `nixos-rebuild switch`). When that
work lands, it will likely be a migration plus a small nix module
piece, not a bespoke mechanism.
