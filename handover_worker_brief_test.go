package spore

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runWorkerBrief runs the shim with a fake agent that prints its last
// argument (the prompt) and returns it.
func runWorkerBrief(t *testing.T, brief string, args ...string) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	agent := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(agent, []byte("#!/bin/sh\nfor a; do last=$a; done\nprintf '%s' \"$last\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if brief != "" {
		if err := os.MkdirAll(filepath.Join(dir, "tasks"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tasks", "x.md"), []byte(brief), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	shim, err := filepath.Abs("bootstrap/handover/spore-worker-brief.sh")
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(bash, append([]string{shim}, args...)...)
	c.Dir = dir
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "SPORE_TASK_SLUG=x", "SPORE_WORKER_AGENT=" + agent}
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("shim: %v: %s", err, out)
	}
	return string(out)
}

func TestWorkerBriefPassesBrief(t *testing.T) {
	if got := runWorkerBrief(t, "the brief"); got != "the brief" {
		t.Errorf("prompt = %q, want the brief", got)
	}
}

func TestWorkerBriefPrependsSpawnNote(t *testing.T) {
	got := runWorkerBrief(t, "the brief", "spore: dirty worktree")
	if want := "spore: dirty worktree\n\nthe brief"; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
}

func TestWorkerBriefNoteWithoutBrief(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the brief retry loop")
	}
	if got := runWorkerBrief(t, "", "spore: note"); !strings.HasSuffix(got, "spore: note") {
		t.Errorf("prompt = %q, want the note", got)
	}
}
