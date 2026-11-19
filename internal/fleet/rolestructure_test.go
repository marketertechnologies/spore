package fleet

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/versality/spore/internal/task"
)

const (
	structV1 = "```diff\n Widget\n+  spin()\n```"
	structV2 = "```diff\n Widget\n+  spin()\n+  stop()\n```"
)

func writeEngStructure(t *testing.T, root, slug string, round int, structure string) {
	t.Helper()
	if err := task.WriteEngineerResponse(root, slug, round, task.EngineerResponse{
		Addressed: []string{"placeholder"},
		Structure: structure,
	}); err != nil {
		t.Fatal(err)
	}
}

// requestChanges drives the loop from engineer round n to n+1 through
// a reviewer A verdict asking for changes.
func (f *mirrorFixture) requestChanges(t *testing.T, round int, structure string) {
	t.Helper()
	mustWriteReview(t, f.root, f.slug, task.ReviewerA, round, task.Review{Verdict: task.VerdictRequestChanges, Summary: "again"})
	writeEngStructure(t, f.root, f.slug, round+1, structure)
	f.tick(t)
}

func TestGitHubMirrorPRBodyStructure(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	writeEngStructure(t, f.root, f.slug, 1, structV1)
	f.tick(t)
	if log := f.log(t); !strings.Contains(log, "BODY:\nMake the widget spin.\n\n"+structV1+"\n\nDispatched by @octo") {
		t.Fatalf("create body lacks structure between summary and footer:\n%s", log)
	}

	f.requestChanges(t, 1, "")
	for _, c := range f.calls(t) {
		if strings.HasPrefix(c, "pr edit") {
			t.Fatalf("body edited with inherited structure: %q", f.calls(t))
		}
	}

	f.requestChanges(t, 2, strings.TrimSuffix(strings.TrimPrefix(structV2, "```diff\n"), "\n```"))
	calls := f.calls(t)
	if last := calls[len(calls)-1]; last != "pr edit 12 --body-file -" {
		t.Fatalf("last call = %q, want body edit; all %q", last, calls)
	}
	if log := f.log(t); !strings.Contains(log, "BODY:\nMake the widget spin.\n\n"+structV2+"\n\nDispatched by") {
		t.Fatalf("edited body lacks new structure:\n%s", log)
	}
	if s := f.state(t); !s.BodyOwned || s.BodyStructure != structV2 {
		t.Fatalf("state = %+v", s)
	}
	n := len(f.calls(t))
	f.tick(t)
	if got := len(f.calls(t)); got != n {
		t.Fatalf("idle tick made %d gh calls", got-n)
	}
}

// TestGitHubMirrorPRBodyEditRetries: a structure change with no new
// event to post still reaches the body, and a failed edit retries.
func TestGitHubMirrorPRBodyEditRetries(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	writeEngStructure(t, f.root, f.slug, 1, structV1)
	f.tick(t)
	writeEngStructure(t, f.root, f.slug, 1, structV2)
	if err := os.WriteFile(f.failFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f.tick(t)
	if s := f.state(t); s.BodyStructure != structV1 {
		t.Fatalf("failed edit recorded: %+v", s)
	}
	if err := os.Remove(f.failFile); err != nil {
		t.Fatal(err)
	}
	f.tick(t)
	calls := f.calls(t)
	if last := calls[len(calls)-1]; last != "pr edit 12 --body-file -" {
		t.Fatalf("calls = %q", calls)
	}
	if s := f.state(t); s.BodyStructure != structV2 {
		t.Fatalf("state = %+v", s)
	}
}

func TestGitHubMirrorPRBodyStructureDropsPaths(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	writeEngStructure(t, f.root, f.slug, 1, "```diff\n+/srv/main/x.go\n```")
	f.tick(t)
	log := f.log(t)
	if strings.Contains(log, "/srv/main") || !strings.Contains(log, "BODY:\nMake the widget spin.\n\nDispatched by") {
		t.Fatalf("gh log:\n%s", log)
	}
}

// TestGitHubMirrorAdoptedPRBodyUntouched: the mirror never rewrites a
// body it did not write.
func TestGitHubMirrorAdoptedPRBodyUntouched(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	t.Setenv("FAKE_GH_OPEN_PR", `[{"number":7,"url":"https://github.com/acme/widget/pull/7"}]`)
	writeEngStructure(t, f.root, f.slug, 1, structV1)
	f.tick(t)
	f.requestChanges(t, 1, structV2)
	for _, c := range f.calls(t) {
		if strings.Contains(c, "--body-file") && strings.HasPrefix(c, "pr edit") {
			t.Fatalf("adopted PR body edited: %q", f.calls(t))
		}
	}
}

func TestSummaryStructure(t *testing.T) {
	root := t.TempDir()
	slug := "demo"
	writeEngStructure(t, root, slug, 1, structV1)
	mustWriteReview(t, root, slug, task.ReviewerA, 1, task.Review{Verdict: task.VerdictApprove, Summary: "ok"})
	mustWriteReview(t, root, slug, task.ReviewerB, 1, task.Review{Verdict: task.VerdictApprove, Summary: "ship it"})
	path := filepath.Join(task.RoleTaskDir(root, slug), "summary.md")
	read := func() string {
		t.Helper()
		if err := writeSummary(root, slug); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	body := read()
	i := strings.Index(body, "## structure\n\n"+structV1+"\n\n")
	if i < 0 || i > strings.Index(body, "## reviewer A") {
		t.Fatalf("structure missing or below the reviews:\n%s", body)
	}
	if again := read(); again != body {
		t.Fatalf("summary regenerated on stable inputs")
	}
	writeEngStructure(t, root, slug, 1, structV2)
	if body := read(); !strings.Contains(body, structV2) {
		t.Fatalf("summary kept the stale structure:\n%s", body)
	}
}

func TestLatestStructureInheritsAndFences(t *testing.T) {
	root := t.TempDir()
	slug := "demo"
	got := func() string {
		t.Helper()
		s, err := latestStructure(root, slug)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := got(); s != "" {
		t.Fatalf("no rounds: %q", s)
	}
	writeEngStructure(t, root, slug, 1, "  Widget\n+  spin()\n")
	writeEngStructure(t, root, slug, 2, "")
	if s, want := got(), "```diff\n  Widget\n+  spin()\n```"; !reflect.DeepEqual(s, want) {
		t.Fatalf("latest = %q, want %q", s, want)
	}
}
