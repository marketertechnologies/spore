package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/versality/spore/internal/events"
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

func TestReconcileBudgetedRecordsFailure(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	orig := replenishReconcile
	t.Cleanup(func() { replenishReconcile = orig })
	replenishReconcile = func(fleet.Config) (fleet.Result, error) {
		return fleet.Result{}, errors.New("scan tasks: boom")
	}
	if _, err := reconcileBudgeted(fleet.Config{ProjectRoot: root}); err == nil {
		t.Fatal("want the pass error back")
	}
	evs, err := events.Read(filepath.Base(root), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != events.ReconcileFailed || !strings.Contains(evs[0].Detail, "boom") {
		t.Fatalf("ledger = %+v", evs)
	}
}

func cliTmuxSession(t *testing.T, name string, env ...string) {
	t.Helper()
	var envArgs []string
	for _, kv := range env {
		envArgs = append(envArgs, "-e", kv)
	}
	cmd := exec.Command("tmux", "-L", testTmuxSocket, "new-session", "-d", "-s", name)
	cmd.Args = append(cmd.Args, append(envArgs, "sleep 300")...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session %s: %v: %s", name, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", testTmuxSocket, "kill-session", "-t", "="+name).Run() })
}

// status lists role panes and the real agent count; stop kills the
// project's agent sessions, prints them, and leaves a pilot shell.
func TestFleetStatusAndStop(t *testing.T) {
	requireToolchain(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SPORE_FLEET_MAX_WORKERS", "3")
	root := filepath.Join(t.TempDir(), "clistop")
	gitInitRepo(t, root)
	t.Chdir(root)
	if err := fleet.Enable(); err != nil {
		t.Fatal(err)
	}
	cliTmuxSession(t, "spore/clistop/w1", "SPORE_TASK_SLUG=w1", "WT_PROJECT=clistop")
	cliTmuxSession(t, "spore-role/clistop/r1/engineer", "SPORE_TASK_SLUG=r1", "WT_PROJECT=clistop")
	cliTmuxSession(t, "spore-role/clistop/r1/reviewer-A", "SPORE_TASK_SLUG=r1", "WT_PROJECT=clistop")
	cliTmuxSession(t, "spore/pilot/op")

	_, out, _ := captureFn(t, func() int {
		if err := runFleet([]string{"status"}); err != nil {
			t.Errorf("status: %v", err)
		}
		return 0
	})
	for _, want := range []string{
		"fleet: enabled",
		"role panes:\n  r1/engineer\n  r1/reviewer-A\n",
		"agents: 3 running (coordinator 0, workers 1, role panes 2); slots 2/3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}

	_, out, _ = captureFn(t, func() int {
		if err := runFleet([]string{"stop", "--project", "clistop"}); err != nil {
			t.Errorf("stop: %v", err)
		}
		return 0
	})
	for _, want := range []string{
		"fleet: disabled",
		"killed: spore/clistop/w1\n",
		"killed: spore-role/clistop/r1/engineer\n",
		"killed: spore-role/clistop/r1/reviewer-A\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stop output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "pilot") {
		t.Errorf("stop touched the pilot session:\n%s", out)
	}
	if exec.Command("tmux", "-L", testTmuxSocket, "has-session", "-t", "=spore/pilot/op").Run() != nil {
		t.Error("spore/pilot/op was killed")
	}
	if on, _ := fleet.Enabled(); on {
		t.Error("fleet still enabled after stop")
	}
}

// Stop run from the coordinator kills its own session last, which
// ends the process: an error (the flag stayed) must reach stderr
// before that kill, not after.
func TestReportStopPrintsErrorBeforeSelfKill(t *testing.T) {
	var stdout, stderr strings.Builder
	var atKill string
	kill := func(string) error { atKill = stderr.String(); return nil }
	res := fleet.StopResult{Killed: []string{"spore/p/w1"}, Self: "spore/p/coordinator"}
	flagErr := errors.New("kill-switch flag not removed (the reconciler may respawn): read-only")
	_ = reportStop(res, flagErr, kill, &stdout, &stderr)
	if !strings.Contains(atKill, "flag not removed") {
		t.Errorf("stderr at self-kill = %q; want the flag error", atKill)
	}
	if strings.Contains(stdout.String(), "fleet: disabled") {
		t.Errorf("stdout claims disabled:\n%s", stdout.String())
	}
}

// The disabled line follows the flag, not the whole Stop: a listing
// failure after the flag went still says the fleet is disabled.
func TestReportStopDisabledDespiteListingError(t *testing.T) {
	var stdout, stderr strings.Builder
	res := fleet.StopResult{Disabled: true}
	err := reportStop(res, errors.New("tmux list-sessions: exit status 1"), func(string) error { return nil }, &stdout, &stderr)
	if err == nil {
		t.Error("listing error dropped")
	}
	if !strings.Contains(stdout.String(), "fleet: disabled") {
		t.Errorf("stdout missing the disabled line:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "killed: none") {
		t.Errorf("claims nothing ran after a failed listing:\n%s", stdout.String())
	}
}
