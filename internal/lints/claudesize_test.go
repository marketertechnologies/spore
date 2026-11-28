package lints

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeSize_Short(t *testing.T) {
	body := "# Short\nline1\nline2\n"
	root := newTestRepo(t, map[string]string{"CLAUDE.md": body})
	issues, err := ClaudeSize{Limit: 5}.Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("expected 0 issues, got %v", issues)
	}
}

func TestClaudeSize_Overlong(t *testing.T) {
	lines := []string{"# Big"}
	for i := 0; i < 10; i++ {
		lines = append(lines, "filler")
	}
	lines = append(lines, "# Small", "ok")
	body := strings.Join(lines, "\n") + "\n"

	root := newTestRepo(t, map[string]string{"CLAUDE.md": body})
	issues, err := ClaudeSize{Limit: 5}.Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %v", issues)
	}
	if !strings.Contains(issues[0].Message, `"Big"`) {
		t.Fatalf("expected Big section flagged, got %v", issues[0])
	}
}

func TestClaudeSize_OptOut(t *testing.T) {
	lines := []string{"# Big"}
	for i := 0; i < 10; i++ {
		lines = append(lines, "filler")
	}
	lines = append(lines, "<!-- lint: size-ok -->")
	body := strings.Join(lines, "\n") + "\n"

	root := newTestRepo(t, map[string]string{"CLAUDE.md": body})
	issues, err := ClaudeSize{Limit: 5}.Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("expected 0 issues with opt-out, got %v", issues)
	}
}

func TestClaudeSize_SubdirFile(t *testing.T) {
	lines := []string{"# Rules"}
	for i := 0; i < 10; i++ {
		lines = append(lines, "rule")
	}
	body := strings.Join(lines, "\n") + "\n"

	root := newTestRepo(t, map[string]string{
		"sub/CLAUDE.md": body,
		"CLAUDE.md":     "# Top\nok\n",
	})
	issues, err := ClaudeSize{Limit: 5}.Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue in sub/CLAUDE.md, got %v", issues)
	}
	if issues[0].Path != "sub/CLAUDE.md" {
		t.Fatalf("expected sub/CLAUDE.md, got %s", issues[0].Path)
	}
}

func TestClaudeSize_LoneTitleSplitsOnH2(t *testing.T) {
	lines := []string{"# area", "intro"}
	for _, h := range []string{"## one", "## two"} {
		lines = append(lines, h)
		for i := 0; i < 4; i++ {
			lines = append(lines, "rule")
		}
	}
	lines = append(lines, "```sh", "# a shell comment, not a heading", "```")
	root := newTestRepo(t, map[string]string{"sub/CLAUDE.md": strings.Join(lines, "\n") + "\n"})
	issues, err := ClaudeSize{Limit: 7}.Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("lone title with short ## sections flagged: %v", issues)
	}
}

func TestDefaultRunsClaudeSizeAndSubdir(t *testing.T) {
	names := map[string]bool{}
	for _, l := range Default() {
		names[l.Name()] = true
	}
	for _, want := range []string{"claude-size", "claude-subdir"} {
		if !names[want] {
			t.Errorf("%s is not in Default()", want)
		}
	}
}

func TestDefaultLintsCleanOnRepo(t *testing.T) {
	// claude-size lists files with git ls-files; the nix flake check copies
	// the source tree without .git. Match listFiles' safe.directory, and
	// require ../.. to be the toplevel: inside a parent repo ls-files would
	// list none of spore's files and the test would pass vacuously.
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-c", "safe.directory="+root, "-C", root, "rev-parse", "--show-toplevel").Output()
	if errors.Is(err, exec.ErrNotFound) {
		t.Fatal(err)
	}
	if err != nil {
		t.Skipf("repo root is not a git work tree: %v", err)
	}
	if top := strings.TrimSpace(string(out)); top != root {
		t.Skipf("repo root %s is inside another git repo (toplevel %s)", root, top)
	}
	for _, l := range []Lint{ClaudeSize{}, ClaudeSubdir{}} {
		issues, err := l.Run("../..")
		if err != nil {
			t.Fatalf("%s: %v", l.Name(), err)
		}
		for _, i := range issues {
			t.Errorf("%s: %s", l.Name(), i)
		}
	}
}
