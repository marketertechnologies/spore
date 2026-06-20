# Cross-repo worker ship (ROC-18)

**Status**: design accepted - independent sibling repos, repo-relative
ship cycle, cross-repo features decomposed into per-repo child tickets.
The spore harness repo stays code-free. No `task`/`ship`/`merge`/`gh`
internals change; the only new work is coordinator-side routing and a
target-repo field on the ticket.

## Problem

The operational model is Linear-driven and 1:1: one worker owns one
ticket. A ticket is a feature that may touch more than one repo. The ship
cycle assumes a worker is one worktree of ONE git repo: a single
`wt/<slug>` branch, a single `origin`, a single PR, a single `.git`
history. A worker can `cd` into a sibling repo and run git by hand, but
the question is how a feature spanning repos lands without falling outside
auto-PR, the merge audit, and evidence projection.

## Constraints (operator)

- The spore repo (`marketertechnologies/spore`) holds **only the spore
  harness and spore itself** - no product code vendored in. It stays
  open-source-clean.
- The other repos hold **only code**, each an independent git repo with
  its own remote and its own PRs.
- spore agents must be able to **navigate away** from the harness repo
  into a code repo to do the work.

This rules out the subtree/monorepo option (it would vendor code into the
spore repo) and the submodule option (it couples the code repos to a
parent and still needs multi-repo ship machinery). The repos stay
independent siblings.

## Key fact: the ship cycle is already repo-relative

The single-repo assumptions live entirely in the subsystems that touch
git/gh - `internal/task` (lifecycle/ship/merge), `internal/merge`
(audit/unblock), `internal/gh`. But none of them hardcode a *global*
project root. They all derive the repo from the task's location:

- `internal/task/lifecycle.go:691` -
  `ProjectRootFromTasksDir(tasksDir) = filepath.Dir(tasksDir)`. The repo a
  worker ships into is just the parent of its `tasks/` dir.
- `lifecycle.go:473-474` - worktree `<projectRoot>/.worktrees/<slug>`,
  branch `wt/<slug>`, both rooted at that derived projectRoot.
- `internal/task/ship/ship.go` and `merge.go` - all `git -C projectRoot`
  / `gh` calls (`cmd.Dir = projectRoot`) run against that same derived
  root. `origin`/`main` are the per-repo remote and base, not a global.
- The fleet already drives **multiple** independent project roots: it
  reads `~/.config/wt/projects`, one project root per line
  (`internal/fleet/livenessstatus.go:58-105`), and walks each for
  liveness/reap/wake (`livenessstatus.go:251-263`,
  `internal/fleet/wake.go:26` uses `<projectRoot>/tasks`).
- `internal/evidence/verify.go` is purely structural (never shells git)
  and already cross-repo-aware (`isCrossRepoRest`,
  verify.go:73-75,192-204). `internal/wtcheck` runs `nix develop -c just
  check` in whatever root it is handed (wtcheck.go:42). Neither needs a
  change.

So "one worker = one repo = one worktree = one branch = one PR" holds per
ticket *for whatever repo the ticket's tasks dir lives in*. The harness
does not need a global monorepo; it needs each ticket pointed at the right
code repo.

## Design

### Layout

A host has a workspace directory holding the spore harness repo and each
code repo as independent siblings:

```
workspace/
  spore/        # harness repo (this repo); NOT in the projects list
  repo-a/       # code repo, own remote + PRs; has repo-a/tasks/
  repo-b/       # code repo, own remote + PRs; has repo-b/tasks/
```

`~/.config/wt/projects` lists the **code repos** (`repo-a`, `repo-b`),
not the spore repo. The coordinator runs from a pinned spore binary
(per the bootstrap rule: the fleet that schedules workers must not change
under them) and serves all listed code repos.

### One ticket -> one repo

A ticket names its target code repo (a `repo:` / `target_repo` field,
resolvable to a project root in the list). The coordinator mints the task
file into that repo's `tasks/<slug>.md`. The worker spawns into
`<repo>/.worktrees/<slug>`; its branch, PR, merge audit, and evidence all
run inside that code repo with no code change - this is exactly the
existing per-project flow the fleet already supports. The agent "navigates
away" by `cd`-ing into the code repo's worktree; spore is invoked by path,
never vendored.

### A feature that spans repos -> per-repo child tickets

A single worker does NOT ship across two repos in one branch/PR (that is
the N-PR-per-worker machinery this design avoids: per-repo `(remote,
base)` plumbing, a persisted per-repo PR set, per-`.git` audit, N-CI
reconciliation - multi-week work thrown away if the repos ever merge).

Instead the coordinator **decomposes** a cross-repo feature into one child
ticket per repo, each shipped 1:1 by its own worker, linked by a Linear
dependency (parent feature -> per-repo children, blocked-by edges for
ordering). Each child is a normal single-repo ship. The parent feature is
done when its children are. This keeps every gate (wt-check, evidence,
merge audit) valid as-is and preserves the 1:1 model.

Cross-repo interface contracts (repo-a calls a new repo-b endpoint) are
handled by ordering the children: ship the provider repo first, then the
consumer, via the blocked-by edge - the same way a single repo sequences
dependent commits.

## What this requires

No change to `task`/`ship`/`merge`/`gh`/`evidence`/`wtcheck` internals.
The new work is coordinator-side:

1. **Projects list = code repos.** Bootstrap/infect writes
   `~/.config/wt/projects` with the code repos (sibling dirs), excluding
   the spore harness repo. (Mechanism already exists;
   `livenessstatus.go:58-105`.)
2. **Target-repo field on the ticket.** A `repo:` value the coordinator
   maps to a project root, so it mints `tasks/<slug>.md` into the right
   code repo. Default to the sole code repo when only one is served.
3. **Cross-repo decomposition in the coordinator brief.** When a feature
   touches multiple repos, the coordinator creates per-repo child tickets
   with blocked-by ordering instead of handing one worker two repos. This
   is brief/runbook guidance plus (optionally) a Linear helper, not
   kernel code.

## Open questions

- **Decomposition: automatic vs operator-driven.** Start operator/coord
  driven (the coordinator proposes the split, the operator confirms the
  child set). Automating the split from a feature description is a later
  refinement, not needed to land the model.
- **Shared-interface contracts across child tickets.** Blocked-by
  ordering covers sequencing; if a contract needs to be pinned (an API
  schema, a proto), reference it from both child briefs. No harness
  mechanism needed beyond the dependency edge.
- **Whether code repos ever merge into one product monorepo.** Out of
  scope here and not required: this design works for N independent repos.
  If they later merge, cross-repo features simply become intra-repo and
  the child-ticket decomposition stops being needed - no harness rework
  either way.
