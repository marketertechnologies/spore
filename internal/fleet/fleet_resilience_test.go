package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/versality/spore/internal/task"
)

func coordinatorInboxFor(t *testing.T, projectRoot string) string {
	t.Helper()
	dir, err := task.CoordinatorInboxDirForProject(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func fleetNotices(t *testing.T, projectRoot string) []string {
	t.Helper()
	var out []string
	for _, e := range readInboxEnvelopes(t, coordinatorInboxFor(t, projectRoot)) {
		if e.Source == "fleet" {
			out = append(out, e.Body)
		}
	}
	return out
}

// A bad task file sorting first must not stop the pass: the good
// workers spawn, the role loop is driven, and the coordinator hears
// about each bad file once across passes.
func TestReconcileSkipsBadTaskFiles(t *testing.T) {
	requireToolchain(t)
	bad := map[string]string{
		"a-noslug":   "---\nstatus: active\ntitle: x\n---\n",
		"a-mismatch": "---\nstatus: active\nslug: a-renamed\ntitle: x\n---\n",
		"a..bad":     "---\nstatus: active\nslug: a..bad\ntitle: x\n---\n",
		"a-nofence":  "status: active\nslug: a-nofence\n",
	}
	dirs := newTestDirs(t)
	gitInit(t, dirs.project)
	mustEnable(t)
	t.Setenv("SPORE_AGENT_BINARY", "sleep 30")
	shimClaudeOnPath(t)
	for name, body := range bad {
		if err := os.WriteFile(filepath.Join(dirs.tasks, name+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTask(t, dirs.tasks, "b-good", "active")
	writeTask(t, dirs.tasks, "c-good", "active")
	writeRoleLoopTask(t, dirs.tasks, "r-role", "active")
	t.Cleanup(func() {
		killSporeSessions(dirs.project)
		killRoleSessions(dirs.project)
	})

	for pass := range 2 {
		r, err := Reconcile(Config{TasksDir: dirs.tasks, ProjectRoot: dirs.project, MaxWorkers: 10})
		if err != nil {
			t.Fatalf("pass %d: Reconcile: %v", pass, err)
		}
		if pass == 0 {
			if got, want := r.Spawned, []string{"b-good", "c-good"}; !equalSlices(got, want) {
				t.Errorf("Spawned = %v, want %v", got, want)
			}
		}
		if len(r.Warnings) != len(bad) {
			t.Errorf("pass %d: warnings = %q, want one per bad file", pass, r.Warnings)
		}
	}
	project, _ := task.ProjectName(dirs.project)
	if !hasSession(fmt.Sprintf("spore-role/%s/r-role/engineer", project)) {
		t.Errorf("role loop not driven past the bad files")
	}
	notices := fleetNotices(t, dirs.project)
	if len(notices) != len(bad) {
		t.Fatalf("notices = %q, want %d (one per bad file, deduplicated)", notices, len(bad))
	}
	for name := range bad {
		var n int
		for _, b := range notices {
			if strings.Contains(b, "tasks/"+name+".md") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s: %d notices, want 1: %q", name, n, notices)
		}
	}
}

func TestValidBranchName(t *testing.T) {
	for name, want := range map[string]bool{
		"wt/ok-slug_1.2": true,
		"wt/a..b":        false,
		"wt/.hidden":     false,
		"wt/x.lock":      false,
		"wt/sp ace":      false,
		"wt/col:on":      false,
		"wt/trail.":      false,
		"wt/at@{":        false,
	} {
		if got := validBranchName(name); got != want {
			t.Errorf("validBranchName(%q) = %v, want %v", name, got, want)
		}
	}
}

func fakeClock(t *testing.T) *time.Time {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	prev := nowFunc
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = prev })
	return &now
}

func waitNoSession(t *testing.T, name string) {
	t.Helper()
	for range 50 {
		if !hasSession(name) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session %s still alive", name)
}

// A worker that dies right after spawn is respawned MaxFastDeaths
// times, then left down with one coordinator notice. Pausing the task
// re-arms the cap, and a healthy run resets the count.
func TestReconcileCapsWorkerCrashLoop(t *testing.T) {
	requireToolchain(t)
	dirs := newTestDirs(t)
	gitInit(t, dirs.project)
	mustEnable(t)
	t.Setenv("SPORE_COORDINATOR_AGENT", "sleep 300")
	t.Setenv("SPORE_AGENT_BINARY", "true")
	now := fakeClock(t)
	writeTask(t, dirs.tasks, "crashy", "active")
	t.Cleanup(func() { killSporeSessions(dirs.project) })
	project, _ := task.ProjectName(dirs.project)
	session := fmt.Sprintf("spore/%s/crashy", project)

	pass := func() Result {
		t.Helper()
		r, err := Reconcile(Config{TasksDir: dirs.tasks, ProjectRoot: dirs.project, MaxWorkers: 3})
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		waitNoSession(t, session)
		*now = now.Add(5 * time.Second)
		return r
	}
	var spawns int
	var last Result
	for range 6 {
		last = pass()
		spawns += len(last.Spawned)
	}
	if spawns != MaxFastDeaths {
		t.Errorf("spawns = %d, want %d", spawns, MaxFastDeaths)
	}
	if !equalSlices(last.Crashed, []string{"crashy"}) {
		t.Errorf("Crashed = %v, want [crashy]", last.Crashed)
	}
	if n := fleetNotices(t, dirs.project); len(n) != 1 || !strings.Contains(n[0], "crashy") {
		t.Errorf("notices = %q, want one crash-loop notice", n)
	}

	flipStatus(t, dirs.tasks, "crashy", "paused")
	pass()
	flipStatus(t, dirs.tasks, "crashy", "active")
	t.Setenv("SPORE_AGENT_BINARY", "sleep 30")
	r, err := Reconcile(Config{TasksDir: dirs.tasks, ProjectRoot: dirs.project, MaxWorkers: 3})
	if err != nil || !equalSlices(r.Spawned, []string{"crashy"}) {
		t.Fatalf("after pause: Spawned = %v, err %v; want respawn", r.Spawned, err)
	}
	*now = now.Add(FastDeathWindow)
	if _, err := Reconcile(Config{TasksDir: dirs.tasks, ProjectRoot: dirs.project, MaxWorkers: 3}); err != nil {
		t.Fatal(err)
	}
	dir, _ := fleetStateDir(dirs.project, "workers")
	if g := loadGuard(filepath.Join(dir, "crashy.json")); g.Pending || g.Deaths != 0 {
		t.Errorf("after healthy run: guard = %+v, want judged healthy with 0 deaths", g)
	}
}

// launchScript writes an agent script that logs each launch, then runs
// tail (a shell snippet).
func launchScript(t *testing.T, tail string) (agent, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "launches.log")
	agent = filepath.Join(dir, "agent.sh")
	body := fmt.Sprintf("#!/bin/sh\necho launch >> %s\n%s\n", log, tail)
	if err := os.WriteFile(agent, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return agent, log
}

func launches(t *testing.T, log string) int {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "launch")
}

// A coordinator that dies on spawn must not abort the pass: the worker
// still spawns. After MaxFastDeaths the pass stops trying and tells the
// coordinator inbox once.
func TestReconcileCoordinatorDiesOnSpawnKeepsPassAlive(t *testing.T) {
	requireToolchain(t)
	dirs := newTestDirs(t)
	gitInit(t, dirs.project)
	mustEnable(t)
	agent, log := launchScript(t, "exit 1")
	t.Setenv("SPORE_COORDINATOR_AGENT", agent)
	t.Setenv("SPORE_AGENT_BINARY", "sleep 30")
	now := fakeClock(t)
	writeTask(t, dirs.tasks, "w1", "active")
	t.Cleanup(func() { killSporeSessions(dirs.project) })

	for pass := range MaxFastDeaths + 2 {
		r, err := Reconcile(Config{TasksDir: dirs.tasks, ProjectRoot: dirs.project, MaxWorkers: 3})
		if err != nil {
			t.Fatalf("pass %d: Reconcile aborted: %v", pass, err)
		}
		if pass == 0 && !equalSlices(r.Spawned, []string{"w1"}) {
			t.Errorf("Spawned = %v, want [w1]", r.Spawned)
		}
		if len(r.Warnings) == 0 || !strings.Contains(r.Warnings[0], "coordinator") {
			t.Errorf("pass %d: warnings = %q, want a coordinator warning", pass, r.Warnings)
		}
		*now = now.Add(5 * time.Second)
	}
	if n := launches(t, log); n != MaxFastDeaths {
		t.Errorf("coordinator launches = %d, want %d", n, MaxFastDeaths)
	}
	if n := fleetNotices(t, dirs.project); len(n) != 1 || !strings.Contains(n[0], "coordinator") {
		t.Errorf("notices = %q, want one coordinator notice", n)
	}
}

// A launcher that falls back to an idle shell keeps the session alive
// after the agent exits. The pass must see through it and respawn.
func TestReconcileRespawnsCoordinatorBehindGreetShell(t *testing.T) {
	requireToolchain(t)
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc")
	}
	dirs := newTestDirs(t)
	gitInit(t, dirs.project)
	mustEnable(t)
	agent, log := launchScript(t, "exec sh")
	t.Setenv("SPORE_COORDINATOR_AGENT", agent)
	prev := coordinatorGrace
	coordinatorGrace = 0
	t.Cleanup(func() { coordinatorGrace = prev })
	t.Cleanup(func() { killSporeSessions(dirs.project) })

	session := CoordinatorSessionName(dirs.project)
	for range 3 {
		if _, err := Reconcile(Config{TasksDir: dirs.tasks, ProjectRoot: dirs.project, MaxWorkers: 3}); err != nil {
			t.Fatal(err)
		}
	}
	if n := launches(t, log); n != 3 {
		t.Errorf("launches = %d, want 3 (one per pass: the idle shell is a dead agent)", n)
	}
	if !hasSession(session) {
		t.Errorf("coordinator session gone; want the latest respawn alive")
	}
}

// An agent run as a child of a launcher shell is alive: only an idle
// shell counts as an exit.
func TestCoordinatorAgentGoneSeesChildOfLauncherShell(t *testing.T) {
	requireToolchain(t)
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc")
	}
	dirs := newTestDirs(t)
	gitInit(t, dirs.project)
	mustEnable(t)
	agent, log := launchScript(t, "sleep 300\nexit 0")
	// Run through sh so the pane process shows as a shell, like a
	// bash launcher script does.
	t.Setenv("SPORE_COORDINATOR_AGENT", "sh "+agent)
	prev := coordinatorGrace
	coordinatorGrace = 0
	t.Cleanup(func() { coordinatorGrace = prev })
	t.Cleanup(func() { killSporeSessions(dirs.project) })

	for range 3 {
		if _, err := Reconcile(Config{TasksDir: dirs.tasks, ProjectRoot: dirs.project, MaxWorkers: 3}); err != nil {
			t.Fatal(err)
		}
	}
	if n := launches(t, log); n != 1 {
		t.Errorf("launches = %d, want 1 (a launcher running the agent as a child is alive)", n)
	}
}

// Role loops take one slot each toward MaxWorkers, so the cap bounds
// agents, not just generic workers.
func TestReconcileCountsRoleLoopsTowardMaxWorkers(t *testing.T) {
	requireToolchain(t)
	dirs := newTestDirs(t)
	gitInit(t, dirs.project)
	mustEnable(t)
	t.Setenv("SPORE_COORDINATOR_AGENT", "sleep 300")
	t.Setenv("SPORE_AGENT_BINARY", "sleep 30")
	shimClaudeOnPath(t)
	writeRoleLoopTask(t, dirs.tasks, "a-role", "active")
	writeTask(t, dirs.tasks, "b-work", "active")
	writeTask(t, dirs.tasks, "c-work", "active")
	writeRoleLoopTask(t, dirs.tasks, "d-role", "active")
	t.Cleanup(func() {
		killSporeSessions(dirs.project)
		killRoleSessions(dirs.project)
	})
	project, _ := task.ProjectName(dirs.project)

	for pass := range 2 {
		r, err := Reconcile(Config{TasksDir: dirs.tasks, ProjectRoot: dirs.project, MaxWorkers: 2})
		if err != nil {
			t.Fatal(err)
		}
		if got, want := r.Skipped, []string{"c-work", "d-role"}; !equalSlices(got, want) {
			t.Errorf("pass %d: Skipped = %v, want %v", pass, got, want)
		}
	}
	if !hasSession(fmt.Sprintf("spore-role/%s/a-role/engineer", project)) {
		t.Errorf("a-role: engineer pane missing")
	}
	if hasSession(fmt.Sprintf("spore-role/%s/d-role/engineer", project)) {
		t.Errorf("d-role: engineer spawned past the cap")
	}
	if hasSession(fmt.Sprintf("spore/%s/c-work", project)) {
		t.Errorf("c-work: worker spawned past the cap")
	}
}
