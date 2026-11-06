package fleet

import (
	"os"
	"strings"
	"testing"

	"github.com/versality/spore/internal/editor"
)

func TestReconcileRefreshesWorkspaceOnlyWhenPresent(t *testing.T) {
	requireToolchain(t)

	dirs := newTestDirs(t)
	gitInit(t, dirs.project)
	mustEnable(t)
	t.Setenv("SPORE_AGENT_BINARY", "sleep 30")
	t.Cleanup(func() { killSporeSessions(dirs.project) })

	cfg := Config{TasksDir: dirs.tasks, ProjectRoot: dirs.project, MaxWorkers: 3}
	path := editor.WorkspacePath(dirs.project)

	writeTask(t, dirs.tasks, "alpha", "active")
	if _, err := Reconcile(cfg); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("reconcile created %s without opt-in: %v", path, err)
	}

	if _, _, err := editor.WriteWorkspace(dirs.project); err != nil {
		t.Fatal(err)
	}
	writeTask(t, dirs.tasks, "beta", "active")
	r, err := Reconcile(cfg)
	if err != nil {
		t.Fatalf("Reconcile pass 2: %v", err)
	}
	if got, want := r.Spawned, []string{"beta"}; !equalSlices(got, want) {
		t.Fatalf("Spawned = %v, want %v", got, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"../.worktrees/alpha"`, `"../.worktrees/beta"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("workspace missing %s:\n%s", want, data)
		}
	}
}
