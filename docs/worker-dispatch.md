# worker dispatch: tmux only

**Status**: design constraint, lifted from a live infect target after
a worker-launch attempt via the Claude Code Agent tool returned blocked.

## TL;DR

Worker sessions must be spawned by `task.ensureSession` (the kernel
path: `tmux new-session ... <agent>` inside the worktree cwd), or by
an equivalent operator-side `tmux new-window` that exec's the same
agent with the brief baked in. The Claude Code Agent tool is **not**
a viable worker-dispatch primitive in this harness.

## Why

The Agent tool spawns subagents in a sandbox that cannot read or
write under `.worktrees/<slug>/`. A coordinator that tries to
delegate a worker via Agent receives a "blocked" response the moment
the subagent tries to `cd` into the worktree path or read files
under it. This is independent of the kernel: the sandbox sees
`.worktrees/` as outside its allowed root.

Tmux-spawned sessions, by contrast, run as ordinary processes under
the spore user's shell. They inherit the worktree cwd, full access
to the brief at `tasks/<slug>.md` (now also copied into the worktree
itself, see `internal/task/lifecycle.go`), and can write commits on
`wt/<slug>` without sandbox interference. The headless agent then
exits, the tmux window closes, and `spore-fleet-reconcile.timer`
respawns when the task is still active.

Worker branches are local implementation detail. The worker close
path must not push `wt/<slug>` or feature branches to GitHub; only
the landed `main` branch is published upstream.

## Implications

- Anyone designing new worker-spawn paths in spore should assume the
  Agent tool is off-limits. Wrap the spawn in tmux.
- Cross-worker coordination has to flow through git (commits on
  `wt/<slug>`, observable from the coordinator's worktree), the
  `tasks/<slug>.md` brief, or files under
  `~/.local/state/spore/<project>/`. Direct in-process messaging
  between coordinator and workers is not on the table while the
  sandbox stands.
- The PreToolUse `block-bg-bash.pl` hook (in
  `bootstrap/handover/hooks/`) closes the obvious workaround of
  `Bash run_in_background:true`. Background bash silently buffers
  output where neither the operator nor the coordinator can see it.
  Tmux windows are the only sanctioned channel for long-running
  jobs.

## Agent Selection

`tasks/<slug>.md` frontmatter controls the worker process. With no
`agent:` field, Spore starts `claude-code`. `agent: claude` and
`agent: claude-code` use the same default. `agent: codex` starts:

```
codex --dangerously-bypass-approvals-and-sandbox --no-alt-screen --disable apps
```

Codex reasoning effort comes from `effort:` when present
(`low`, `medium`, `high`, `xhigh`, plus `very-high` / `very_high`
as aliases for `xhigh`). Without an explicit effort, `complexity:
light|medium` maps to `medium`, and `complexity: heavy` or an absent
complexity maps to `high`. `model:` pins the Codex model for that
task; otherwise `SPORE_CODEX_MODEL` may provide a process-wide
default, and an empty value lets Codex use its own default. The
legacy `SPORE_AGENT_BINARY` override still wins over frontmatter.

## Worktree Base

`spore task start`, the fleet reconciler and the role loop create `.worktrees/<slug>/` on branch `wt/<slug>`. An existing `wt/<slug>` branch is checked out as it is. A new one is created `--no-track` from a fresh base:

- The base branch is the task's `base:` frontmatter, else `origin/HEAD`'s branch, else `main`, else `master`.
- Spore fetches exactly that branch (`git fetch --no-tags origin +refs/heads/<b>:refs/remotes/origin/<b>`) with a 30 second deadline. Without an operator-configured ssh (`GIT_SSH_COMMAND`, `GIT_SSH`, `core.sshCommand`) the fetch runs with `GIT_TERMINAL_PROMPT=0` and ssh `BatchMode=yes`. Fetches into one repository are serialized by a flock on `<git-common-dir>/spore-fetch.lock`; a fetch that hits its deadline makes the same process skip fetching for that repository for two minutes, so a pass that spawns several workers stalls once. A fast failure is not remembered.
- The start point is `refs/remotes/origin/<b>` when it is ahead of `refs/heads/<b>`, and `refs/heads/<b>` when the local branch is equal, strictly ahead (unpushed commits), or diverged (with a warning): forking from local never drops local commits. When only one of the two exists, it is used. When neither exists, the branch forks from `HEAD` with a warning.
- A missing `origin` or a failed fetch prints one warning and the choice above runs on the refs already present. The spawn never fails because of the fetch.

`[fleet] worktree_base = "head"` in `spore.toml` restores the old behaviour, forking from the main checkout's `HEAD` without fetching. `"fresh"` is the default. Any other value, or a `spore.toml` that does not parse, warns and uses `"fresh"`.

### .worktreeinclude

A `.worktreeinclude` file at the main checkout's root lists ignored files to copy into each new worktree, such as `.env`. Only entries that `git ls-files --others --ignored --exclude-standard --directory` reports are candidates, so tracked and untracked-but-not-ignored files are never copied. A wholly ignored directory is reported once, as the directory.

Supported pattern subset:

- One pattern per line. Blank lines and lines starting with `#` are skipped; `\#` starts a literal `#`. Trailing spaces are trimmed.
- `*`, `?` and `[...]` glob as in Go's `path.Match`; none of them crosses a `/`. `**` is not supported.
- A pattern without a `/` (other than a trailing one) matches the last path element of a reported entry at any depth: `.env` matches `.env` and `apps/web/.env`.
- A pattern with a `/` at the start or in the middle is anchored to the root and matches the whole path: `/config/master.key`, `config/*.json`.
- A trailing `/` matches only directories.
- `!pattern` re-excludes; the last matching pattern wins.
- Spore never descends into a reported directory to match patterns. A directory is copied whole when a pattern matches the directory itself; otherwise nothing inside it is copied. `.env` does not pick up `node_modules/pkg/.env`; listing `node_modules/` copies all of `node_modules`.

Existing files in the worktree are never overwritten. Symlinks are recreated only when they cannot lead a worker into the main checkout: an absolute link that points into the main checkout (checked on both the given and the `EvalSymlinks`-resolved paths of the link target and the project root) and a relative link that climbs out of the checkout are skipped with a warning. A file whose destination directory resolves outside the worktree is skipped too. Copy problems warn and never fail the spawn.

## Merge Close Path

`spore task merge <slug>` fast-forwards `wt/<slug>` into the main
checkout, runs the same inbox and evidence gates as a done flip, then
commits `tasks/<slug>: status -> done`. It then runs exactly
`git push origin main:main` and verifies that `origin`'s
`refs/heads/main` matches local `main` before removing the worktree,
branch, and tmux session. It does not push `wt/<slug>`, feature
branches, or any other local branch. If a close or push gate fails,
the branch and worktree stay in place so the worker can fix the
evidence, drain the inbox, or retry the upstream push.

Before the fast-forward, `spore task merge` runs `just check` from
the worktree as a gate. The gate skips silently when the worktree
has no `justfile`, no `just` is on PATH, or the project lacks a
`check` recipe. On a red `just check` it refuses with a typed
`*task.MergeGateError` (exit 2) and leaves the branch + worktree in
place so the worker can fix the failing recipe. Pass
`spore task merge <slug> --force-merge-red <reason>` for genuine
emergencies; the bypass appends a row to
`$XDG_STATE_HOME/spore/<project>/merge-override.jsonl` so operators
can audit who shipped red and why. Hard-coded `just check` mirrors
the upstream nix-config bash gate; per-project recipe configuration
is out of scope for the initial gate.
