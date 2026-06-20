# Cross-repo worker ship (ROC-18)

**Status**: design accepted - subtree-vendored monorepo. No ship-cycle
code changes required for the near term; flow-back tooling deferred to a
follow-up ticket.

## Problem

The operational model is Linear-driven and 1:1: one worker owns one
ticket. A ticket is a feature that may touch more than one repo. But the
ship cycle assumes a worker is one worktree of ONE git repo: a single
`wt/<slug>` branch, a single `origin`, a single PR, a single `.git`
history. A worker can `cd` into a sibling repo and run git by hand, but
the harness only ships the project-root repo's branch - commits made in a
sibling repo fall outside auto-PR, the merge audit, and evidence
projection.

Where "one repo" is hardwired today (from the ship-cycle map):

- `internal/task/lifecycle.go:473-474` - worktree is
  `<projectRoot>/.worktrees/<slug>`, branch is `wt/<slug>`. One repo
  root, one branch per slug.
- `internal/task/ship/ship.go:111-149` - `push origin <branch>`,
  `CreatePR(projectRoot, branch, base, ...)`, then ff-merge
  `origin/<base>` and delete the branch. One remote (`origin`), one base
  (`main`), one PR.
- `internal/task/merge.go:103-124` - merge requires `main` checked out at
  `projectRoot`, pushes `origin main:main`.
- `internal/merge/audit` and `internal/merge/unblock` - every git probe
  runs `git -C <Root>`; one `.git`.
- `internal/gh/gh.go` - every `gh` call sets `cmd.Dir = projectRoot`; gh
  infers the repo from cwd. PR number is the only handle and is
  rediscovered via `gh pr view <branch>`, never persisted per repo.
- `internal/evidence/verify.go:73-75,192-204` - the evidence contract is
  already cross-repo-aware: `isCrossRepoRest` *detects* `<repo>:<ref>`
  tokens and forge URLs and emits a `CrossRepo` verdict. It is purely
  structural (never shells git), so it needs no change either way.

So the only subsystems that hard-assume one repo are the ones that touch
git/gh directly: task lifecycle/ship/merge, merge audit/unblock, gh.
Evidence and wt-check are already repo-agnostic.

## Topology decision already taken

Single coordinator per host; multiple repos served by an umbrella
`projectRoot` with sub-repos as subdirs (proto-monorepo). Eventual end
state is a true monorepo. ROC-5 landed the single-coordinator collapse
(PR #48). This design only has to choose how the subdirs relate to git,
not whether to have an umbrella.

## Options

### A. Subtree-vendored monorepo (chosen)

Sub-repos are imported into the umbrella's own history as ordinary
subdirectories (via `git subtree add --prefix=<sub> <sub-remote> <ref>`,
or a plain squashed import for repos whose history we do not need). After
import the umbrella has ONE `.git`. A cross-repo feature is edits across
two subdirs in one worktree, one `wt/<slug>` branch, one PR.

- Ship cycle works **unchanged**. Every hardwired "one repo" assumption
  above is satisfied because there genuinely is one repo. No code in
  task/ship/merge/gh/audit has to learn about multiple repos.
- Matches the stated end state (true monorepo) - this *is* the end state,
  reached incrementally one `subtree add` at a time.
- Cost: sub-repos lose independent per-repo PRs. Changes made in the
  umbrella must be flowed back to a sub-repo's own remote (if it still
  has consumers) with `git subtree push --prefix=<sub> <sub-remote>
  <branch>`. That flow-back is the only new machinery, and it is a
  *post-merge* concern, not part of the worker ship cycle.

### B. Submodules / side-by-side clones (rejected for now)

Sub-repos stay independent git repos with their own remotes and PRs; the
umbrella references them (submodule pointers or sibling clones).

- Matches "keep multiple repos with independent remotes" literally.
- But the ship machinery does NOT span them. Landing this needs real
  harness work in exactly the subsystems enumerated above:
  - branch naming gains a repo discriminator (`wt/<repo>/<slug>`);
  - ship/merge take a per-repo `(remote, base)` instead of hardcoded
    `origin`/`main`, and loop over N repos producing N PRs;
  - the task file must persist a per-repo PR set (today the PR is
    rediscovered from the single branch);
  - merge audit and unblock must run per `.git` and aggregate;
  - gh calls must target each repo's cwd and reconcile N CI runs.
  That is a multi-week build that we would throw away when the monorepo
  end state arrives. Deferred unless an external consumer forces a
  sub-repo to keep a live independent remote.

## Decision

Adopt **A: subtree-vendored monorepo**. It reaches the declared end state
directly, requires zero ship-cycle changes, and keeps every existing gate
(wt-check, evidence, merge audit) valid as-is. Submodule multi-repo ship
(option B) stays unbuilt; revisit only if a sub-repo must retain a live
independent remote with its own PR review.

## What this requires

Near term (this design's scope) - nothing in the ship cycle. To onboard a
second repo under the umbrella:

1. `git subtree add --prefix=<sub> <sub-remote> <ref>` (squash if the
   sub-repo's history is not worth carrying). Commit on the integration
   branch.
2. Workers now edit `<sub>/...` like any other path; the existing
   `wt/<slug>` + one-PR flow ships cross-repo features with no change.
3. Briefs that reference a sub-repo path use the normal slug; evidence
   that cites a sub-repo ref still parses (CrossRepo verdict is benign
   here because it is all one git history - the `<repo>:` token is just a
   path).

Follow-up ticket (deferred, not blocking): **flow-back tooling**. A
`spore subtree push` wrapper around `git subtree push --prefix=<sub>
<sub-remote> <branch>` so post-merge umbrella changes can be mirrored to a
sub-repo's origin while that origin still has external consumers. Until a
sub-repo has such a consumer, the umbrella is the source of truth and no
flow-back is needed.

## Open questions

- **History import**: squash vs full history per sub-repo. Default to
  squash (`--squash`) unless a sub-repo's line history is needed for
  blame; full import bloats the umbrella and complicates future evolved
  pulls. Decide per repo at import time.
- **Flow-back cadence**: manual `spore subtree push` on demand vs a
  post-merge hook. Defer until the first sub-repo actually needs a live
  remote; do not build the hook speculatively.
- **`.worktrees` and subtree prefixes**: confirm a worker worktree
  (`<projectRoot>/.worktrees/<slug>`) is a clean checkout of the whole
  umbrella including vendored subdirs - it is, since they are ordinary
  tracked paths. No `.worktrees` layout change.
