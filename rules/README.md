Rule-pool fragments. Markdown files here are composed into a project's
`CLAUDE.md` / `AGENTS.md` by `internal/composer`. `core/` holds always-on,
language-agnostic rules; `lang/` holds language-specific fragments
selected per project (`lang/` is empty until per-language fragments
land). Per-consumer rule lists live in `consumers/<name>.txt`, one
fragment id per line.

`composer.DefaultCore` names the core fragments every consumer list
carries. Bootstrap seeds new projects with them. For an existing
project the advisory `consumer-defaults` lint warns on each missing
one; `spore compose --consumer <name> --add-defaults` appends the ids
and copies the fragments into the project's pool for its own
coordinator to commit.
