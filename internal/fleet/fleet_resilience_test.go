package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
