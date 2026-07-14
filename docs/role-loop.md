# Role-based fleet: operator guide

The role loop runs one task through an engineer pane and two reviewer
panes. Roles never see each other; they talk through files under
`.spore/<slug>/`. The driver derives all state from that tree, so
every command here is safe to re-run.

## Minting a task

Create `tasks/<slug>.md` as usual (`spore task new`). The frontmatter
is the standard task set (`status`, `slug`, `title`, `created`,
`project`); the body below the frontmatter becomes the spec.

Opt-in is the presence of `.spore/<slug>/` on disk. Seed it while the
task is still a draft (`role-drive` has no status gate):

    spore task role-drive <slug>

Then flip the frontmatter to `status: active` by editing the file,
not via `spore task start` (start spawns a homogeneous worker
session). Order matters: the fleet reconciler skips role-looped slugs
when minting homogeneous workers, but it detects them by the
`.spore/<slug>/` dir. An active slug without the dir gets a
homogeneous worker on the same `wt/<slug>` branch, and that worker
stays alive once the dir appears. Seed first, activate second.

`role-drive` is also the manual driver: each run advances the loop by
one idempotent tick. The reconciler calls the same drive for
role-looped slugs on every pass, so an active task keeps moving
without manual ticks.

The first tick:

- caches the task body (frontmatter stripped) at
  `.spore/<slug>/spec.md`; the cache is immutable for the run
- creates the `wt/<slug>` branch and worktree at `.worktrees/<slug>/`
- spawns the engineer pane, tmux session
  `spore-role/<project>/<slug>/engineer`

## The phase machine

Phases derive from artifact counts on disk; writing a file causes the
transition. Verdict files hold `approve` or `request_changes`.

| Phase | Meaning | Entered by writing |
|---|---|---|
| `engineer-initial` | no engineer response yet | (start state) |
| `review-A` | A owns the next verdict | `responses/engineer-round-N.json` |
| `engineer-revise-A` | A requested changes | `reviews/A/round-N.json` (request_changes) |
| `review-B` | A approved; B owns the verdict | `reviews/A/round-N.json` (approve) |
| `engineer-revise-B` | B requested changes | `reviews/B/round-N.json` (request_changes) |
| `done` | B approved; branch ready | `reviews/B/round-N.json` (approve) |
| `escalated` | cap hit; operator decides | request_changes at round 3 |

A revise phase flips back to the review phase once the engineer
writes the response to the latest verdict. Each reviewer phase is
capped at 3 rounds (`MaxReviewerRounds`): a `request_changes` verdict
at round 3 escalates instead of looping. On escalation the driver
leaves all panes alive; the operator can clarify the spec,
force-approve, kill the task, or hand it back for another loop.

Per-phase side effects of a drive tick:

- engineer phases: spawn the engineer pane if missing; on revise
  phases, nudge it (tmux send-keys) with the latest verdict path
- `review-A`: keep engineer and reviewer A panes alive
- `review-B`: reap reviewer A's pane, spawn reviewer B; the engineer
  pane stays alive across the handover
- `done`: write `summary.md`, clear `escalated-*` markers, write the
  `ready` marker, reap all three panes
- `escalated`: write the `escalated-*` marker, touch nothing else

## Roles

All panes run in the task worktree with `SPORE_TASK_DIR` pointing at
the absolute `.spore/<slug>/` tree, plus `SPORE_TASK_SLUG`,
`SPORE_PROJECT_ROOT`, `WT_PROJECT`, and `SPORE_ROLE` in the
environment. Role bodies come from `bootstrap/roles/<role>.md`.

Engineer (`spore-role/<project>/<slug>/engineer`): reads the spec and
the latest verdict, commits on the task branch, and ends each round
by writing `responses/engineer-round-N.json`:

    {
      "addressed": ["comment 1 -> commit X"],
      "pushback": ["comment 3 -> disagree because Z"],
      "notes": "free-form context for the reviewer"
    }

Reviewer (`spore-role/<project>/<slug>/reviewer-<instance>`): one
body, two instances, selected by `SPORE_REVIEWER_INSTANCE` (A or B).
A persists across its rounds and reads the engineer's responses. B
spawns fresh after A approves; round 1 is fresh eyes (no A history,
no engineer responses), rounds 2+ read only the engineer's replies to
B's own verdicts. Each round a reviewer writes
`reviews/<instance>/round-N.json`:

    {
      "verdict": "approve" | "request_changes",
      "summary": "one-line overall take",
      "comments": ["one string per concern"]
    }

## Observing a running loop

`spore task status <slug>` prints the derived snapshot: phase,
engineer round, current reviewer, reviewer round, last verdict.

Markers under `.spore/<slug>/state/`:

- `woken-<reviewer>-<round>-<stamp>`: the engineer was nudged for
  this verdict. The stamp is the pane's tmux `session_created`, so a
  respawned engineer pane gets re-nudged on the next tick.
- `escalated-<reviewer>-<round>`: the loop escalated. Cleared when
  the loop later reaches done (operator force-approve).
- `ready`: the loop reached done; the branch awaits the operator.
  Cleared by `spore task done`.

`spore task waybar` renders `d/a/p/b/e/r` counts (draft, active,
paused, blocked, escalated, ready). Escalated and ready count marker
files regardless of frontmatter status. The chip CSS class is the
highest-priority nonzero bucket: `escalated`, then `blocked`, then
`ready`, then `active`, else `idle`.

`summary.md` lands in `.spore/<slug>/` when the loop hits done: both
review threads (verdict, summary, comments per round) plus every
engineer response. It embeds a fingerprint of the artifact counts and
regenerates if the tree changes under it.

## Finishing

When the loop is done, review the branch (`summary.md` is the digest)
and merge with `spore task merge <slug>`. Then run:

    spore task done <slug>

Done flips the status, kills the homogeneous worker session, removes
the worktree and the `wt/<slug>` branch, deletes `tasks/<slug>.md`,
and clears the `state/ready` marker. It refuses while the branch has
unmerged commits (`--force` discards them). It does not kill role
panes: its session cleanup matches `<project>/<slug>` at a name
boundary, which `spore-role/...` names never hit. On the normal path
that is moot (the done tick already reaped all three panes), but when
you abandon an escalated task, kill the surviving panes yourself with
`tmux kill-session -t spore-role/<project>/<slug>/<role>`.

The rest of `.spore/<slug>/` is preserved: `spec.md`, `responses/`,
`reviews/`, `summary.md`, and the remaining state markers stay on
disk as the task's audit trail.
