**Status**: not-started

# bootstrap: Rails-aware consumer template

## Problem

`spore bootstrap` does not scaffold a project's rules pool. The eight
default stages (`repo-mapped` through `worker-fleet-ready`) cover
detection, alignment, and worker readiness, but none of them emits a
`rules/` directory or a `rules/consumers/<name>.txt` for the newly
adopted project. That work happens manually after bootstrap, by the
operator or coordinator.

The immediate consequence: a new Rails bootstrap does not pick up
`rules/lang/rails-migrations.md` automatically. The fragment is
present in the rule pool and the composer's `?rails` predicate fires
when the project is detected as Rails, but no consumer txt exists yet
to reference the fragment. Someone has to author the consumer txt.

The same gap will reappear for every per-language rule we ship.

## Goal

When `spore bootstrap` adopts a Rails project, the project leaves the
walk with a working consumer txt that already references
`?rails lang/rails-migrations` (and any other Rails-aware fragments
shipped at the time), wired to render to `CLAUDE.md` and `AGENTS.md`.

## Shapes to consider

1. **New stage `consumer-scaffolded`** between `repo-mapped` and
   `info-gathered`. Detector checks for `rules/consumers/<slug>.txt`;
   when absent, emits a template based on detected project kind
   (Rails / Go / etc.) and the kernel's default rule list. Idempotent
   on re-runs.
2. **Subcommand `spore rules init`**, called by a stage rather than
   being a stage itself. Separates the action ("emit a consumer
   template") from the gate ("project has a rules pool"). Easier to
   re-run manually when the project shape changes (e.g., a Rails app
   later adds a Go subdir).
3. **Fold into `repo-mapped`**. That stage already inspects the tree
   for shape markers. Adding "and emit a default consumer txt" makes
   it do two jobs in one. Cheapest in stage count; muddies the gate.

## Constraints

- Must not overwrite an existing consumer txt; templates only fire on
  absence (idempotent).
- The template content lives in `bootstrap/` (embedded), not in the
  rule pool itself. Mixing templates into `rules/` would confuse the
  composer's "rule fragment" semantics.
- Detection must distinguish Rails (Gemfile + config/application.rb)
  from plain Ruby gems (Gemfile only) - reuse `internal/lang.IsRails`
  rather than re-implementing.
- Stage gates remain additive (a re-run of `spore bootstrap` does
  not regress).
- Cross-language projects (e.g., Go backend + Rails admin) need a
  composition story. Likely: multiple language predicates can fire,
  the template lists each conditional fragment with its own
  `?<lang> <id>` line. Worth deciding before shipping the template
  format.

## Out of scope

- A full DSL for "if Rails then X, if Go then Y" in the template
  itself. The predicate gating already handles per-language
  inclusion; the template just needs to list the candidate lines.
- Automatic upgrades of existing consumer txts when new
  per-language fragments ship. That's the job of host migrations
  (e.g., `bootstrap/migrations/002-rails-rule-pickup.sh`).

## Progress
