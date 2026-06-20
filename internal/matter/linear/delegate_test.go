package linear

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestSyncDelegatesClaimedIssues(t *testing.T) {
	stub := newStub(t)
	stub.actorID = "rocky-actor-id"
	iss := stub.addReady("issue-uuid-1", "MAR-12", "Wire up onboarding email", "body")
	iss.DelegateID = "rocky-actor-id"

	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	t.Setenv("LINEAR_API_KEY", "lin_test")
	src, err := NewFromConfig(Config{
		Team:            "MAR",
		ReadyState:      "Ready",
		InProgressState: "In Progress",
		DoneState:       "Done",
		APIKeyEnv:       "LINEAR_API_KEY",
		Endpoint:        srv.URL,
		Delegate:        true,
	})
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}

	if _, _, err := src.Sync(context.Background(), t.TempDir()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if stub.lastDelegateID != "rocky-actor-id" {
		t.Errorf("claim delegateId = %q, want rocky-actor-id", stub.lastDelegateID)
	}
	if stub.lastAssigneeID != "" {
		t.Errorf("claim set assigneeId = %q, want empty (delegate, not assign)", stub.lastAssigneeID)
	}
}

// TestSyncSkipsUndelegatedWhenDelegateGateOn covers the ROC-20 negative
// smoke: with cfg.Delegate=true, Sync must skip Ready issues whose
// delegate is absent or points to a different actor. The pickup gate is
// symmetric with the claim-side delegate write: the operator (or a
// helper) pre-delegates a ticket to the rocky actor on Linear; rocky
// then claims only its own delegates.
func TestSyncSkipsUndelegatedWhenDelegateGateOn(t *testing.T) {
	stub := newStub(t)
	stub.actorID = "rocky-actor-id"
	stub.addReady("issue-undelegated", "ROC-20", "undelegated smoke", "must skip")
	other := stub.addReady("issue-other-delegate", "MAR-77", "delegated elsewhere", "must skip")
	other.DelegateID = "someone-else"
	mine := stub.addReady("issue-mine", "MAR-99", "delegated to rocky", "must pick up")
	mine.DelegateID = "rocky-actor-id"

	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	t.Setenv("LINEAR_API_KEY", "lin_test")
	src, err := NewFromConfig(Config{
		Team:            "MAR",
		ReadyState:      "Ready",
		InProgressState: "In Progress",
		DoneState:       "Done",
		APIKeyEnv:       "LINEAR_API_KEY",
		Endpoint:        srv.URL,
		Delegate:        true,
	})
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}

	created, _, err := src.Sync(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if created != 1 {
		t.Errorf("created = %d, want 1 (only the rocky-delegated issue)", created)
	}
	if stub.issues["issue-undelegated"].StateID != stub.states["Ready"] {
		t.Errorf("undelegated issue state = %q, want Ready (untouched)", stub.issues["issue-undelegated"].StateID)
	}
	if stub.issues["issue-other-delegate"].StateID != stub.states["Ready"] {
		t.Errorf("other-delegate issue state = %q, want Ready (untouched)", stub.issues["issue-other-delegate"].StateID)
	}
	if stub.issues["issue-mine"].StateID != stub.states["In Progress"] {
		t.Errorf("mine state = %q, want In Progress (claimed)", stub.issues["issue-mine"].StateID)
	}
}
