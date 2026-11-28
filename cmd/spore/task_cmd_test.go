package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/versality/spore/internal/task"
	"github.com/versality/spore/internal/task/frontmatter"
)

func TestRunTaskMergeForceMergeRedRequiresReason(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"flag with no follow-up", []string{"demo", "--force-merge-red"}},
		{"flag followed by empty string", []string{"demo", "--force-merge-red", ""}},
		{"flag with empty equals", []string{"demo", "--force-merge-red="}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runTaskMerge(tc.args)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "requires a <reason>") {
				t.Errorf("error = %q should mention requires a <reason>", err)
			}
		})
	}
}

func TestRunTaskMergeUnknownFlag(t *testing.T) {
	err := runTaskMerge([]string{"demo", "--bogus"})
	if err == nil {
		t.Fatal("expected error for unknown flag, got nil")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("error = %q should mention unknown flag", err)
	}
}

func TestRunTaskMergeMissingSlug(t *testing.T) {
	err := runTaskMerge(nil)
	if err == nil {
		t.Fatal("expected error for missing slug, got nil")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Errorf("error = %q should be a usage hint", err)
	}
}

func TestRunTaskNewRoleRefusesStart(t *testing.T) {
	t.Chdir(t.TempDir())
	err := runTaskNew([]string{"--role", "--start", "--no-edit", "demo"})
	if err == nil {
		t.Fatal("expected error for --role with --start, got nil")
	}
	if !strings.Contains(err.Error(), "--role does not combine with --start") {
		t.Errorf("error = %q should mention --role does not combine with --start", err)
	}
	if _, statErr := os.Stat("tasks"); !os.IsNotExist(statErr) {
		t.Errorf("tasks dir created despite refusal: %v", statErr)
	}
}

func TestRunTaskNewAgentWritesFrontmatter(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := runTaskNew([]string{"--role", "--agent", "codex", "--no-edit", "demo"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("tasks/demo.md")
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := frontmatter.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Agent != "codex" || m.Loop != task.LoopRole {
		t.Errorf("meta = agent %q loop %q, want codex role", m.Agent, m.Loop)
	}
}

func TestRunTaskNewRejectsUnknownAgent(t *testing.T) {
	t.Chdir(t.TempDir())
	err := runTaskNew([]string{"--agent", "gemini", "--no-edit", "demo"})
	if err == nil || !strings.Contains(err.Error(), "want claude or codex") {
		t.Fatalf("err = %v, want agent validation error", err)
	}
	if _, statErr := os.Stat("tasks"); !os.IsNotExist(statErr) {
		t.Errorf("tasks dir created despite refusal: %v", statErr)
	}
}

func TestRunTaskStartAgentFlagValidated(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll("tasks", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("tasks/x.md", []byte("---\nstatus: draft\nslug: x\ntitle: X\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runTaskStart([]string{"x", "--agent", "gemini"})
	if err == nil || !strings.Contains(err.Error(), "want claude or codex") {
		t.Fatalf("err = %v, want agent validation error", err)
	}
	if err := runTaskStart(nil); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Errorf("err = %v, want usage", err)
	}
}

func TestRunTaskReopenNoteFlags(t *testing.T) {
	usage := [][]string{
		nil,
		{"demo"},
		{"demo", "--note", "a", "--note-file", "b"},
		{"demo", "--from-pr", "--note", "a", "--note-file", "b"},
	}
	for _, args := range usage {
		if err := runTaskReopen(args); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Errorf("runTaskReopen(%q) = %v, want usage error", args, err)
		}
	}
	t.Chdir(t.TempDir())
	if err := runTaskReopen([]string{"demo", "--from-pr"}); err == nil || strings.Contains(err.Error(), "usage:") {
		t.Errorf("--from-pr alone = %v, want it accepted past flag checks", err)
	}
}

func TestRunTaskTellRoleTaskWithoutPaneErrors(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	t.Chdir(root)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SPORE_TASKS_DIR", filepath.Join(root, "tasks"))
	if err := os.MkdirAll("tasks", 0o755); err != nil {
		t.Fatal(err)
	}
	writeTask := func(slug, loop string) {
		m := frontmatter.Meta{Status: "active", Slug: slug, Title: slug, Loop: loop}
		if err := os.WriteFile(filepath.Join("tasks", slug+".md"), frontmatter.Write(m, []byte("body\n")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTask("roled", task.LoopRole)
	writeTask("plain", "")

	err := runTaskTell([]string{"roled", "hello"})
	if err == nil || !strings.Contains(err.Error(), "not delivered") {
		t.Fatalf("role tell err = %v, want a not-delivered error", err)
	}
	inbox, err := task.InboxDir("roled")
	if err != nil {
		t.Fatal(err)
	}
	if n := inboxMessages(t, inbox); n != 0 {
		t.Errorf("role tell wrote %d inbox entries nobody reads", n)
	}

	if err := runTaskTell([]string{"plain", "hello"}); err != nil {
		t.Fatalf("plain tell: %v", err)
	}
	inbox, err = task.InboxDir("plain")
	if err != nil {
		t.Fatal(err)
	}
	if n := inboxMessages(t, inbox); n != 1 {
		t.Errorf("plain tell inbox has %d entries, want 1", n)
	}

	if err := os.WriteFile(filepath.Join("tasks", "broken.md"), []byte("no frontmatter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runTaskTell([]string{"broken", "hello"}); err != nil {
		t.Fatalf("tell on unparseable task: %v", err)
	}
	inbox, err = task.InboxDir("broken")
	if err != nil {
		t.Fatal(err)
	}
	if n := inboxMessages(t, inbox); n != 1 {
		t.Errorf("unparseable task inbox has %d entries, want 1", n)
	}
}

// inboxMessages counts published messages, skipping the .tmp staging dir.
func inboxMessages(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			n++
		}
	}
	return n
}

func TestTaskCommandsFromWorktreeActOnMainRepo(t *testing.T) {
	root := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git(root, "init", "-q", "-b", "main")
	git(root, "config", "user.email", "test@example.com")
	git(root, "config", "user.name", "Test")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SPORE_TASKS_DIR", "")
	writeTask := func(dir, slug, status string) {
		t.Helper()
		m := frontmatter.Meta{Status: status, Slug: slug, Title: slug}
		if err := os.MkdirAll(filepath.Join(dir, "tasks"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tasks", slug+".md"), frontmatter.Write(m, []byte("body\n")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTask(root, "ax", "active")
	writeTask(root, "bx", "active")
	git(root, "add", ".")
	git(root, "commit", "-q", "-m", "tasks")
	wt := filepath.Join(root, ".worktrees", "ax")
	git(root, "worktree", "add", "-q", "-b", "wt/ax", wt, "main")
	writeTask(root, "late", "draft")
	t.Chdir(wt)

	status := func(dir, slug string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, "tasks", slug+".md"))
		if err != nil {
			t.Fatal(err)
		}
		m, _, err := frontmatter.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return m.Status
	}

	if err := runTaskPause([]string{"ax"}); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := runTaskBlock([]string{"bx"}); err != nil {
		t.Fatalf("block: %v", err)
	}
	if got := status(root, "ax"); got != "paused" {
		t.Errorf("main ax status = %q, want paused", got)
	}
	if got := status(root, "bx"); got != "blocked" {
		t.Errorf("main bx status = %q, want blocked", got)
	}
	if got := status(wt, "ax"); got != "active" {
		t.Errorf("worktree copy of ax = %q, want untouched active", got)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	lsErr := runTaskLs(nil)
	os.Stdout = orig
	w.Close()
	out, _ := io.ReadAll(r)
	if lsErr != nil {
		t.Fatalf("ls: %v", lsErr)
	}
	if !strings.Contains(string(out), "late\tdraft") {
		t.Errorf("ls from worktree missed main-only task late:\n%s", out)
	}
}
