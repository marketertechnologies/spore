package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	dir := t.TempDir()
	t.Setenv("SPORE_PROJECT_ROOT", dir)
	t.Chdir(dir)

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

func TestFleetWorkerAgentSetPrintClear(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	t.Setenv("SPORE_PROJECT_ROOT", root)

	if err := runFleet([]string{"worker-agent", "gemini"}); err == nil {
		t.Error("unknown agent accepted")
	}
	if err := runFleet([]string{"worker-agent", "codex"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if a, src, err := fleet.EffectiveWorkerDefault(root); err != nil || a != "codex" || src != fleet.WorkerDefaultFromOverlay {
		t.Errorf("after set = %q %q %v, want codex from overlay", a, src, err)
	}
	if err := runFleet([]string{"worker-agent"}); err != nil {
		t.Fatalf("print: %v", err)
	}
	if err := runFleet([]string{"worker-agent", "--clear"}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if a, src, _ := fleet.EffectiveWorkerDefault(root); a != "claude" || src != fleet.WorkerDefaultFromKernel {
		t.Errorf("after clear = %q %q, want kernel claude", a, src)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("project root not untouched: %v", entries)
	}
}

func TestResolveMaxWorkers_SporeTOMLBeatsEnv(t *testing.T) {
	t.Setenv("SPORE_FLEET_MAX_WORKERS", "3")
	t.Setenv("WT_FLEET_FLOOR", "8")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "spore.toml"), []byte("[fleet]\nmax_workers = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveMaxWorkers(0, root)
	if err != nil {
		t.Fatalf("resolveMaxWorkers: %v", err)
	}
	if got != 1 {
		t.Errorf("got %d, want 1 (spore.toml beats the host env default)", got)
	}
	if got, _ := resolveMaxWorkers(5, root); got != 5 {
		t.Errorf("flag: got %d, want 5", got)
	}
}

func TestFleetReconcileWarnsOnUnknownSporeTOMLKey(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("SPORE_FLEET_MAX_WORKERS", "")
	t.Setenv("WT_FLEET_FLOOR", "")
	if err := os.WriteFile(filepath.Join(root, "spore.toml"), []byte("[fleet]\nmax_worker = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := replenishReconcile
	t.Cleanup(func() { replenishReconcile = orig })
	replenishReconcile = func(fleet.Config) (fleet.Result, error) { return fleet.Result{Disabled: true}, nil }
	_, _, stderr := captureHook(t, "", func() int {
		if err := runFleetReconcile(nil); err != nil {
			t.Errorf("reconcile: %v", err)
		}
		return 0
	})
	if !strings.Contains(stderr, `spore fleet reconcile: warning: spore.toml line 2: unknown key "max_worker" in [fleet]`) {
		t.Errorf("stderr = %q, want the unknown-key warning", stderr)
	}
}

func gitInitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}

// A `claude -p` started from a coordinator pane inherits the
// coordinator env; its Stop hook must not reconcile the repo it runs in.
func TestFleetReplenishHook_OtherRepoDoesNotReconcile(t *testing.T) {
	state := t.TempDir()
	project := filepath.Join(t.TempDir(), "proj")
	scratch := filepath.Join(t.TempDir(), "scratch")
	gitInitRepo(t, project)
	gitInitRepo(t, scratch)
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", state)
	t.Setenv("SKYHELM_STATE_DIR", "")
	t.Setenv("SPORE_TASK_INBOX", filepath.Join(state, "proj", "inbox"))
	t.Setenv("SPORE_PROJECT_ROOT", project)
	t.Setenv("WT_PROJECT", "proj")
	t.Setenv("AGENT_BUDGET_STATE_DIR", t.TempDir())
	t.Setenv("AGENT_BUDGET_MODE", "api")
	t.Setenv("SPORE_FLEET_MAX_WORKERS", "2")

	var got []fleet.Config
	orig := replenishReconcile
	t.Cleanup(func() { replenishReconcile = orig })
	replenishReconcile = func(c fleet.Config) (fleet.Result, error) {
		got = append(got, c)
		return fleet.Result{}, nil
	}
	run := func(dir string) {
		t.Chdir(dir)
		code, stdout, stderr := captureHook(t, `{}`, func() int {
			if err := runFleetReplenishHook(nil); err != nil {
				t.Errorf("runFleetReplenishHook: %v", err)
				return 1
			}
			return 0
		})
		if code != 0 || stdout != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	}

	run(scratch)
	if len(got) != 0 {
		t.Fatalf("reconciled from a foreign repo: %+v", got)
	}

	t.Setenv("SPORE_PROJECT_ROOT", "")
	run(scratch)
	if len(got) != 0 {
		t.Fatalf("reconciled from a repo not named $WT_PROJECT: %+v", got)
	}

	t.Setenv("SPORE_PROJECT_ROOT", project)
	sub := filepath.Join(project, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	run(sub)
	if len(got) != 1 {
		t.Fatalf("own project: reconcile calls = %d, want 1", len(got))
	}
	if !samePath(got[0].ProjectRoot, project) || !samePath(got[0].TasksDir, filepath.Join(project, "tasks")) {
		t.Errorf("config = %+v, want project %s", got[0], project)
	}
}

func TestReconcileBudgetedAbandonsHungPass(t *testing.T) {
	t.Setenv(PassTimeoutEnv, "100ms")
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	orig, origAbort := replenishReconcile, passAbort
	t.Cleanup(func() { replenishReconcile, passAbort = orig, origAbort })
	replenishReconcile = func(fleet.Config) (fleet.Result, error) {
		<-release
		return fleet.Result{}, nil
	}
	aborted := false
	passAbort = func() { aborted = true }

	start := time.Now()
	_, err := reconcileBudgeted(fleet.Config{})
	if err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("err = %v, want a budget error", err)
	}
	if !aborted {
		t.Error("in-flight calls not aborted")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("took %s", took)
	}
}
