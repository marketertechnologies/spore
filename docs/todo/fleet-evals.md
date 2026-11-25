**Status**: implemented in v0.34.0.

# fleet-evals: reviewer regression suite

Turn every real reviewer mistake into a repeatable case, fix it with
a rule, and keep the case so the mistake cannot come back.

## Why

Role prompts (`bootstrap/roles/reviewer*.md`) have no behavior
tests. The Go loop logic is unit-tested; what the reviewer agent
actually flags is only tested by live use. When a reviewer misses a
real defect or raises an invalid comment, the fix today is a prompt
edit plus hope. Nothing proves the edit fixed that case, and nothing
catches a later edit that undoes it.

## The loop

1. **Detect.** spore scans `.spore/<slug>/` trees for candidates.
2. **Confirm.** The operator confirms a candidate; it becomes a case.
3. **Rule.** A rule is written that should fix the case.
4. **Prove.** The suite shows the new case failing without the rule
   and passing with it, and no existing case regressing.
5. **Keep.** The case stays in the suite for good.

## Goals

- Reviewer-generic. A case belongs to whichever reviewer slot made
  the mistake (A, B, or any later slot). No slot has a fixed job in
  this design: a reviewer that approved past a real problem has a
  miss, whatever kind of problem it was (bug, spec gap, anything a
  later reviewer or a human caught).
- Two case kinds:
  - **miss**: a reviewer approved (or did not flag) a real problem.
    Expected: a `[blocker]` comment pointing at that problem.
  - **invalid**: a reviewer raised a comment that the engineer
    and/or the operator called a fluke or a non-issue. Expected: the
    comment is not raised again (at any tier).
- Candidate detection from existing artifacts, no manual digging:
  - miss: `reviews/<X>/round-N.json` approve, then a later
    request_changes from another reviewer slot (or a human PR review
    after `done`) naming a problem present at the sha X approved.
  - invalid, engineer side: a non-empty `pushback` entry in
    `responses/engineer-round-N.json`, attributed to the slot that
    owned the verdict when it was written (from the phase timeline:
    responses between X's first round and X's approve answer X).
  - invalid, operator side: `spore eval flag` marks any reviewer
    comment as a non-issue directly, with or without engineer
    pushback.
  - The operator's `spore eval confirm` turns a candidate into a
    case; nothing else records the confirmation.
- A rule promotion path that respects the leak surface (see
  Constraints): project-specific rules stay host-side; general ones
  go into the kernel role bodies.
- A ship gate: a rule change lands only with a green suite run.

## Non-goals

- Evaluating the engineer role or the loop plumbing (nudges, GitHub
  mirror, phase machine). v1 covers every reviewer slot.
- Running on every push or in CI. Each run spends real agent tokens
  and minutes; it runs on demand before a rule change ships.
- Automatic rule writing. The agent may draft a rule; the operator
  confirms the case, and the suite decides whether the rule ships.

## Anti-goals

- A suite so slow or flaky that nobody runs it. Keep a full run in
  the minutes range for a few dozen cases.
- Matching comment text literally. Reviewers phrase things
  differently every run; brittle matching produces noise, not signal.
- Consumer code, specs, or hostnames landing in the spore repo or in
  a consumer repo.

## Constraints

- **Leak surface.** spore is opensource-bound. Cases built from
  marketer (most of today's corpus) contain marketer code and specs:
  they never enter the spore tree.
- **No consumer repo writes.** spore never commits into a consumer
  repo, so cases and project rules cannot live in
  `<projectRoot>/bootstrap/roles/` either.
- Therefore cases and project rules live host-side, under
  `task.StateDirForProject(projectRoot)` (call it `<state>`):
  - `<state>/evals/cases/<case-id>/`
  - `<state>/roles/rules/<role-body-name>/*.md` (new; see Rules)
- Kernel-level cases, built from spore's own `.spore/` runs, may live
  in the repo under `evals/cases/` since they contain only spore code.
- The case's reviewer slot config must be reproduced: mode, agent,
  effort and body come from `[fleet.reviewers.<X>]` and the defaults,
  resolved the same way `RoleSpawnSpec.resolve` does. The runner
  never assumes what a slot does (e.g. that A is `/code-review`).

## Approaches

1. **Headless replay (chosen).** Check out the case's frozen commit
   in a scratch worktree, run the resolved reviewer body
   non-interactively against it, capture the verdict JSON, judge it
   with one small model call per case. N runs per case, pass by
   majority. Fast, exercises exactly what a rule changes (the role
   body), no tmux. Skips loop plumbing, which unit tests already
   cover.
2. **Replay through the real role loop.** Scratch state dir, real
   panes, real drive ticks. Most faithful, but tens of minutes per
   case, flaky (trust prompts, tmux timing), token-heavy. Rejected:
   a suite that slow stops getting run.

Also rejected: literal text match on comments (brittle), and a static
check that the rule text is present (proves nothing about behavior).

Operator picked approach 1.

## Case format

`<cases>/<case-id>/`:

- `case.toml`
  - `id`, `kind` (`miss` | `invalid`), `project`, `source_slug`,
    `reviewer` (slot, e.g. `A`), `round` (that slot's round being
    replayed), `created`
  - `repo` (path or remote of the project repo), `base_sha`,
    `head_sha` (the commit the reviewer actually reviewed)
  - `expect`: one or two sentences, judge-facing. For a miss: the
    defect and where it lives. For an invalid: the comment that must
    not recur and why it is wrong.
  - `source` (`engineer`, `operator`, `reviewer:<Y>`, `human-pr`)
  - `confirmed_by`, `confirmed_at`
  - `rule` (optional): the rule file that fixed it, once one exists
- `spec.md`: copy of `.spore/<slug>/spec.md`.
- `responses/`: engineer responses up to (not including) the
  replayed round, so a round 2+ replay sees the same pushback history.
- `original-verdict.json`: what the reviewer actually wrote, for
  reference.

The case pins `head_sha`, not a branch: `wt/<slug>` branches get
deleted after merge. The builder decides how to keep the commit
reachable (fetch `refs/pull/<n>/head`, or bundle the needed objects
into the case dir).

## Commands

- `spore eval candidates [--project P]`: list detected misses and
  invalid-comment candidates not yet turned into cases or dismissed.
- `spore eval flag <slug> <reviewer> <round> <comment-ref>
  [--why "..."]`: the operator marks a reviewer comment as a
  non-issue; it becomes an invalid candidate.
- `spore eval confirm <candidate> [--expect "..."]`: write the case
  dir. `--expect` defaults to a draft built from the later
  reviewer's comment, the engineer's pushback, or the flag's
  `--why`; the operator edits it before confirming.
- `spore eval dismiss <candidate>`: record a no, so it stops showing.
- `spore eval run [--project P] [--case ID...] [--runs N]
  [--with-rule FILE] [--without-rule FILE]`: replay and judge.
  Prints per-case pass rate and an overall pass/fail. `--with-rule`
  appends a draft rule that is not placed yet; `--without-rule` drops
  a placed one. (Builder-check call: drafts are tested unplaced, so
  live panes never see an unproven rule.)
- `spore eval promote FILE [--kernel]`: runs the ship gate for a
  draft rule and places it in `<state>/roles/rules/` only on green.
  `--kernel` runs the gate and prints the summary for the PR instead
  of placing anything.

## Runner

Per case, per run:

1. Create a scratch worktree at `head_sha`; make `base_sha`
   resolvable as the review base.
2. Create a scratch `SPORE_TASK_DIR` holding `spec.md` and the
   case's `responses/`. No `github.json`, so the reviewer takes the
   no-PR path and posts nothing.
3. Resolve the case's reviewer slot (mode, agent, effort) and its
   body (project override, else bundled), then append the active
   rules for that body.
4. Run the agent headless (e.g. `claude -p` with the body as prompt,
   `SPORE_REVIEWER_INSTANCE=<X>`, `SPORE_REVIEW_EFFORT`, cwd the
   worktree). Collect `reviews/<X>/round-<round>.json`.
5. Judge: one model call with `expect`, the case kind and the verdict
   JSON. Output: pass/fail plus a one-line reason. miss passes only on
   a `[blocker]` comment matching the defect; invalid passes when no
   comment raises the rejected point.
6. Clean up the worktree and scratch dir.

Default N is 3, pass on 2 of 3. Results go to
`<state>/evals/runs/<timestamp>/` (verdicts, judge output, summary)
so a regression can be inspected after the fact.

## Rules

A rule is a short markdown fragment appended to a reviewer body at
spawn time, e.g. "When a job's failure path writes only a log row,
check that the record still enters the repair scope."

- Project rules: `<state>/roles/rules/<body-name>/*.md`, host-side,
  sorted by filename. New: `ResolveRoleBody` (or the spawn path)
  must append them for live panes too, so the eval and production run
  the same text.
- Kernel rules: edits to `bootstrap/roles/*.md` in the spore repo,
  landed by the normal PR flow, only when the lesson is not
  project-specific.

## Ship gate

A rule change ships when:

- the case that motivated it fails without the rule and passes with
  it (`--with-rule`), and
- every other case whose reviewer runs the same body still passes:
  that project's cases for a project rule; kernel cases plus every
  host project's cases for a kernel rule, since kernel bodies reach
  all projects. Both kinds gate: a rule that fixes a miss must not
  bring back an invalid comment, and the reverse.

`spore eval promote` enforces this for project rules. For a kernel
rule, the PR records the suite summary as bare counts per project
(cases, passed, failed): no case ids, `expect` text, or project
names beyond the kernel's own, to keep consumer detail out of a
public PR.

## Runtime budget

Faithful config wins over wall-clock time: the runner uses the
production mode, agent and effort for the case's slot, never a
cheaper stand-in. Speed comes from running cases in parallel
(bounded worker count, default 4, one scratch worktree each) and
from `--case` to run a subset while iterating. "Minutes" is a target,
not a hard limit; a full run of a few dozen cases at N=3 may take
tens of minutes.

## Seed corpus

About 15 marketer runs show A approve then B request_changes:
hk-421, hk-481, hk-499, hk-780 (x4), hk-792 (x3), hk-796, hk-800,
hk-857. These include spec-gap catches (e.g. hk-481 "holes 5 and 7
not achieved"); they count as A misses like any other. About 10
marketer runs carry engineer pushback, against A or B. Many of those
A reviews predate code-review mode (PR #107), so some old misses may
already pass on today's body; that is fine and expected.

## Left to the builder

- Package layout (`internal/evals/` suggested) and CLI wiring.
- How `head_sha` is recovered for existing runs (verdict file mtime
  vs branch reflog vs PR refs) and recorded for future runs (the
  driver could stamp the reviewed sha into the verdict-phase state).
- Judge model and prompt; keep it cheap and deterministic-leaning.
- Parallelism across cases.
- Candidate dedup and the dismiss store format.

## Builder notes (v0.34.0)

- Package `internal/evals/`, CLI `cmd/spore/eval_cmd.go`.
- Candidate ids: `<slug>.miss.<X><n>.<Y><m>` (X approved round n, Y
  requested changes at round m), `<slug>.invalid.<X><n>.eng<r>.p<i>`
  (pushback entry i of engineer round r), and
  `<slug>.invalid.<X><n>.op.c<i>` (operator flag on comment i).
  Pushback entries starting `skipped:` are tier skips, not candidates.
  Human PR reviews are not scanned yet: flag those by hand.
- Dismissals are empty markers under `<state>/evals/dismissed/<id>`;
  flags are JSON under `<state>/evals/flags/`.
- `head_sha`: the drive loop stamps the worktree HEAD into
  `.spore/<slug>/state/reviewed-<X>-<n>` when HEAD is no newer than
  the verdict file, else an empty stamp (unknown). Older runs fall back to the last commit on
  `wt/<slug>` at or before the verdict's mtime; else `--head-sha`.
  Confirm bundles `base..head` into `head.bundle` (built in a shared
  scratch clone, so the project repo is never written).
- Replays run in a `git clone --shared` scratch repo with no remote:
  the task branch at `head_sha`, the base branch at `base_sha`. The
  case dir also carries the slot's own earlier verdicts
  (`reviews/<X>/`) so a round 2+ reviewer numbers its file right.
- Judge: `claude -p --model haiku`, one call per run, JSON
  `{"pass", "reason"}`. A codex slot replays via `codex exec` with the
  live pane's argv (`RolePlan.Argv`: model, reasoning effort, CLAUDE.md
  fallback), minus the TUI-only `--no-alt-screen`.
- `confirm --kernel` refuses any repo whose go.mod is not spore's.
- Project rules dir uses the body file name without `.md`, e.g.
  `<state>/roles/rules/reviewer-code-review/`.
