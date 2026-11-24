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

## Worktree Base

`internal/task/worktreebase.go` creates every task worktree. For a new `wt/<slug>` branch it fetches `+refs/heads/<b>:refs/remotes/origin/<b>` and branches from `origin/<b>` with `--no-track`, so workers start on the current upstream tip instead of a stale main checkout. `<b>` is the task's `base:` frontmatter (its stack parent) when set, else origin's default branch: `refs/remotes/origin/HEAD`, else the first of `main` / `master` known locally or as a remote-tracking ref. When the local `<b>` has commits `origin/<b>` lacks (unpushed drift auto-commits, a failed push, operator commits), the branch forks from the local `<b>` instead, so a later `spore task merge --ff-only` still applies; a local `<b>` that has diverged from origin also wins, so those workers miss the newer upstream commits. The fetch runs under a 30 s deadline with `GIT_TERMINAL_PROMPT=0` and, unless `GIT_SSH_COMMAND` or `core.sshCommand` is set, `ssh -o BatchMode=yes`; a successful fetch is reused for 30 s within one process, so a reconcile pass spawning several tasks fetches once. With no `origin` remote, a failed or timed-out fetch, spore prints one warning line to stderr and branches from the local `base:` branch when it exists, else the main checkout's `HEAD`; the spawn never fails on the fetch. An existing `wt/<slug>` branch is reused untouched. `[fleet] worktree_base = "head"` in spore.toml skips the fetch and branches from the local `base:` branch or `HEAD`; `"fresh"` or no value means the default, and any other value warns on stderr and means `"fresh"`.

After `git worktree add`, gitignored paths that match a project-root `.worktreeinclude` are copied from the main checkout into the new worktree (same idea as Claude Code's `.worktreeinclude`). Existing files in the worktree are never overwritten, and a copy error is a stderr warning, not a spawn failure. Supported syntax is a subset of gitignore:

- `#` comments and blank lines.
- A pattern without a slash matches a file or directory name at any depth (`.env`, `.env*`).
- A leading or inner `/` anchors to the project root (`/config/master.key`, `secrets/*.pem`).
- A trailing `/` matches directories only; a matched directory is copied whole.
- `!` negates; the last matching line wins.
- Globs are `filepath.Match` per path segment. `**` is not supported.

Candidates are the paths `git ls-files --others --ignored --exclude-standard --directory` reports, which collapses wholly ignored directories. Only anchored patterns select inside those (unanchored negations still apply), so `.env` does not pick up `node_modules/pkg/.env`; negations are not re-checked inside a directory that is copied whole. Tracked files and untracked files that are not ignored are never copied. An absolute symlink pointing into the main checkout is recreated pointing at the same path in the new worktree, so a worker cannot write through it into the coordinator's copy; the retargeted link dangles when its target is not copied too. Other symlinks are copied verbatim.

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
