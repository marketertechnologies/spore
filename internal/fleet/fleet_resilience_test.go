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
