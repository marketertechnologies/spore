---
status: active
slug: smoke-undelegated-should-not-be-picked-up
title: [smoke] undelegated should NOT be picked up
created: 2026-06-20
agent: claude
matter: linear
matter_id: ROC-20
matter_sort_order: -15
matter_url: https://linear.app/newbuilds/issue/ROC-20/smoke-undelegated-should-not-be-picked-up
---

# ROC-20 - [smoke] undelegated should NOT be picked up

Linear: https://linear.app/newbuilds/issue/ROC-20/smoke-undelegated-should-not-be-picked-up

Negative test: no delegate set. Rocky must skip.

## Plan

Test outcome: FAILED on the live deployment. Rocky picked up ROC-20
despite no delegate being set (this worker's existence is the evidence).
Root cause: `internal/matter/linear/linear.go` `listIssuesByState`
filters by state + optional label only; `cfg.Delegate` was write-side
only (claim sets `delegateId` on the actor) and never gated pickup.

Implementation (commit c946eda):
- `linearIssue` gains `delegate { id }` from the issues query.
- When `cfg.Delegate=true`, `Sync` resolves actorID up front and skips
  Ready issues whose delegate is absent or does not match the actor.
- Tests split into `internal/matter/linear/delegate_test.go`: the
  existing claim-side delegate test now pre-sets a delegate so the
  issue passes the new gate, plus `TestSyncSkipsUndelegatedWhenDelegateGateOn`
  asserting both undelegated and other-delegated issues stay in Ready
  while a rocky-delegated issue is claimed.
- `go test ./...` and `spore lint` both green.

Caveat CLEARED by coordinator: live Linear schema probed -
`Issue.delegate` is a real field, and `issues(filter: {delegate: {id: {eq: $d}}})`
returns nodes without a schema error. c946eda is safe to merge as-is.

MERGE NOT ATTEMPTED by worker: tried `git -C /home/spore/project merge --ff-only`
but the main checkout is dirty with overlapping operator work on
`internal/matter/linear/linear.go` (already includes a `loadActorID()`
call in Sync plus an unreleased `CreateIssue` API). Operator must
reconcile my branch with their in-progress edits manually rather than
fast-forward. Optional optimization for that reconcile: move the
filter from client-side into the GraphQL query
(`issues(filter: {state: ..., delegate: {id: {eq: $actor}}})`) per the
coordinator's probe - saves a round of node fetches for issues
delegated elsewhere.

## Resume point (for the next worker)

Branch state (3 commits ahead of spore-upgrade-0.9.2):
- `1e711bf` test(smoke): document ROC-20 negative test failure (marker file `tasks/SMOKE.txt`)
- `c946eda` feat(matter): gate Linear pickup on existing delegate (ROC-20)
- `a08e936` task(roc-20): document plan + caveat in task brief

Coordinator inbox has two tells from this worker: the initial 3-option
escalation and a `plan ready: smoke-undelegated-should-not-be-picked-up`
ack request.

Next action depends on the schema verification:
- If `delegate { id }` is correct on Linear's Issue type: merge
  wt/smoke-undelegated-should-not-be-picked-up into spore-upgrade-0.9.2
  (cannot use `spore task merge` here because that requires the main
  checkout to be on `main`; the project root is on the integration
  branch instead - so a direct `git merge --ff-only` from the integration
  branch is required).
- If the field name differs (e.g. `delegateUser`, `delegateActor`): fix
  the query selector + struct tag in `internal/matter/linear/linear.go`
  (`linearIssue.Delegate` + the two `delegate { id }` selections inside
  `listIssuesByState`), rerun tests, then merge.

Wrapping due to token cap; no blocker.
