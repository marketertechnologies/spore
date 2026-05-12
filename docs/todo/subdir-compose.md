**Status**: not-started

# composer: sub-directory AGENTS.md rendering

## Problem

`core/tier-policy.md` describes the intended layout:

> Rules tier into root `CLAUDE.md` / `AGENTS.md` (project-wide), subdir
> instruction mirrors (single-area, under 150 lines), `docs/<topic>.md`
> (rationale and debugging notes), and `docs/todo/<slug>.md`
> (multi-session specs, each starting with a `**Status**:` header).

But `spore compose` only knows how to render root-level
`AGENTS.md` / `CLAUDE.md`. Sub-directory mirrors are entirely
hand-written. That is fine for content that is genuinely
single-area (a `lib/foo/AGENTS.md` covering only the `foo`
subtree), but it means there is no compose-mediated way to share
*generic* rules across sub-directory files (e.g., a shorter
"writing style" reminder, or the project's commit-identity block).

A real example from `crm-gateway-ruby-client`:
- `lib/crm_gateway_client/rpc/AGENTS.md`
- `lib/crm_gateway_client/notifications/AGENTS.md`
- `spec/AGENTS.md`

All three hand-written, with no mechanism to pull in a small set
of shared fragments (a per-subdir header that points back to the
root, for instance).

## Goal

`spore compose -consumer <consumer> -target subdir/AGENTS.md` reads
a consumer file shaped like the root one (one rule id per line) but
intended for sub-directory placement. Default contents are
shorter, defer the role/writing-style blocks to the root, focus on
the subtree's responsibilities.

Possible shape: a `subdir/` namespace under `rules/` for fragments
specifically intended for sub-directory mirrors (size-budgeted,
written in the second-person-imperative voice common to subdir
docs).

## Constraints

- Tier-policy's "subdir mirrors under 150 lines" must still apply.
  The lint check should refuse to render a subdir AGENTS.md that
  exceeds the budget.
- Sub-directory consumers must compose with the project-overlay
  proposed in `compose-overlay.md` (so a project can ship its own
  `subdir/` fragments).
- Idempotent re-render with the marker convention from
  `compose-update-markers.md`.

## Open questions

- Should sub-directory composes auto-emit a "see root AGENTS.md
  for X" pointer? Or is that a fragment the consumer can opt
  into?
- File-naming: `AGENTS.md` per spec, but Claude Code also reads
  `CLAUDE.md`. Render both? Or rely on existing tooling to mirror?

## Progress
