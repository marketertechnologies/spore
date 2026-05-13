**Status**: not-started

# secrets: layered per-project + optional global

## Problem

Spore has no opinion today about where projects keep operator-managed
secrets. The current host-side convention (`~/.config/spore/secrets.env`,
mode 0600, sourced by per-project wrappers) was fine when each host ran
one coordinator. With multiple coordinators on one host
(`crm-gateway`, `crm-gateway-ruby-client`, `marketer`, ...), one shared
file means:

1. Every project's wrappers see every project's secrets. No isolation.
2. There is no convention for project-scoped tokens. The single file
   ends up either polluted with `<PROJECT>_TOKEN` namespacing or with
   genuine collisions on common names (`GITHUB_TOKEN`,
   `BUNDLE_RUBYGEMS__PKG__GITHUB__COM`, etc.).
3. The shared coordinator role tells every project to source the same
   path, which encourages cross-project leakage.

## Goal

Document and codify a two-layer convention in spore:

- `~/.config/spore/<project>/secrets.env` — per-project, primary.
  Mode 0600, dir mode 0700.
- `~/.config/spore/secrets.env` — optional global fallback. Same mode.
  For keys genuinely shared across every spore-managed project on
  the host. Layered _under_ the per-project file: source global
  first, per-project second, so per-project entries override globals
  for any key set in both.

`<project>` is `$SPORE_PROJECT_ROOT` basename inside any
spore-spawned pane, or `git rev-parse --show-toplevel` basename for
non-spore invocations.

## Reference implementation

Already in production in `crm-gateway`:

- `bin/with-services` (`load_secrets` helper at the top of the
  `exec` subcommand) — the canonical resolver for app/test commands.
- `bin/dev/record-vitec-payloads` — the same resolver duplicated
  because the script pre-checks credentials before delegating to
  `with-services exec`.

Both share the same five-line pattern:

```sh
config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/spore"
project="$(basename "${SPORE_PROJECT_ROOT:-$(git rev-parse --show-toplevel)}")"
set -a
[ -f "$config_dir/secrets.env" ] && . "$config_dir/secrets.env"
[ -f "$config_dir/$project/secrets.env" ] && . "$config_dir/$project/secrets.env"
set +a
```

## Where this should land in spore

1. **`rules/core/secrets.md`** (new) — a short rule fragment that
   coordinators and workers pick up via `spore compose`. Describes
   the layered model, the project-name resolution rule, the
   "never echo or log" guardrail, and one canonical sourcing
   snippet.
2. **`bootstrap/stages/creds-wired.md`** update — the creds-wired
   stage already exists to record where credentials live. Add a
   bullet pointing operators at the per-project location as the
   default destination for any newly-wired credential, and note the
   global file as opt-in.
3. **`spore-coordinator-launch` shim** (in nixosModules / bundled
   flake) — currently does not touch secrets at all. Leave it that
   way. Sourcing remains a project-side concern so individual
   commands can decide whether they need secrets in env.
4. **`spore secrets path <project>` helper** (optional, nice-to-have)
   — emits the resolved per-project path so wrappers don't all
   duplicate the resolver. Defer until the convention has at least
   two downstream consumers.

## Acceptance

- A fresh `spore bootstrap` walks the operator through creating the
  per-project secrets directory (mode 0700) with the right name, and
  records the path in `info-gathered.json` so future agents know
  where to look without re-asking.
- The shared coordinator role (rendered into
  `~/.config/spore/coordinator-role.md` by the bundled flake) cites
  the per-project location as primary and the global as opt-in
  fallback.
- A canonical sourcing snippet exists in `rules/core/secrets.md`
  that projects can paste verbatim into a `bin/with-services`-style
  wrapper. The crm-gateway implementation continues to work
  unmodified after upstream adopts it.

## Cross-references

- crm-gateway commit `96fc1127 chore(secrets): layered per-project
  secrets resolver` — landed the resolver + CLAUDE.md update.
- crm-gateway `CLAUDE.md` "Credentials & secrets" section — the
  project-side write-up of the convention.
- Host-level shared role at `~/.config/spore/coordinator-role.md`
  "Operating regime" — updated in lockstep to describe the layered
  model.
- Related rules: `rules/core/role.md` (where the coordinator role
  base lives), `bootstrap/stages/creds-wired.md` (where the
  credentials-tracking stage gate lives).
