**Status**: not-started

# composer: support layered rule pools

## Problem

`spore compose -rules <dir>` accepts exactly one rule pool directory.
`internal/composer/composer.go` is explicit about this:

> The rules pool has ONE source dir, no override layering. Compose
> reads only the rulesDir argument it is given: no env-var redirects,
> no ~/.config/ overlays, no per-user shadow paths.

That keeps Compose pure, which is good. But it forces every adopted
project into one of two awkward shapes:

1. **Vendor the whole rule pool**: copy `rules/core/`, `rules/lang/`,
   `rules/consumers/` into the project repo, drift over time as
   upstream changes.
2. **Upstream-host project-specific consumers + rules**: every
   downstream project has to land changes in the spore repo just to
   carry its own rule fragments. Coupling explosion.

In practice today (as of `feat/consumer-crm-gateway-ruby-client`),
downstream projects ship their generic rules via an upstream-hosted
consumer file (option 2 for the consumer), but cannot ship
project-specific rule fragments at all - they live in hand-written
`AGENTS.md` content outside the compose render. That breaks the
"edit fragments, not the rendered file" contract.

## Goal

`spore compose -rules <upstream>:<local-overlay> -consumer <name>`.
Compose reads `<name>.txt` from the consumer list at the first
matching pool. Each fragment id is looked up by walking the pool
list in order; the first hit wins. Lets a project layer its own
`projects/<slug>.md` fragments on top of upstream `core/`
fragments.

Alternative shape if `:` is too fragile: `-rules` accepts repeated
flags (`-rules <up> -rules <local>`), or a directory containing a
list file (`rules.lst`) describing the overlay chain.

## Constraints

- Compose must remain a pure function of (pool list, consumer,
  predicates). No env-var redirects, no user shadow paths.
- The override rule (first hit wins vs. last hit wins) is a design
  choice. First-hit-wins is more natural for "local overlay on
  upstream defaults"; last-hit-wins is more natural for "upstream
  patches over a vendored base". Pick one and lock it in tests.
- Detector for `validation-green` lint stage should still work
  without overlay set (single pool is the common case).

## Out of scope

- A "merge" semantic where overlay fragments are concatenated with
  upstream fragments under the same id. Doesn't compose with the
  consumer-file-as-list-of-ids model; would need a different
  rendering shape.
- Per-user overrides via `~/.config/`. Explicitly forbidden by the
  current design and we should keep it that way.

## Progress
