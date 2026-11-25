# Role-based fleet: operator guide

The role loop runs one task through an engineer pane and two reviewer
panes. Roles never see each other; they talk through files under
`.spore/<slug>/`. The driver derives all state from that tree, so
every command here is safe to re-run.

## Minting a task

Create `tasks/<slug>.md` with the role opt-in:

    spore task new "<title>" --role

The frontmatter is the standard task set (`status`, `slug`, `title`,
`created`, `project`) plus `loop: role`; the body below the
frontmatter becomes the spec. When editing a task file by hand, add
`loop: role` to the frontmatter yourself.

Then flip the frontmatter to `status: active` by editing the file,
not via `spore task start` (start spawns a homogeneous worker
session, and refuses on a `loop: role` task). The key makes the
opt-in atomic with the status flip: the reconciler pass that reacts
to the file write already sees `loop: role`, never mints a
homogeneous worker for the slug, and drives the role loop instead.
No pre-seeding is needed; the first drive tick creates
`.spore/<slug>/`.

`loop: role` is the only opt-in. A `.spore/<slug>/` dir on disk does
not mark the slug role-looped: coordinators stage briefs and inputs
there for generic tasks too, and those tasks keep their single
homogeneous worker. A task seeded by hand with only the dir needs
`loop: role` added to enter the role loop.

`role-drive` is also the manual driver: each run advances the loop by
one idempotent tick, and refuses a task whose frontmatter lacks
`loop: role` or whose status is not `active`. The reconciler calls the same drive for
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
at round 3 escalates instead of looping (see "Graded review comments"
for which verdicts each round allows). On escalation the driver
leaves all panes alive and wakes the engineer once for a fix pass at
the final verdict's high/medium comments; no reviewer is woken and
no re-review follows. The engineer's fix-pass response keeps the
phase `escalated` (`EscalationFixPending` clears). The operator can
then clarify the spec, force-approve, kill the task, or hand it back
for another loop. Force-approve with `spore task force-approve <slug>
<A|B> --reason <text>`: it flips that reviewer's escalated final
verdict to `approve` and records the reason as `operator_override`.
It refuses outside an escalation of that reviewer and while the fix
pass is still pending: an earlier verdict already has an engineer
round paired with it, and a fix pass landing after B's first verdict
would be read as B's answer. A forced approve is exempt from the
grade/round verdict rule, so its high and medium comments stay on
record instead of being bounced (hand-editing the verdict alone is
rejected by validation and restarts the reviewer). Only the exact
content the command wrote, recorded by sha256 in
`state/operator-overrides`, may carry `operator_override`; a reviewer
that sets it itself, or rewrites a forced file, is bounced. The
command clears that reviewer's `escalated-*` marker. The PR mirror
posts the override with its reason, and the summary marks the forced
verdict. The command refuses
while a drive tick holds the loop; retry. After a force-approve of
A's final verdict, the fix-pass response counts toward A's phase, so
B's revisions count from a fresh engineer round.

Per-phase side effects of a drive tick:

- engineer phases: keep the engineer pane alive (see Pane liveness);
  on revise phases, nudge it (tmux send-keys) with the latest verdict
  path
- `review-A`: keep engineer and reviewer A panes alive
- `review-B`: reap reviewer A's pane, keep reviewer B alive; the
  engineer pane stays alive across the handover
- review phases with the engineer check configured: hold the
  reviewer until the latest response has a pass or give-up marker
  (see "Engineer check gate")
- `done`: write `summary.md`, clear `escalated-*` markers, write the
  `ready` marker, reap all three panes
- `escalated`: write the `escalated-*` marker; until the engineer
  answers the final verdict, keep its pane alive and nudge it once
  for the fix pass. Reviewers are left alone
- every phase: bounce invalid role files (see below)
- non-terminal phases: check the active role for a stall (see Pane
  liveness)

## Pane liveness

Phase completion comes only from files; a pane exiting never advances
or stalls the loop by itself. Each tick probes every pane the phase
expects (tmux `pane_dead`, `pane_current_command`, `session_created`):

- missing session: spawn it, silently. This covers a pane missing
  after a phase transition and a token-monitor wrap (which kills its
  own session on purpose); neither counts toward the respawn cap.
- agent gone (pane dead, or a shell in the foreground once the
  session is 30s old, e.g. a hand-started role whose agent exited):
  kill the session and spawn a fresh one.

Each agent-gone respawn claims `state/respawn-<role>-r<round>-<n>`
and tells the coordinator (`<slug>: respawned <role> (<reason>; round
R, respawn K/3)`). When the agent dies again after 3 respawns of one
role in one round, the driver stops, leaves the dead pane for
inspection, and tells the coordinator once (`<slug>: escalated:
<role> agent died again ...`, marker
`state/respawn-capped-<role>-r<round>`). The cap re-arms when the
role's round advances; delete the role's `respawn-<role>-r<round>-*`
markers and the capped marker to re-arm it by hand.

Stall: the role that owns the next file write is stalled when its
expected file is absent and neither the role files under
`.spore/<slug>/` (outside `state/`) nor the pane transcript (the text
above the input prompt; status line and input box excluded) changed
for `[fleet] role_stall_minutes` (default 20, 0 disables). The
coordinator is told once per quiet spell (`<slug>: stalled: ...`);
any activity re-arms it. Stalls never kill the pane: the agent may be
waiting on a dialog only the operator can answer. The activity clock
lives in `state/activity-<role>-r<round>`.

## Roles

All panes run in the task worktree with `SPORE_TASK_DIR` pointing at
the absolute `.spore/<slug>/` tree, plus `SPORE_TASK_SLUG`,
`SPORE_PROJECT_ROOT`, `WT_PROJECT`, and `SPORE_ROLE` in the
environment. Role bodies are bundled in the spore binary; a project
file at `bootstrap/roles/<role>.md` overrides the bundled body of the
same name. A bundled body is written to
`$XDG_STATE_HOME/spore/<project>/roles/<sha>/<role>.md`, keyed by
content so two spore releases spawning for one project never swap each
other's body. An override that exists but is broken (dangling symlink
on the file or the `bootstrap/roles` dir, unreadable, not a regular
file) fails the spawn instead of falling back; a `bootstrap` or
`bootstrap/roles` that is not a directory (such as a `./bootstrap`
script) means no override. A project with a `bootstrap/roles/` dir
that lacks one body gets a stderr warning, once per process, when the
bundled body fills the gap. See "Reviewer slots" for the code-review
body.
When `[fleet] account_tier` is set in spore.toml, panes also get
`SPORE_ACCOUNT_TIER`, and `spore worker token-monitor` meters them
with the same three bands as workers (soft warn, finish-unit, force)
at the same tier-keyed caps. The wrap instructions are role-shaped:
commit in-flight work to the task branch, write the phase artifact
only if it is complete, then kill the pane session; the drive loop
respawns the phase's pane on its next tick and it resumes from the
branch plus artifacts. The soft warn fires once per session via a
marker at `.spore/<slug>/state/token-monitor/<session_id>.soft`.

### Reviewer slots

Each reviewer slot (A, B) picks how it produces its verdict, in
spore.toml or the host overlay (`$XDG_STATE_HOME/spore/<project>/workers.toml`,
which wins key by key):

```toml
[fleet.reviewers.A]
mode = "fleet"          # "fleet" | "code-review" (A defaults to fleet, B to code-review)

[fleet.reviewers.B]
mode   = "code-review"
agent  = "claude"       # optional; default is the task / [fleet.workers] agent
effort = "medium"       # code-review only: low|medium|high|xhigh|max (default medium)
```

- `fleet` runs the `reviewer.md` body: spec and acceptance-criteria
  conformance, runs tests and lints. A defaults to it.
- `code-review` runs the `reviewer-code-review.md` body, which
  invokes Claude Code's `code-review` skill at `effort` on the branch
  diff (the draft PR when `github.json` holds a PR number; the
  skill does not post, the mirror anchors the graded comments). The
  skill's findings carry no severity: the reviewer
  grades each one (see "Graded review comments") and the verdict
  follows from the grades. Rounds 2+ drop new `low` findings on code
  the engineer did not change that round, since the skill re-reviews
  the whole PR each time. The verdict file schema, comment IDs and
  the PR mirror are shared with `fleet` mode. The reviewer
  transcribes the skill's `ReportFindings` fields directly (`file:line`
  to `where`, `summary` to `text`, `failure_scenario` to `trigger`,
  `category` to `category`), so the mirror can anchor each graded
  comment on its line. The skill runs without `--comment`, so the
  review the driver posts is the only one on the PR and the
  authoritative numbered list. The
  run has no spec context, so the engineer pushes back with a
  one-line reason on findings the spec contradicts, and later rounds
  do not re-raise a finding with a sound pushback.
- `code-review` is claude-only. B defaults to it (effort `medium`)
  when no `mode` is set, but falls back to `fleet` when its agent
  (slot `agent`, else the task `agent:`, else the `[fleet.workers]`
  default) resolves to non-claude; an `effort` it set is then
  ignored with a stderr warning, as on any fleet slot. The fallback
  is re-evaluated at every spawn, like the agent, so changing the
  task agent or `[fleet.workers]` default mid-task can switch B
  between the two protocols across rounds; pin `mode` on B to rule
  that out. An explicit `mode = "code-review"` with a non-claude
  `agent` fails config load; without `agent` it runs claude even
  when the task pins codex.
- `agent` on a slot overrides only that slot. It is resolved at each
  spawn from the current config, like the task agent, and never from
  the worker ratio: a live pane keeps its agent, but a pane spawned
  after a config edit picks up the new one.
- Panes get `SPORE_REVIEWER_MODE`, `SPORE_REVIEWER_PREFIX`,
  `SPORE_REVIEWER_LABEL` (see "Comment IDs"), plus
  `SPORE_REVIEW_EFFORT` in code-review mode.

The default pairing is A = fleet, B = code-review: requirements
first, code review last. The engineer settles what the spec asks for
before the bug hunt runs over the final shape of the change. spore's
own spore.toml pins it explicitly. Before 0.33.1 the default was the
reverse (A = code-review); pin both `mode`s to keep that order.

### Pre-start hook

`[fleet] pre_start = "<shell command>"` runs a project command inside
each role pane and each worker session, before the agent starts (not
the coordinator). Typical use is bringing services up and loading a
test schema, e.g. marketer:

    [fleet]
    pre_start = "bin/with-services up && <schema load>"

The command runs in the session cwd (the worktree) with the session's
`SPORE_*` env, via `sh -c`, under `flock .spore/<slug>/state/pre-start.lock`.
The lock is per slug and shared by the worker, engineer, and both
reviewers, so make the command idempotent: later sessions wait for
the first, then should return quickly. The pane shell runs it, not
the reconciler, so spawning never blocks on it. The hook runs under
`timeout`, default 900s, set by `[fleet] pre_start_timeout_seconds`
(0 runs it unbounded), so a hung bring-up cannot hold the slug lock
forever.

While a session waits on the lock or runs the hook, the pane shows
the launch shell, which liveness would otherwise read as a dead
agent once spawnGrace passes. The prelude therefore holds
`state/pre-start-<role>.running` (`engineer`, `reviewer-A`,
`reviewer-B`, or `worker`) from just before the lock until just
before exec. Liveness leaves a shell-showing pane alone while that
marker exists and is no older than the session; a marker left by a
pane killed mid-hook is older than the respawned session and is
ignored.

A non-zero exit (including a missing `flock`, or 124 on timeout) never stops the agent.
The agent's first prompt gets one leading line: `spore: pre-start
hook failed (exit N): <command>. Services may be down.` Role panes
put it above the role body; a role pane with no role body gets that
line alone as the prompt. A worker gets it as the agent's first
argument, which `spore-worker-brief` puts above the brief. Unset or empty leaves the spawn argv byte-identical to
before.

### Engineer check gate

`[fleet.roles.engineer] check = "<shell command>"` makes the
project's checks a hard gate between the engineer and the reviewers,
instead of a prompt instruction:

```toml
[fleet.roles.engineer]
check = "just check"            # or a diff-scoped script such as bin/ci-local; unset = gate off
check_timeout_seconds = 900     # default 900
```

The gate is `spore hooks engineer-check`, a Stop hook shipped in
`bootstrap/handover/settings.json` (timeout 3600s; a larger
`check_timeout_seconds` is clamped to 3540s so the hook can record the
result). The shipped command runs it only when `spore hooks` lists
`engineer-check`, so a host on an older binary skips it. It is a no-op unless
`SPORE_ROLE=engineer` (set at spawn), `check` is set, and the latest
`responses/engineer-round-N.json` has no `check-pass-N-<sha>` or
`check-failed-N-<sha>` marker newer than it, sha being the worktree
HEAD. Then it runs `check` via `sh -c` in `.worktrees/<slug>/` under
`check_timeout_seconds` (exit 124 on timeout, the process group is
killed):

- pass: writes `state/check-pass-N-<sha>` (command, exit, duration,
  HEAD) and lets the stop through.
- fail: writes `state/check-attempt-N-<sha>-<k>` and exits 2, which
  blocks the stop and hands the model its stderr: the command, the
  exit code, and the last 40 lines of output.
- the 3rd failure on one round and sha writes
  `state/check-failed-N-<sha>` (with the output tail) and lets the
  stop through. Claude Code caps consecutive Stop blocks at 8 anyway;
  this guard keeps the loop moving well before that.

A newer response re-arms the gate, and so does a commit (new sha).
The hook reads no stdin, so the engineer can run it by hand.

Runs on one round are serialised by a flock on `state/check-N.lock`, which the holder stamps with its start time. A second run (the Stop hook racing a manual run) waits for the first, up to `check_timeout_seconds` plus 30s but never so long that the wait plus its own run would pass the 3600s hook timeout. Then it re-reads the gate: if the first run recorded a pass or give-up, or the engineer wrote a newer round meanwhile, it returns without running; if the first run recorded a failing attempt (any attempt written since the holder's stamp), it blocks with that attempt's output instead of rerunning. A holder that outlives the wait leaves the second run a clean exit (stop let through, nothing recorded); the driver still holds reviewers.

The driver records the gate in the snapshot (`Check`, `CheckSHA`;
`spore task status` prints a `check:` line, and while the check is pending its phase line reads `review-A (held: check)` so the hold does not look like a running reviewer) for the latest engineer
round at the worktree HEAD, in review phases only:

- `pending`: no reviewer wake, and a reviewer at round 0 is not
  spawned (a fresh reviewer starts on spawn). A reviewer with a
  verdict on record keeps its pane. The engineer is the active role
  for the stall check. Once the response is older than
  `check_timeout_seconds` plus a minute (the hook never ran: a codex
  pane, or a loop already in review when `check` was set; or HEAD
  moved after it ran), the engineer is woken once per round and sha
  to run `spore hooks engineer-check`.
- `passed`: hand off as usual; the reviewer wake says the check
  passed so the reviewer skips the full suite.
- `failed`: hand off anyway; the reviewer wake (a one-off wake for a
  fresh reviewer) names the failed check and the marker holding its
  output, to treat as a finding.

The draft-PR mirror holds the same gate on GitHub: while `check` is set, it does not push the branch or post the latest engineer round (nor any event after it) until `check-pass-N-<sha>` exists for the worktree HEAD. A failing run or a give-up (`check-failed`) leaves GitHub untouched for that round. While the latest round is held, every unposted engineer round is held with it, because each engineer event pushes the current HEAD; a gave-up round lands, with its verdict, once a later round passes at HEAD. `done` releases the hold, since the engineer pane is reaped and nothing would record a pass; so does a give-up at HEAD while `escalated`, since no later round will pass and the operator needs the PR current (a fix-pass commit moves HEAD and holds it again until that HEAD passes). With no `check` the mirror is unchanged.

Engineer (`spore-role/<project>/<slug>/engineer`): reads the spec and
the latest verdict, commits on the task branch, and ends each round
by writing `responses/engineer-round-N.json`:

    {
      "author": "engineer",
      "addressed": ["R1.1: guard the nil session"],
      "pushback": ["R1.3: disagree because Z"],
      "notes": "free-form context for the reviewer",
      "followups": ["out-of-scope idea for a separate PR"],
      "outcomes": [
        {"reviewer": "A", "round": 1, "comment": 0, "outcome": "fixed",
         "detail": "guard the nil session", "sha": "abc1234"}
      ]
    }

Reviewer (`spore-role/<project>/<slug>/reviewer-<instance>`): one
body, two instances, selected by `SPORE_REVIEWER_INSTANCE` (A or B).
A persists across its rounds and reads the engineer's responses. B
spawns fresh after A approves; round 1 is fresh eyes (no A history,
no engineer responses), rounds 2+ read only the engineer's replies to
B's own verdicts. Each round a reviewer writes
`reviews/<instance>/round-N.json`:

    {
      "author": "A",
      "prefix": "R",
      "verdict": "approve" | "request_changes",
      "summary": "one-line overall take",
      "comments": [
        {
          "severity": "high" | "medium" | "low" | "question",
          "category": "correctness",
          "where": "path:line",
          "text": "the defect and its failure scenario",
          "reason": "one line: why this severity"
        }
      ]
    }

## Graded review comments

Each review comment is an object (`internal/task/reviewgrade.go`):

- `severity`: `high`, `medium`, `low`, or `question`, rated as
  likelihood times impact. `high`: users hit it in normal use, or
  data loss, a security hole, a spec violation. `medium`: a
  plausible path with real impact, a missing test for a
  spec-required rule, or a design defect (a broken documented project
  convention, or a responsibility in the wrong layer; the trigger
  names the convention or the layer). `low`: a rare edge case, a
  theoretical race, a local code-quality note no convention
  requires. `question`: the reviewer is unsure or wants an
  explanation; the engineer must answer it. Severity is not
  confidence: anything the reviewer could not verify is `low` or
  `question`, never `high` / `medium`.
- `category`: `security`, `correctness`, `spec`, `data-loss`,
  `edge-case`, `test`, `perf`, `maintainability`, or `nit`.
- `where`: optional `path:line`.
- `text`: required.
- `reason`: one line on the grade; required for `high` and `medium`.
- `trigger`: the concrete inputs or steps that cause the failure;
  required for `high` and `medium`.
- `suggestion`: optional on any grade; replacement code or a small
  unified diff for `where`, included only when the reviewer is
  confident it applies.

The grade alone decides the verdict; `category` is only a label.
Validation enforces the rule per reviewer round
(`checkGrades(review, round)`):

| Round | Comments | Required verdict | Next |
| --- | --- | --- | --- |
| 1 | none | `approve` | next reviewer, or done |
| 1 | any (`high`, `medium`, `low`, or `question`) | `request_changes` | engineer revises (answers questions), reviewer re-reviews |
| 2 | none, or only `low` / `question` | `approve` | kept on record and mirrored, engineer not woken; next reviewer, or done |
| 2 | any `high` or `medium` | `request_changes` | engineer revises, reviewer re-reviews |
| 3 (final) | none, or only `low` / `question` | `approve` | as round 2 (PR marked ready when B approves) |
| 3 (final) | any `high` or `medium` | `request_changes` | escalated: one engineer fix pass, then the operator |

The engineer must fix `high`, fixes or pushes back (one-line reason)
on `medium`, and implements a `low` if it is simple or sensible,
otherwise declines it (`pushback`) or defers it (`followups`) with a
reason; lows are not blanket-discarded. It answers every
`question`. From round 2, reviewers do not raise new `low` comments
on code the engineer left unchanged that round, and a low the
engineer declined with a reason is closed: it is not raised again (a
pushback on a `medium` / `high` may be re-raised when its reasoning
is wrong). These two are role-body rules, not enforced in code.

Per-project calibration: reviewer bodies read
`$SPORE_PROJECT_ROOT/REVIEW.md` when it exists. It calibrates
severity for the project (for example "a missing required test is
medium here") and overrides the generic rubric examples, never the
verdict rules above. Spore only points the reviewer at it (the role
body is not composed in code); there is no default file.

Plain-string comments (legacy files, project role overrides that
predate grading) still parse and are ungraded. A review whose
comments are all ungraded (or empty) keeps its verdict as written,
exactly as before grading, at any round. In a review that grades
any comment, a plain string counts like a `medium`.

## Comment IDs

Each slot has a display label and a comment ID prefix, derived from
its mode, not its letter (`task.ReviewerPrefix`):

| Slot modes (A, B) | A | B |
| --- | --- | --- |
| fleet, code-review (default) | `R` Requirements review | `C` Code review |
| code-review, fleet | `C` Code review | `R` Requirements review |
| fleet, fleet | `R` Requirements review | `S` Requirements review 2 |
| code-review, code-review | `C` Code review | `D` Code review 2 |

The spawn settles both slots' modes (including the non-claude
fallback) and passes the result as `SPORE_REVIEWER_PREFIX` and
`SPORE_REVIEWER_LABEL`. The reviewer copies the prefix into its
verdict as `prefix`, so each file names its own IDs even if a slot's
mode changes between rounds. Internals keep A and B: `reviews/A/`,
`[fleet.reviewers.A]`, `SPORE_REVIEWER_INSTANCE`, and the `reviewer`
field of engineer outcomes.

A comment's ID is `<prefix><round>.<n>`, n counting from 1 in file
order: `R1.3` is the third comment of the requirements review's
round-1 verdict, `comment` index 2 in an engineer outcome. A graded
verdict must list its comments high, then medium (an ungraded string
counts as medium), then low, then question; validation rejects an
unsorted file, so on-disk order, ID order and PR order agree. A file
without `prefix` (written before IDs, or by a project role body that
does not set it) is numbered with its instance letter (`A1.2`) and
labelled `Reviewer A`.

The draft-PR mirror heads each review `**<label> (round N):
<verdict>**` and lists the comments in file order as `**R1.1** |
high | correctness | `path:line` - text (reason)`, with the trigger
on an indented `Trigger:` line and the suggestion as an indented
fenced block (tagged `diff` when it is a unified diff). An ungraded
string reads `**R1.2** | ungraded | text`; a fully ungraded review
lists `**A1.1** text`. A graded comment whose `where` is exactly
`path:line` is posted instead as an inline comment on that line of
the PR diff (right side, anchored to the last pushed commit), headed
`**R1.1** | high | correctness` with the text, the trigger and the
suggestion; the review body then lists only the other comments. When
GitHub rejects an anchor outside the diff (HTTP 422), the mirror
retries once keeping only the anchors the PR's file patches show in
the diff; when it cannot tell, every comment goes in the body as
before. The escalation comment names the reviewer by
label, says the engineer was asked for one fix pass, and that the
operator decides.

An engineer response comment starts with a table built from its
`outcomes`, one row per outcome in ID order (slot A, then B; round;
comment):

    | ID | Grade | Outcome | Details |
    | --- | --- | --- | --- |
    | R1.1 | high | fixed | `abc1234` guard the nil session |
    | R1.3 | low | declined | rename churns callers |

The grade is joined from the verdict file (`?` when the file is gone
or the index is past its comments); details are the outcome's `sha`
and `detail`. The free-text `addressed` / `pushback` / notes /
follow-ups follow, minus `addressed` / `pushback` entries that lead
with an ID whose row already has details. Responses without
`outcomes` render their lists as before.

### Final PR title and description

Engineer responses may carry `pr: {"title", "body"}`; the engineer
keeps it current in every response. Validation: `title` non-empty,
one line, at most 120 characters; `body` non-empty; no other keys. An
invalid `pr` bounces the file like any schema error; a missing one is
valid. With the draft-PR flow on, reaching done applies the `pr` of
the latest engineer response that carries one to the PR (title, and
body plus each reviewer's final verdict and the spore footer) just
before `gh pr ready`; see `docs/github-app.md`. The fleet reviewer
checks the `pr` text describes the change; a missing or misleading
one is a `low`. The engineer wake names the
reviewer by label and the IDs to cite (`Code review (reviewer B,
comment IDs C2.N) requested changes at round 2`).

## File validation

Every drive tick validates each role file before deriving the phase
(`internal/task/rolefile.go`). A file is invalid when it is not one
JSON object, carries a key outside its schema, lacks a required field
(`author` everywhere; `verdict` and `summary` on reviews), has a
verdict outside `approve` / `request_changes`, has a malformed graded
comment (unknown severity or category, unknown key, missing `text`,
missing `reason` or `trigger` on `high` / `medium`), lists graded
comments out of severity order, has an unknown `prefix`, has an
engineer `outcomes` entry with an unknown reviewer or outcome or a
`sha` that is not a 7-40 character hex commit id, has a verdict
that
contradicts its grades for its round (the error names the round
and the rule), or has an `author`
that does not match its location: `reviews/A/` needs `"A"`,
`reviews/B/` needs `"B"`, `responses/` needs `"engineer"`. Panes fill
it from `$SPORE_REVIEWER_INSTANCE` / `$SPORE_ROLE`.

An invalid file never counts toward the phase, so it cannot wedge or
skew the loop. The drive tells the coordinator once per bad file and
wakes the authoring pane with the validation error. Once the wake is
confirmed, the file moves to `<name>.invalid` (then `.invalid.2`, ...)
for debugging and the pane rewrites it. If the authoring pane is not
running, the file moves aside at once and the coordinator notice is
the only signal.

Upgrade path: the first drive by a validating binary writes
`state/legacy-files`, listing the role files already on disk. Those
are read leniently (unknown keys and a missing `author` pass; the
verdict enum still applies), so an in-flight loop does not rewind.
Files written after that point are strict. Panes spawned before the
upgrade still run the old role body; their next file without
`author` is bounced, and the wake message names the missing field.
The severity-order rule has its own marker: the first drive by a
binary that enforces it writes `state/order-exempt-files`, and the
verdicts listed there keep their order (their IDs already went out).
A tree with no marker yet is exempt in full until its first drive.

## Observing a running loop

`spore task status <slug>` prints the derived snapshot: phase,
engineer round, current reviewer, reviewer round, last verdict with
its comment counts (high/medium, low, question, ungraded), and an
`engineer: fix pass pending` line while an escalation's fix pass is
owed.

`spore task review-stats` scans every `.spore/*/reviews/{A,B}/
round-*.json` under the main repo root and prints three tables, by
reviewer label (`Requirements review`, `Code review`, `Reviewer A`
for files without a prefix), by severity and by category: comments
raised, and how the engineer handled them (`fixed`, `pushed_back`,
`declined`, `answered`) as recorded in the `outcomes` list of
engineer responses (`{reviewer, round, comment, outcome, detail,
sha}`, comment being the 0-based index into that review's
`comments`, so ID n minus 1; the latest record wins).
Comments with no record count as `unrecorded`; invalid files are
skipped and counted.

Markers under `.spore/<slug>/state/`:

- `woken-<reviewer>-<round>-<stamp>`: the engineer was nudged for
  this verdict. The stamp is the pane's tmux `session_created`, so a
  respawned engineer pane gets re-nudged on the next tick.
- `escalated-<reviewer>-<round>`: the loop escalated. Cleared by
  `spore task force-approve` for that reviewer, else when the loop
  later reaches done. After A's
  final verdict is approved past an escalation (forced, or an
  ungraded verdict hand-edited while this marker exists), the phase
  machine counts A's fix pass as one extra engineer round, provided
  that response exists and predates B's first verdict. A forced
  verdict keeps counting it after the marker is cleared.
- `operator-overrides`: review files `spore task force-approve`
  rewrote, each with the sha256 of the content it wrote; only that
  content may carry `operator_override`. A missing marker allows none.
- `ready`: the loop reached done; the branch awaits the operator.
  Cleared by `spore task done`.
- `legacy-files`: role files grandfathered from before validation.
- `order-exempt-files`: verdicts grandfathered from before the
  severity-order rule.
- `respawn-<role>-r<round>-<n>`, `respawn-capped-<role>-r<round>`,
  `activity-<role>-r<round>`, `stalled-<role>-r<round>-<since>`: pane
  liveness (see above).
- `check-pass-<round>-<sha>`, `check-attempt-<round>-<sha>-<k>`,
  `check-failed-<round>-<sha>`: engineer check gate results
  (`check-<round>.lock` serialises runs);
  `check-held-<round>-<sha>-<stamp>` and
  `check-failed-reviewer-<instance>-<round>-<stamp>` mark its wakes.
- `invalid-<file>-<n>-<hash>`: the coordinator was told about this
  invalid file; `bounced-<file>-<n>-<hash>-<stamp>` marks the
  delivered wake.

`spore task waybar` renders `d/a/p/b/e/r` counts (draft, active,
paused, blocked, escalated, ready). Escalated and ready count marker
files regardless of frontmatter status. The chip CSS class is the
highest-priority nonzero bucket: `escalated`, then `blocked`, then
`ready`, then `active`, else `idle`.

`summary.md` lands in `.spore/<slug>/` when the loop hits done: both
review threads (verdict, summary, comments per round) plus every
engineer response. It embeds a fingerprint of the artifact counts and
regenerates if the tree changes under it.

## Telling a role pane

Role panes have no inbox: they run without `SPORE_TASK_INBOX`, so the `watch-inbox` Stop hook never fires in them. `spore task tell <slug> <message>` on a `loop: role` task therefore types the message straight into a pane, with newlines and other control characters flattened to spaces and ` (operator message via spore task tell)` appended. The target is the role that owns the next file write (the engineer in engineer phases and while the check gate is pending, the current reviewer in review phases), or the engineer once the loop is done or when the reviewer pane is not up. The tell holds the drive lock while it types (waiting up to 5s for a running tick), so a drive wake cannot interleave with it. Delivery uses the same verified send as the drive's wakes. When no pane is up, its input prompt is not up yet (agent still booting), or the submit cannot be confirmed, the command exits 1 with the reason and the message is not queued anywhere: retry once the pane is up, and after an unconfirmed submit check the pane first, since the text may sit unsubmitted in its input. A tell on a task without `loop: role`, or whose task file cannot be parsed, still appends to the worker inbox.

## Finishing

When the loop is done, review the branch (`summary.md` is the digest)
and merge with `spore task merge <slug>`. Then run:

    spore task done <slug>

Done flips the status, kills the task's tmux sessions (the homogeneous worker session and every `spore-role/<project>/<slug>/<role>` pane, so an abandoned escalated task leaves nothing running), removes the worktree and the `wt/<slug>` branch, deletes `tasks/<slug>.md`, and clears the `state/ready` marker. It refuses while the branch has commits not yet merged into the task's `base:` (checked against the local branch and `origin/<base>`; the integration branch when `base:` is unset); `--force` discards them. `spore task merge` kills the same sessions.

### Reopening a finished loop

When the loop is done (or escalated) but the PR needs another pass,
reopen it instead of starting a new task:

    spore task reopen <slug> --note "drop the new service; keep the fix in the resolver"
    spore task reopen <slug> --note-file note.md
    spore task reopen <slug> --from-pr
    spore task reopen <slug> --from-pr --note "the inline comments win over the old spec"

`--from-pr` (draft-PR flow only) reads the human feedback left on the
task's PR and appends it to the note, so you do not paste review
comments by hand. It runs `gh api graphql` as the GitHub App, the
same way the mirror runs gh, against the PR number in `github.json`.
It takes what arrived after the cutoff: the newest of the loop's
`ready` events and its reopen comments (everything, if the loop has
done neither). It keeps review bodies (`COMMENTED` or
`CHANGES_REQUESTED`), inline review comments and plain PR comments.
It drops anything a bot or app wrote (the loop's own posts included)
and threads marked resolved. The rendered section, `### PR #N feedback`,
groups inline comments by file, each with its author, `path:line` and
the last four lines of the diff hunk, then lists the general comments.
With nothing new and no `--note`, reopen refuses. It reads the latest
100 reviews, threads and comments, and the latest 50 comments in each
thread; older ones are ignored. Comments left while a cycle runs
(after its reopen comment, before the loop marks the PR ready again)
fall before the next cutoff and are never read: post feedback for
`--from-pr` once the PR is ready. The PR's reopen comment carries
only the operator's own note plus a one-line pointer, not the
feedback: reposting it would duplicate the comments and @-mention
every author again. The reopen comment is cut at 60000 characters,
under GitHub's limit.

The task must be `status: active` (a `spore task done` task is gone)
and its loop done or escalated; mid-loop it refuses. Reopen:

- Kills the role panes.
- Moves `responses/`, `reviews/`, `summary.md` and the `state/`
  markers (except pre-start locks) to `cycles/<n>/`. The phase is
  derived from round files, so the empty tree restarts at
  engineer-initial with fresh round caps; the next reconciler tick
  spawns the engineer on the same worktree and branch.
- Appends the note to `spec.md` as `## Operator reopen (cycle N)`.
  The engineer builds on the branch and delivers that section; the
  requirements review checks it like any spec item.
- With `pr_flow = "draft"`, resets `github.json` to the PR number,
  URL and last pushed commit plus the pending reopen. The next tick
  moves the PR back to draft, posts the note as a comment, and the
  cycle's posts read `(cycle N, round M)`. Round numbers and comment
  IDs restart at 1 per cycle. At done the PR goes ready again and the
  engineer's PR description is re-applied.

Reopen fits "the PR is mostly right, change X". When the scope itself
was wrong, close the PR and mint a new task with the corrected spec.

The rest of `.spore/<slug>/` is preserved: `spec.md`, `responses/`,
`reviews/`, `summary.md`, and the remaining state markers stay on
disk as the task's audit trail.
