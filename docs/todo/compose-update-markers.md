**Status**: not-started

# composer: in-place marker re-render

## Problem

`spore compose` renders to stdout. Operators redirect to
`AGENTS.md` / `CLAUDE.md`. As soon as the project wants any
project-specific content alongside the rendered rules, the file
becomes a hybrid:

```
<<rendered rules>>

# Project: <name>
<<hand-written content>>
```

Re-rendering after an upstream rule fragment changes means either
- regenerate the whole file and lose the project content, or
- diff-and-paste manually.

Today's convention (introduced in `feat/consumer-crm-gateway-ruby-client`):

```
<!-- BEGIN spore-compose:<consumer> -->
<<rendered>>
<!-- END spore-compose -->

# Project: <name>
<<hand-written content>>
```

Markers preserve the project content but require manual splicing.

## Goal

`spore compose --update <file>` opens an existing file, locates the
`<!-- BEGIN spore-compose:<consumer> -->` / `<!-- END spore-compose -->`
markers, and replaces the content between them with a fresh render.

- Block uses the consumer name embedded in the BEGIN marker.
- Read `-rules` and predicate state the same way the stdout-mode
  invocation does (`align.Active(cwd)`).
- Multiple files in one invocation: accept a list of paths.
- Idempotent: re-running on an up-to-date file should leave it
  byte-identical (lets CI assert "no drift" cheaply).

## Constraints

- No silent edits without markers. If markers are missing, error
  with the marker spec in the message; don't silently
  overwrite the whole file.
- Preserve trailing newline shape of the surrounding content.
- Compose-via-stdout (current default) must still work.

## Lint integration

A new lint check (`compose-drift`) can:
1. Locate every file with a `BEGIN spore-compose` marker.
2. Run compose for the named consumer.
3. Compare against the on-disk content between markers.
4. Fail on drift.

Lets `spore lint` catch stale renders the same way `claude-drift`
already catches stale `AGENTS.md` mirrors.

## Progress
