package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/versality/spore/internal/fleet"
)

func TestIsCoordinatorSession(t *testing.T) {
	state := t.TempDir()
	cases := []struct {
		name     string
		envInbox string
		envState string
		envCoord string
		want     bool
	}{
		{"empty inbox is not coordinator", "", state, "", false},
		{"inbox exactly the legacy state dir", state, state, "", true},
		{"inbox under legacy state dir", filepath.Join(state, "proj/inbox"), state, "", true},
		{"inbox under kernel-neutral state dir", filepath.Join(state, "proj/inbox"), "", state, true},
		{"inbox unrelated to state dirs", "/tmp/rower-x/inbox", state, state, false},
		{"trailing slash on state dir is normalised", filepath.Join(state, "proj/inbox"), state + "/", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SPORE_TASK_INBOX", tc.envInbox)
			t.Setenv("SKYHELM_STATE_DIR", tc.envState)
			t.Setenv("SPORE_COORDINATOR_STATE_DIR", tc.envCoord)
			if got := isCoordinatorSession(); got != tc.want {
				t.Errorf("isCoordinatorSession() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveMaxWorkers_HonoursWTFleetFloor(t *testing.T) {
	t.Setenv("SPORE_FLEET_MAX_WORKERS", "")
	t.Setenv("WT_FLEET_FLOOR", "8")
	root := t.TempDir()
	got, err := resolveMaxWorkers(0, root)
	if err != nil {
		t.Fatalf("resolveMaxWorkers: %v", err)
	}
	if got != 8 {
		t.Errorf("got %d, want 8 (from WT_FLEET_FLOOR)", got)
	}
}

func TestResolveMaxWorkers_SporeMaxBeatsWTFloor(t *testing.T) {
	t.Setenv("SPORE_FLEET_MAX_WORKERS", "4")
	t.Setenv("WT_FLEET_FLOOR", "8")
	root := t.TempDir()
	got, err := resolveMaxWorkers(0, root)
	if err != nil {
		t.Fatalf("resolveMaxWorkers: %v", err)
	}
	if got != 4 {
		t.Errorf("got %d, want 4 (SPORE_FLEET_MAX_WORKERS wins)", got)
	}
}

func TestResolveMaxWorkers_BadWTFloorErrors(t *testing.T) {
	t.Setenv("SPORE_FLEET_MAX_WORKERS", "")
	t.Setenv("WT_FLEET_FLOOR", "0")
	root := t.TempDir()
	if _, err := resolveMaxWorkers(0, root); err == nil {
		t.Error("expected error for WT_FLEET_FLOOR=0")
	}
}

// runMatterSync was the prior CLI-level wrapper around the Linear
// adapter. The matter sync now runs inside fleet.Reconcile (see
// internal/fleet/matter_test.go) and the Linear adapter has its own
// integration suite (internal/matter/linear/linear_test.go), so the
// CLI no longer owns its own coverage of the matter pass.

func TestFleetReplenishHook_StdoutEmpty(t *testing.T) {
	state := t.TempDir()
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", state)
	t.Setenv("SKYHELM_STATE_DIR", "")
	t.Setenv("SPORE_TASK_INBOX", filepath.Join(state, "inbox"))
	t.Setenv("AGENT_BUDGET_STATE_DIR", t.TempDir())
	t.Setenv("AGENT_BUDGET_MODE", "api")
	t.Setenv("SPORE_FLEET_MAX_WORKERS", "2")
	t.Chdir(t.TempDir())

	orig := replenishReconcile
	t.Cleanup(func() { replenishReconcile = orig })
	replenishReconcile = func(fleet.Config) (fleet.Result, error) {
		return fleet.Result{Active: []string{"a"}, Spawned: []string{"a"}}, nil
	}

	code, stdout, stderr := captureHook(t, `{"hook_event_name":"Stop"}`, func() int {
		if err := runFleetReplenishHook(nil); err != nil {
			t.Errorf("runFleetReplenishHook: %v", err)
			return 1
		}
		return 0
	})
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty (codex parses it as JSON)", stdout)
	}
	if !strings.Contains(stderr, "replenish-hook: active=1 spawned=1") {
		t.Errorf("stderr = %q, want status line", stderr)
	}
}
