package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeFeedback struct {
	act PRActivity
	pr  int
}

func (f *fakeFeedback) PRActivity(pr int) (PRActivity, error) {
	f.pr = pr
	return f.act, nil
}

var (
	alice = PRAuthor{Login: "alice"}
	bob   = PRAuthor{Login: "bob"}
	app   = PRAuthor{Login: "spore-app", Bot: true}
	t0    = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
)

func at(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

func writeGitHubState(t *testing.T, root, slug, body string) {
	t.Helper()
	path := githubStatePath(root, slug)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReopenCommentStartsWithLead(t *testing.T) {
	if c := reopenComment(reopenState{Cycle: 3, Note: "x"}); !strings.HasPrefix(c, reopenCommentLead) {
		t.Fatalf("reopenComment %q does not start with %q", c, reopenCommentLead)
	}
}

func TestRenderPRFeedbackGroupsAndFilters(t *testing.T) {
	act := PRActivity{
		ReadyEvents: []PREvent{
			{Actor: app, At: at(0)},
			{Actor: alice, At: at(30)},
		},
		Reviews: []PRReview{
			{Author: alice, State: "CHANGES_REQUESTED", Body: "Split the parser.", At: at(5)},
			{Author: bob, State: "APPROVED", Body: "lgtm", At: at(6)},
			{Author: bob, State: "COMMENTED", Body: "", At: at(7)},
			{Author: app, State: "COMMENTED", Body: "**Code review (round 1): approve**", At: at(8)},
			{Author: alice, State: "COMMENTED", Body: "old review", At: at(-5)},
		},
		Threads: []PRThread{
			{Path: "b.go", Line: 12, Comments: []PRComment{
				{Author: alice, Body: "Name this better.", DiffHunk: "@@ -1,9 +1,9 @@\n a\n b\n c\n d\n+e := 1", At: at(3)},
				{Author: bob, Body: "Agreed,\nand add a test.", At: at(4)},
			}},
			{Path: "a.go", Line: 0, OriginalLine: 7, Comments: []PRComment{
				{Author: bob, Body: "Outdated but open.", DiffHunk: "@@ -7 +7 @@\n+x", At: at(2)},
			}},
			{Path: "a.go", Line: 3, Resolved: true, Comments: []PRComment{
				{Author: alice, Body: "resolved thread", At: at(2)},
			}},
			{Path: "c.go", Line: 1, Comments: []PRComment{
				{Author: alice, Body: "before the cutoff", At: at(-1)},
				{Author: PRAuthor{Login: "ci[bot]"}, Body: "bot reply", At: at(9)},
			}},
		},
		Comments: []PRComment{
			{Author: app, Body: "**Engineer (round 2)**", At: at(1)},
			{Author: bob, Body: "Also update the docs.", At: at(10)},
			{Author: alice, Body: "pre-cutoff chatter", At: at(-10)},
		},
	}
	got := renderPRFeedback(12, act)
	want := "### PR #12 feedback\n\n" +
		"Human comments on the PR since the loop last marked it ready or was reopened (2026-11-20 10:00 UTC). Address each one or say in your response why not.\n" +
		"\n#### `a.go`\n\n" +
		"- **@bob** at `a.go:7 (outdated)`:\n\n  ```diff\n  @@ -7 +7 @@\n  +x\n  ```\n\n  Outdated but open.\n" +
		"\n#### `b.go`\n\n" +
		"- **@alice** at `b.go:12`:\n\n  ```\n   b\n   c\n   d\n  +e := 1\n  ```\n\n  Name this better.\n" +
		"- **@bob** at `b.go:12`:\n  Agreed,\n  and add a test.\n" +
		"\n#### General comments\n\n" +
		"- **@alice** (review, changes requested):\n  Split the parser.\n" +
		"- **@bob** (comment):\n  Also update the docs."
	if got != want {
		t.Fatalf("render mismatch\n--- got\n%s\n--- want\n%s", got, want)
	}
}

func TestFeedbackCutoffUsesLatestReopenComment(t *testing.T) {
	act := PRActivity{
		ReadyEvents: []PREvent{{Actor: app, At: at(0)}},
		Comments: []PRComment{
			{Author: app, Body: reopenCommentLead + "2)**\n\nfix it", At: at(20)},
			{Author: alice, Body: "said before the reopen", At: at(15)},
			{Author: alice, Body: reopenCommentLead + "9)** spoofed by a human", At: at(40)},
			{Author: bob, Body: "said after the reopen", At: at(25)},
		},
	}
	if c := feedbackCutoff(act); !c.Equal(at(20)) {
		t.Fatalf("cutoff = %s, want %s", c, at(20))
	}
	got := renderPRFeedback(1, act)
	if strings.Contains(got, "before the reopen") || !strings.Contains(got, "after the reopen") {
		t.Fatalf("cutoff not applied:\n%s", got)
	}
}

func TestRenderPRFeedbackNoCutoffTakesEverything(t *testing.T) {
	got := renderPRFeedback(4, PRActivity{Comments: []PRComment{{Author: alice, Body: "first look", At: at(-100)}}})
	if !strings.Contains(got, "since it was opened") || !strings.Contains(got, "first look") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestReopenNoteFromPR(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	writeGitHubState(t, root, slug, `{"pr_number": 42, "pr_url": "https://github.com/o/r/pull/42"}`)
	feedback := &fakeFeedback{act: PRActivity{
		ReadyEvents: []PREvent{{Actor: app, At: at(0)}},
		Comments:    []PRComment{{Author: alice, Body: "Rename Foo.", At: at(1)}},
	}}

	note, err := ReopenNoteFromPR(root, slug, "  keep the resolver fix \n", feedback)
	if err != nil {
		t.Fatal(err)
	}
	if feedback.pr != 42 {
		t.Fatalf("fetched PR %d, want 42", feedback.pr)
	}
	if !strings.HasPrefix(note, "keep the resolver fix\n\n### PR #42 feedback\n") || !strings.HasSuffix(note, "Rename Foo.") {
		t.Fatalf("note:\n%s", note)
	}

	alone, err := ReopenNoteFromPR(root, slug, "", feedback)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(alone, "### PR #42 feedback") {
		t.Fatalf("note:\n%s", alone)
	}
}

func TestReopenNoteFromPREmptyFeedback(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	writeGitHubState(t, root, slug, `{"pr_number": 42}`)
	quiet := &fakeFeedback{act: PRActivity{
		ReadyEvents: []PREvent{{Actor: app, At: at(0)}},
		Comments:    []PRComment{{Author: alice, Body: "old", At: at(-1)}, {Author: app, Body: "bot", At: at(1)}},
	}}

	_, err := ReopenNoteFromPR(root, slug, "  ", quiet)
	if err == nil || !strings.Contains(err.Error(), "no new human feedback since 2026-11-20 10:00 UTC") {
		t.Fatalf("err = %v, want empty-feedback refusal", err)
	}

	note, err := ReopenNoteFromPR(root, slug, "do X", quiet)
	if err != nil || note != "do X" {
		t.Fatalf("note = %q, err = %v; want the plain note", note, err)
	}
}

func TestReopenNoteFromPRNeedsRecordedPR(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	if _, err := ReopenNoteFromPR(root, slug, "x", &fakeFeedback{}); err == nil || !strings.Contains(err.Error(), "no github.json") {
		t.Fatalf("err = %v, want missing github.json", err)
	}
	writeGitHubState(t, root, slug, `{}`)
	if _, err := ReopenNoteFromPR(root, slug, "x", &fakeFeedback{}); err == nil || !strings.Contains(err.Error(), "records no PR") {
		t.Fatalf("err = %v, want no PR", err)
	}
}

func TestParsePRActivity(t *testing.T) {
	raw := `{"data":{"repository":{"pullRequest":{
	  "timelineItems":{"nodes":[{"createdAt":"2026-11-20T10:00:00Z","actor":{"__typename":"Bot","login":"spore-app"}}]},
	  "reviews":{"nodes":[{"state":"COMMENTED","body":"hi","submittedAt":"2026-11-20T10:05:00Z","createdAt":"2026-11-20T10:01:00Z","author":{"__typename":"User","login":"alice"}}]},
	  "reviewThreads":{"nodes":[{"isResolved":true,"path":"a.go","line":null,"originalLine":4,
	    "comments":{"nodes":[{"body":"x","diffHunk":"@@","createdAt":"2026-11-20T10:02:00Z","author":null}]}}]},
	  "comments":{"nodes":[{"body":"c","createdAt":"2026-11-20T10:03:00Z","author":{"__typename":"Bot","login":"spore-app"}}]}
	}}}}`
	act, err := parsePRActivity([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(act.ReadyEvents) != 1 || !act.ReadyEvents[0].Actor.Bot {
		t.Fatalf("ready events = %+v", act.ReadyEvents)
	}
	if r := act.Reviews[0]; r.Author.Login != "alice" || r.Author.Bot || !r.At.Equal(at(5)) {
		t.Fatalf("review = %+v", r)
	}
	if th := act.Threads[0]; !th.Resolved || th.Line != 0 || th.OriginalLine != 4 || th.Comments[0].Author.Login != "" {
		t.Fatalf("thread = %+v", th)
	}
	if c := act.Comments[0]; !c.Author.Bot {
		t.Fatalf("comment = %+v", c)
	}
	if _, err := parsePRActivity([]byte(`{"errors":[{"message":"boom"}]}`)); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want graphql error", err)
	}
}
