**Status**: design locked, pending implementation. All open questions
resolved in the design discussion that produced this file; no
operator answers needed before code work starts.

# role-based-fleet: engineer + two-reviewer loop

Replace the homogeneous worker fleet with role-typed workers behind
the coordinator. v1 ships three roles: an engineer who writes code,
and two sequential reviewers who gate the change before the operator
opens the PR.

## Motivation

Today the coordinator can mint N workers, all programmer-shaped, and
the only review step is the operator opening the PR and reading the
diff themselves. Real teams have an author plus one or two reviewers
who come at the change with fresh context. The harness should match.

A third role (QA) is deferred until a consumer has runnable test
surface (browser e2e, integration suite). Today's pilot consumer
(Marketer Backend) is backend-only; a QA worker would have nothing
to drive.

## Roles

- **Engineer.** Persistent for the lifetime of the task. Owns the
  branch. Reads the spec, writes code, addresses review feedback
  across rounds. Same instance through both reviewer phases.
- **Reviewer A.** Spawned after the engineer's first round.
  Persistent context across rounds until A approves. Sees the spec,
  the branch diff, and the engineer's per-round response. Does not
  see B.
- **Reviewer B.** Spawned fresh after A approves. Persistent context
  across rounds until B approves. Sees only the spec and the current
  task branch. Does not see A's review thread or the engineer's
  responses to A.

Reviewers never see each other's panes or artifacts. The coordinator
brokers everything. No worker reads another worker's tmux pane.

## Artifacts on disk

All under `.spore/<task>/`:

```
spec.md                              one-shot cache of the canonical spec
responses/engineer-round-N.json      engineer's per-round response
reviews/A/round-N.json               reviewer A's per-round verdict
reviews/B/round-N.json               reviewer B's per-round verdict
```

Each `reviews/<reviewer>/round-N.json`:

```json
{
  "verdict": "approve" | "request_changes",
  "summary": "one-line overall take",
  "comments": ["...", "..."]
}
```

Each `responses/engineer-round-N.json`:

```json
{
  "addressed": ["comment 1 -> fixed in commit X", "comment 2 -> Y"],
  "pushback": ["comment 3 -> I disagree because Z"],
  "notes": "free-form context for the reviewer"
}
```

Engineer commits substantive output to the task branch. The JSON
file is the structured handoff, not the work itself.

## Loop sequence

1. Operator hands coordinator a spec source (Jira ticket, GH issue,
   Linear, whatever the consumer uses). Coordinator pulls it once
   into `spec.md`. The cache is immutable for the run; mid-loop spec
   changes are out of scope (operator kills task, restarts).
2. Coordinator spawns the engineer pane. Engineer reads `spec.md`,
   writes code on the task branch, writes
   `responses/engineer-round-1.json`, marks the round done.
3. Coordinator spawns the reviewer A pane. A reads `spec.md`, the
   branch diff, and the engineer response. Writes
   `reviews/A/round-1.json`.
4. If A's verdict is `request_changes`: coordinator wakes the
   engineer pane with the verdict file as new input. Engineer makes
   changes, writes `responses/engineer-round-2.json`. Coordinator
   wakes A with the new response. Loop.
5. Cap: 3 rounds with reviewer A. If round 3 does not approve,
   coordinator pauses the loop and pages the operator.
6. When A approves: coordinator kills A's pane, spawns reviewer B
   fresh. B reads only `spec.md` and the current task branch. No A
   history. Same loop with the engineer (still persistent), same
   3-round cap.
7. When B approves: coordinator hands the operator a ready branch
   plus a synthesized summary of both review threads. Operator opens
   the PR, runs their own review, merges.

## Coordinator state machine

Per task, the coordinator tracks:

- Phase: `engineer-initial`, `review-A`, `engineer-revise-A`,
  `review-B`, `engineer-revise-B`, `done`, `escalated`.
- Round number within the current reviewer phase (1, 2, 3).
- Pane handles for engineer and the current reviewer.

Transitions are file-driven. The coordinator watches `.spore/<task>/`
for new artifacts and watches worker task-done signals for pane
idleness.

## Escalation

Three rounds with no `approve` from the current reviewer triggers
escalation. Coordinator marks the task `escalated`, alerts the
operator, leaves all panes alive for inspection. Operator decides:
clarify spec, force-approve, kill the task, or hand it back for
another round.

## Out of scope (v1)

- QA role. Add when a consumer has runnable test surface.
- Mid-loop spec refresh. Operator kills and restarts if the spec
  changes mid-flight.
- Re-engaging reviewer A after B raises a substantive issue. B's
  judgment stands; operator can manually re-trigger A if they want.
- Per-comment severity, line-anchored comments. Freeform comment
  strings are enough for v1.
- Configurable round cap. Hardcoded at 3.
- Posting local artifacts to the GH PR for audit-trail. v2 if the
  operator wants the iteration history visible to non-spore
  reviewers.

## Implementation surface

- `internal/fleet/`: role-typed worker spawn, per-task state machine
  driving the engineer-reviewer loop, file-watcher for artifact
  presence, tmux pane lifecycle per role.
- `internal/task/`: per-task directory under `.spore/<task>/` with
  the artifact layout above. New helpers to read and write the
  response and review JSON; round-N pathing.
- `cmd/spore/`: optional CLI surface for inspecting in-flight tasks
  (`spore task status <task-id>` reporting current phase, round,
  last verdict).
- Role skill bodies (under `bootstrap/skills/` or sibling): three
  bodies, one per role. Each describes what the role reads, what it
  writes, and the contract for verdict / response shape. Loaded into
  the worker's context at spawn.
- Composer / rule pool: no changes expected for v1. Role skills live
  alongside the fleet harness, not in the rule pool.

## Validation plan

- `go test ./internal/fleet/... ./internal/task/...` covers state
  machine transitions and artifact roundtrips.
- `spore lint` green on the branch.
- End-to-end smoke: drive one trivial task through the loop with all
  three roles; observe one rejection round followed by approval from
  both reviewers; verify the final summary artifact.
- Negative path: rig a reviewer to never approve; confirm the
  coordinator escalates at round 3 and leaves panes alive.

## Open follow-ups for v2+

- Mirror local review artifacts to GH PR comments on finalize.
- Configurable round cap via `spore.toml [review]`.
- QA role with a test-runner contract once a consumer benefits.
- Engineer push-back adjudication: today the engineer can record
  pushback in the response file, but there is no formal "decline N
  comments before the reviewer must accept" mechanism. The 3-round
  cap is the only relief valve.
