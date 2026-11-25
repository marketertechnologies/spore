package task

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/versality/spore/internal/events"
)

func TestLifecycleRecordsLedgerEvents(t *testing.T) {
	for _, bin := range []string{"git", "tmux"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available: %v", bin, err)
		}
	}
	repo := filepath.Join(t.TempDir(), "ledger-proj")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	tasksDir := filepath.Join(repo, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"one", "two"} {
		body := "---\nstatus: draft\nslug: " + slug + "\ntitle: T\n---\nbody\n"
		if err := os.WriteFile(filepath.Join(tasksDir, slug+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("SPORE_AGENT_BINARY", "sleep 30")
	for _, slug := range []string{"one", "two"} {
		session, err := Start(tasksDir, slug)
		if err != nil {
			t.Fatalf("Start %s: %v", slug, err)
		}
		t.Cleanup(func() { _ = exec.Command("tmux", "-L", testTmuxSocket, "kill-session", "-t", session).Run() })
	}
	if err := Pause(tasksDir, "one"); err != nil {
		t.Fatal(err)
	}
	if err := Block(tasksDir, "two"); err != nil {
		t.Fatal(err)
	}
	if err := Done(tasksDir, "one", false); err != nil {
		t.Fatal(err)
	}

	evs, err := events.Read("ledger-proj", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range evs {
		got = append(got, e.Kind+":"+e.Slug)
	}
	want := "task.start:one task.start:two task.pause:one task.block:two task.done:one"
	if strings.Join(got, " ") != want {
		t.Fatalf("ledger = %q\nwant     %q", strings.Join(got, " "), want)
	}
	if r := events.ProjectRoot("ledger-proj"); r == "" {
		t.Error("project root not noted")
	}
}
