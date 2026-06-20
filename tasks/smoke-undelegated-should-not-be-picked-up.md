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

Caveat (operator ack needed before merging this branch into
spore-upgrade-0.9.2): assumed Linear's read field is `delegate { id }`
by convention from the existing `delegateId` mutation input. Not
verified against the live Linear schema (no API key in this worktree).
If the field name differs, rocky's next sync errors on the issues
query and stops adopting any ROC tickets. Recommend probing Linear
once before merging.
