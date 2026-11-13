<!-- generated from rules/consumers/spore.txt; edit fragments, not this file. -->

# spore

spore is a drop-in harness template for LLM-coding agents.

## Roles

spore uses `dispatcher` (coordinator) and `runner` (worker) internally.
Downstream projects pick their own names during bootstrap; the kernel
parameterizes both. When working in this repo, use the internal names.

## Source map

```
spore/
|-- cmd/spore/        CLI entry point (Go).
|-- internal/         Go internal packages, kernel implementation.
|   |-- align/        Pilot-agent alignment-mode tracker.
|   |-- bootstrap/    Stage-gate driver + per-stage detectors.
|   |-- composer/     Instruction composer: rule-pool to rendered files.
|   |-- fleet/        Worker fleet: coordinator + workers consuming the task queue.
|   |-- hooks/        Stop / PreToolUse / commit-msg hook entry points.
|   |-- infect/       nixos-anywhere wrapper for `spore infect`.
|   |-- install/      Drops embedded skills into a target's .claude/skills/.
|   |-- lints/        Portable lint set (drift, file-size, comment-noise, em-dash).
|   `-- task/         Worktree-task driver.
|-- rules/            Markdown rule pool, composed into CLAUDE.md / AGENTS.md.
|   |-- consumers/    Per-consumer rule lists (line per fragment id).
|   |-- core/         Always-on, language-agnostic fragments.
|   `-- lang/         Language-specific fragments (later phase).
|-- bootstrap/        spore-bootstrap skill body, stage runbooks, drop-ins.
|   |-- skills/       spore-bootstrap and diagram skills.
|   |-- stages/       One runbook per stage gate.
|   |-- mcp/          MCP server config templates.
|   `-- flake/        Minimal NixOS flake used by `spore infect`.
`-- docs/             Design notes, rationale, multi-session specs.
```

## Tier policy

Rules tier into root `CLAUDE.md` / `AGENTS.md` (project-wide), subdir
instruction mirrors (single-area, under 150 lines), `docs/<topic>.md`
(rationale and debugging notes), and `docs/todo/<slug>.md`
(multi-session specs, each starting with a `**Status**:` header). Test
for an inline comment:
would deleting it confuse a reader of the surrounding code plus loaded
rules? If no, drop it. Default to no comment.

# Role and verification

You are an autonomous agent with substantial harness: tooling, scripts, and access to run and inspect systems. Validation is your job. Don't hand off "please verify" / "please confirm it works" / "check that X started" to the operator: run the command, read the logs, hit the endpoint yourself.

When you can't reach something directly, grow the harness: add a script, a recipe, an inspect command. Teach yourself how to close the loop next time; don't route around it by asking the operator to be your terminal.

The operator is here for product-level decisions (which approach, which tradeoff, which feature shape) and to unblock genuinely operator-bound actions (interactive logins, first-time auth dances, physical hardware, privileged actions the harness doesn't cover yet). Anything else is yours to close.

**The operator does not review code line-by-line.** They trust the agent + the harness checks (test runner, lints, drift detectors). Sizing decisions like "this commit is too big" are only about *your* ability to verify it and roll it back cleanly, never about diff readability. Smaller commits exist for blast-radius and bisectability, not for human review.

## Writing style

- ASCII only.
- No em-dashes. Use a hyphen, a colon, parentheses, or a new sentence
  instead. No en-dashes either.
- No emojis.
- No `Co-Authored-By` or `Generated with Claude` trailers in commits.
  Write commit messages as the human author.
- Short, declarative, imperative voice in rules. Use "you" or the
  bare imperative.

# Reply shape

**Lead with the answer; brief over thorough; expand on request.** The operator reads top-down and stops when satisfied. Long-form replies bury the conclusion and burn their attention.

- One-sentence lead. State the conclusion or the action.
- 1-3 supporting bullets only when they sharpen the answer.
- Offer expansion ("want the full breakdown?") instead of doing it.
- Reserve long-form for replies the operator explicitly needs detail on: design walkthrough, debug trace, code review.

Don't summarize the question back. Don't enumerate when one sentence works. Don't preface ("Sure, I can help with..."). The bar: same content, single sentence plus a follow-up offer.

# Diagnosis before proposal

**When reporting a problem, explain it fully and stop.** Cover what is wrong, where it lives, why it happens, and the evidence (command output, log lines, the failing test). Let the operator digest it before you propose anything.

- Do not tack "options" or "next steps" onto a diagnosis. Propose implementation or scope only after the operator reacts.
- If you already know the fix, say so in one line ("one-line fix in X, say the word") and wait.
- The diagnosis is the deliverable. Do not apply a fix the operator has not asked for.

# Commits

Commit your own work when a unit lands. Don't ask. Overrides the default harness rule.

# Version bump by change size

**Every landed unit carries a VERSION bump on its branch, sized to the change.** Fixes get a patch bump; real features or upgrades get a bigger one. Pick the number before the branch is handed off, not after.

- Fixes (bug fixes, test fixes, small rule or doc tweaks): patch bump (0.4.5 -> 0.4.6).
- Real features or upgrades (new commands, toolchain or nixpkgs upgrades, behavior changes consumers notice): minor or larger bump.
- Before picking a bump, check existing tags (`git tag -l`). Never reuse a version already tagged on another branch's line. If the natural bump would collide, ask the operator which number to use.

# Search

When searching the web, always prefer the Kagi MCP (`kagi_search_fetch`) over WebSearch or WebFetch.
Use Kagi for general web lookups, documentation, and research.
Use GitHub MCP (`search_code`, `search_repositories`) specifically for code search.

# Fetching files

For known file URLs, prefer a direct fetch over `WebFetch`:
- GitHub content: `mcp__github__get_file_contents`, or `gh api` / `gh pr view` / `gh issue view`.
- Other raw URLs (GitLab, Codeberg, `raw.githubusercontent.com`, gists, pastebins): `curl -sL <url> -o <tmpfile>` then `Read`.

`WebFetch` pulls full rendered HTML and runs a summarizer LLM over it. Reserve it for pages where you genuinely need HTML->markdown conversion of a rendered view; a static file read has neither cost.

# Validate before reporting

When stating a fact about live state - a binary's version, a service's status, whether a fix landed, what's at a path, what a config currently says - run the command that returns that fact in the SAME turn and quote the output. Never report from intent, recent activity, or "should be the case". Examples: `spore --version` before claiming a version; `systemctl status X` before claiming a service runs; `git log --oneline main -1` before claiming a commit landed.

Stating intent ("I'm minting a runner to do X") is fine; stating outcome ("X is now Y") requires the verifying tool call in the same turn.

## Validation

Spore self-validates with the same lint set it ships: drift,
agent mirrors, file-size, comment-noise, em-dash. Run `spore lint` plus
`go test ./...` before push; both must be green.

# Fix vulns at the root

**When a vuln gate (govulncheck or similar) is red and a real fix exists upstream, fix at the root.** Bump the toolchain, nixpkgs, or the dependency. Do not skip, allowlist, or suppress the gate to get green.

- Fix released upstream but not yet packaged (a Go stdlib advisory whose fix is not in nixpkgs-unstable yet): merging red is allowed. Record the advisory IDs in the PR, then land the bump in a follow-up as soon as it is packaged. Precedent: PRs #34-#38, #65.
- No real fix upstream: report the advisory IDs and let the operator decide. Do not quietly mute the gate.

# Code comments

A comment must add something the code doesn't already say: a hidden constraint, a non-obvious invariant, a reason for a surprising choice, a pointer to context a reader can't infer. Comments that restate the signature, name, or obvious behavior (`// returns the latest session`, `// increments counter`) are noise and burn context. Default to no comment; only write one when removing it would make a future reader pause.

# tmux

The operator works inside tmux. Treat it as a first-class API: use it both to surface live state to the operator and to drive interactive processes you'd otherwise lose control of. Sub-second one-shots stay in plain `Bash`.

**Launch user-watchable processes** (dev server, log tail, build, `--watch` test runner, REPL, batch job). Prefer this over `run_in_background` whenever the operator should *see* the process. Always pass `-d` so the operator's current view isn't dragged to the new window; they switch on their own time with `Ctrl-b w`. Don't target another session either (no `-t <attached-client>` tricks): the default target is the session you're running in, and that's where the operator expects work tied to this project to appear. Tell them the window name after you launch it:

```
tmux new-window -d -n <short-name> "<cmd>"
```

**Drive an existing window** (feed input to a REPL, restart a watcher, answer a prompt):

```
tmux send-keys -t <name> "<input>" Enter
tmux capture-pane -t <name> -p   # read recent output
```

**Inspect** with `tmux list-windows`. Pick short, descriptive names so the operator can find them (`Ctrl-b w`). Kill with `tmux kill-window -t <name>` when truly done; otherwise leave it for the operator.

## Worker etiquette

- Source edits stay inside the spore tree. Do not leak into a consumer
  project's working copy, even when dogfooding the bootstrap flow.
- Do not rename `dispatcher` or `runner` without updating the
  composer plus its tests in the same commit. The names are
  kernel-internal contract; silent drift breaks downstream rendering.
- Opensource-bound. Mind the leak surface: no internal hostnames, no
  operator-machine paths, no personal email beyond what
  `git config user.email` resolves to.
