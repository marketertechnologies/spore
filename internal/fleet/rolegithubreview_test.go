package fleet

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/versality/spore/internal/task"
)

const (
	reviewCall = "api repos/acme/widget/pulls/12/reviews --hostname github.com --method POST --input -"
	filesCall  = "api repos/acme/widget/pulls/12/files --hostname github.com --paginate"
)

// reviews returns every review payload sent, accepted or not, in order.
func (f *mirrorFixture) reviews(t *testing.T) []reviewPayload {
	t.Helper()
	var out []reviewPayload
	for _, l := range strings.Split(f.log(t), "\n") {
		in, ok := strings.CutPrefix(l, "INPUT: ")
		if !ok {
			continue
		}
		var p reviewPayload
		if err := json.Unmarshal([]byte(in), &p); err != nil {
			t.Fatalf("review payload %q: %v", in, err)
		}
		out = append(out, p)
	}
	return out
}

// anchoredReview has one comment on a diff line (one.txt:1), one on a
// line outside the diff (gone.go:5), and two without a parseable where.
var anchoredReview = task.Review{
	Verdict: task.VerdictRequestChanges, Summary: "two bugs", Prefix: "C",
	Comments: []task.ReviewComment{
		{Severity: "high", Category: "correctness", Where: "one.txt:1", Text: "wrong word", Reason: "always", Trigger: "read one.txt", Suggestion: "uno"},
		{Severity: "medium", Category: "edge-case", Where: "gone.go:5", Text: "nil map", Reason: "plausible", Trigger: "empty input"},
		{Severity: "low", Category: "nit", Where: "one.txt", Text: "file-level nit"},
		{Severity: "low", Category: "nit", Text: "no where"},
	},
}

func (f *mirrorFixture) postAnchoredReview(t *testing.T) {
	t.Helper()
	writeEng(1)(t, f.root, f.slug)
	f.tick(t)
	mustWriteReview(t, f.root, f.slug, task.ReviewerB, 1, anchoredReview)
	f.tick(t)
}

func anchorPaths(p reviewPayload) []string {
	var out []string
	for _, c := range p.Comments {
		out = append(out, c.Path)
	}
	return out
}

func TestGitHubMirrorInlineReviewPayload(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	f.postAnchoredReview(t)
	f.tick(t)
	if n := countCalls(f.calls(t), reviewCall); n != 1 {
		t.Fatalf("review posts = %d, calls = %q", n, f.calls(t))
	}
	revs := f.reviews(t)
	p := revs[0]
	if p.Event != "COMMENT" || p.CommitID == "" || p.CommitID != f.state(t).PushedSHA {
		t.Fatalf("payload = %+v, pushed = %s", p, f.state(t).PushedSHA)
	}
	if !reflect.DeepEqual(anchorPaths(p), []string{"one.txt", "gone.go"}) {
		t.Fatalf("anchors = %+v", p.Comments)
	}
	c := p.Comments[0]
	wantBody := "**C1.1** | high | correctness\n\nwrong word (always)\n\nTrigger: read one.txt\n\n```\nuno\n```"
	if c.Line != 1 || c.Side != "RIGHT" || c.Body != wantBody {
		t.Fatalf("inline comment = %+v", c)
	}
	if p.Comments[1].Line != 5 {
		t.Fatalf("second anchor = %+v", p.Comments[1])
	}
	wantMain := "**Code review (round 1): request changes**\n\ntwo bugs\n\n" +
		"- **C1.3** | low | nit | `one.txt` - file-level nit\n- **C1.4** | low | nit - no where\n"
	if p.Body != wantMain {
		t.Fatalf("body:\n%q\nwant:\n%q", p.Body, wantMain)
	}
}

func TestGitHubMirrorInline422KeepsDiffAnchors(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	writeRaw(t, os.Getenv("FAKE_GH_422"), `"path":"gone.go"`)
	writeRaw(t, os.Getenv("FAKE_GH_FILES"), `[{"filename":"other.txt","patch":"@@ -0,0 +1 @@\n+x"}]`+"\n"+`[{"filename":"one.txt","patch":"@@ -0,0 +1 @@\n+one"}]`+"\n")
	f.postAnchoredReview(t)
	f.tick(t)
	calls := f.calls(t)
	if want := []string{reviewCall, filesCall, reviewCall}; !reflect.DeepEqual(calls[len(calls)-3:], want) {
		t.Fatalf("calls = %q", calls)
	}
	revs := f.reviews(t)
	last := revs[len(revs)-1]
	if !reflect.DeepEqual(anchorPaths(last), []string{"one.txt"}) {
		t.Fatalf("kept anchors = %+v", last.Comments)
	}
	if !strings.Contains(last.Body, "- **C1.2** | medium | edge-case | `gone.go:5` - nil map") || strings.Contains(last.Body, "C1.1") {
		t.Fatalf("body = %q", last.Body)
	}
	if !f.posted(t, "B/round-1.json") {
		t.Fatalf("state = %+v", f.state(t))
	}
}

func TestGitHubMirrorInline422FallsBackToBody(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	writeRaw(t, os.Getenv("FAKE_GH_422"), `"path":`)
	f.postAnchoredReview(t)
	f.tick(t)
	calls := f.calls(t)
	if want := []string{reviewCall, filesCall, reviewCall}; !reflect.DeepEqual(calls[len(calls)-3:], want) {
		t.Fatalf("calls = %q", calls)
	}
	revs := f.reviews(t)
	last := revs[len(revs)-1]
	if len(last.Comments) != 0 || last.CommitID != "" {
		t.Fatalf("fallback payload: commit %q, anchors %+v", last.CommitID, last.Comments)
	}
	for _, id := range []string{"C1.1", "C1.2", "C1.3", "C1.4"} {
		if !strings.Contains(last.Body, "**"+id+"**") {
			t.Errorf("fallback body missing %s: %q", id, last.Body)
		}
	}
}

// Readable patches confirm one.txt:1, but GitHub still rejects the
// narrowed review: the mirror falls back to body only and posts.
func TestGitHubMirrorInline422NarrowedRetryFallsBackToBody(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	writeRaw(t, os.Getenv("FAKE_GH_422"), `"path":"one.txt"`)
	writeRaw(t, os.Getenv("FAKE_GH_FILES"), `[{"filename":"other.txt","patch":"@@ -0,0 +1 @@\n+x"}]`+"\n"+`[{"filename":"one.txt","patch":"@@ -0,0 +1 @@\n+one"}]`+"\n")
	f.postAnchoredReview(t)
	f.tick(t)
	calls := f.calls(t)
	if want := []string{reviewCall, filesCall, reviewCall, reviewCall}; len(calls) < 4 || !reflect.DeepEqual(calls[len(calls)-4:], want) {
		t.Fatalf("calls = %q", calls)
	}
	revs := f.reviews(t)
	if n := revs[len(revs)-2]; !reflect.DeepEqual(anchorPaths(n), []string{"one.txt"}) || n.CommitID != f.state(t).PushedSHA {
		t.Fatalf("narrowed payload = %+v", n)
	}
	last := revs[len(revs)-1]
	if len(last.Comments) != 0 {
		t.Fatalf("fallback kept anchors: %+v", last.Comments)
	}
	for _, id := range []string{"C1.1", "C1.2", "C1.3", "C1.4"} {
		if !strings.Contains(last.Body, "**"+id+"**") {
			t.Errorf("fallback body missing %s: %q", id, last.Body)
		}
	}
	if !f.posted(t, "B/round-1.json") {
		t.Fatalf("state = %+v", f.state(t))
	}
}

// Readable patches that confirm no anchor skip the narrowed retry.
func TestGitHubMirrorInline422NoDiffAnchorsGoesToBody(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	writeRaw(t, os.Getenv("FAKE_GH_422"), `"path":`)
	writeRaw(t, os.Getenv("FAKE_GH_FILES"), `[{"filename":"other.txt","patch":"@@ -0,0 +1 @@\n+x"}]`+"\n")
	f.postAnchoredReview(t)
	f.tick(t)
	calls := f.calls(t)
	if want := []string{reviewCall, filesCall, reviewCall}; !reflect.DeepEqual(calls[len(calls)-3:], want) || countCalls(calls, reviewCall) != 2 {
		t.Fatalf("calls = %q", calls)
	}
	revs := f.reviews(t)
	if last := revs[len(revs)-1]; len(last.Comments) != 0 {
		t.Fatalf("fallback kept anchors: %+v", last.Comments)
	}
	if !f.posted(t, "B/round-1.json") {
		t.Fatalf("state = %+v", f.state(t))
	}
}

// A review that fails even in the body-only form stays unposted and is
// sent again, once, on the next tick.
func TestGitHubMirrorInlineReviewPostsOnceAcrossRetries(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	writeRaw(t, os.Getenv("FAKE_GH_422"), `"event"`)
	f.postAnchoredReview(t)
	if f.posted(t, "B/round-1.json") {
		t.Fatal("rejected review marked posted")
	}
	failed := countCalls(f.calls(t), reviewCall)
	if failed != 2 {
		t.Fatalf("attempts in failing tick = %d, calls = %q", failed, f.calls(t))
	}
	if err := os.Remove(os.Getenv("FAKE_GH_422")); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		f.tick(t)
	}
	if n := countCalls(f.calls(t), reviewCall); n != failed+1 {
		t.Fatalf("review posts after recovery = %d, want %d", n, failed+1)
	}
	if !f.posted(t, "B/round-1.json") {
		t.Fatal("review not marked posted")
	}
}

func (f *mirrorFixture) posted(t *testing.T, name string) bool {
	t.Helper()
	s := f.state(t)
	return s.posted(name)
}

func TestParseWhere(t *testing.T) {
	for in, want := range map[string]struct {
		path string
		line int
		ok   bool
	}{
		"a/b.go:12":      {"a/b.go", 12, true},
		" `./a.go:3` ":   {"a.go", 3, true},
		"a.go":           {"", 0, false},
		"a.go:0":         {"", 0, false},
		"a.go:3-5":       {"", 0, false},
		"/abs/a.go:3":    {"", 0, false},
		":3":             {"", 0, false},
		"a.go:3 and b:4": {"", 0, false},
		"a/b.go:42:7":    {"a/b.go", 42, true},
	} {
		p, l, ok := parseWhere(in)
		if p != want.path || l != want.line || ok != want.ok {
			t.Errorf("parseWhere(%q) = %q, %d, %v", in, p, l, ok)
		}
	}
}

func TestRightSideLines(t *testing.T) {
	patch := "@@ -1,3 +1,4 @@\n ctx\n-old\n+new\n+added\n ctx2\n\\ No newline at end of file\n@@ -20 +21,2 @@\n+x\n y"
	got := rightSideLines(patch)
	want := map[int]bool{1: true, 2: true, 3: true, 4: true, 21: true, 22: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
