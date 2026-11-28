Rule-pool fragments. Markdown files here are composed into a project's
`CLAUDE.md` / `AGENTS.md` by `internal/composer`. `core/` holds always-on,
language-agnostic rules; `lang/` holds language-specific fragments
selected per project (`lang/` is empty until per-language fragments
land). Per-consumer rule lists live in `consumers/<name>.txt`, one
fragment id per line.

## Path-scoped fragments

A fragment that only matters in one area of the codebase declares the
globs it applies to in frontmatter, using Claude Code's rule format:

```markdown
---
paths: ["app/graphql/**", "spec/graphql/**"]
---

# GraphQL
...
```

`paths` accepts a flow list, a block list (`paths:` then `  - "glob"`
lines), or a comma-separated string. It is the only frontmatter key.
`spore compose --consumer <name> --write` renders such a fragment to
`.claude/rules/<fragment-id>.md` (frontmatter, then the generated-from
marker, then the body) instead of the root file, so Claude Code loads
it only when it reads or edits a matching file. Fragments without
`paths` stay in the root file. Plain `spore compose` (stdout) prints
the root body only.

`--write` also deletes stale generated rule files: any `.md` under
`.claude/rules/` whose first line after the frontmatter is a
`<!-- generated from ... -->` marker and that the consumer no longer
renders. Hand-written rule files and symlinks are never touched.

Codex does not read `.claude/rules/`, so path-scoped fragments are
Claude-only.

## Consumer directives

Comment lines in a consumer file carry directives; compose skips them
when rendering:

- `# target: <path>` (repeatable): an output file. Opts the consumer
  into the `claude-drift` lint. `--write` defaults to `CLAUDE.md` and
  `AGENTS.md` when none is set.
- `# mirror: copy|import`: how the `CLAUDE.md` / `AGENTS.md` pair is
  rendered. `copy` (default) writes the full body to every target.
  `import` writes it to `AGENTS.md` only and makes each sibling
  `CLAUDE.md` a stub holding the marker and an `@AGENTS.md` import;
  Codex reads `AGENTS.md` directly and Claude Code expands the import.
