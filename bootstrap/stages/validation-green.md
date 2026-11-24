# Stage: validation-green

Spore's portable lint set runs over the project tree without
issues.

## Detect

`internal/bootstrap/validation_green.go`. Runs the set returned by
`internal/lints.Default()`:

- `emdash` - flags em-dashes / en-dashes in any tracked source.
- `filesize` - source files under 500 lines.
- `comment-noise` - section labels, restated bindings, undated
  TODOs, change-history comments.
- `claude-drift` - rendered instruction files (targets plus
  generated `.claude/rules/` files) consistent with composer output,
  when `rules/consumers/` is set up.
- `claude-totalsize` - root `CLAUDE.md` / `AGENTS.md` within the
  line and char caps (`spore.toml` `[lint]`, see `spore lint -h`).
- `agents-mirror` - `AGENTS.md` mirrors every tracked `CLAUDE.md`,
  byte for byte or through an `@AGENTS.md` import.

## Exit criteria

Every lint reports zero issues. Warnings (a root file in the
200-400 line grace band) do not block.

## Blocker shapes

- `lints reported issues: <name>: <count> issue(s); first: <path>:<line>: ...`
  Fix the issue; do not silence the lint. The detector tail
  references the first issue from each lint that fired.
- `<name> errored: ...` - the lint itself crashed. Open a follow-up
  task; do not skip.

## Notes recorded

`emdash: 0, filesize: 0, comment-noise: 0, claude-drift: 0,
agents-mirror: 0` (or whatever counts the lints reported). Useful in
the audit trail when comparing across bootstraps.
