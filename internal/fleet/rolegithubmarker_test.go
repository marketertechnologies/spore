package fleet

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/versality/spore/internal/task"
)

// A post that landed before the save recording it (a crash between
// the two) is found by its marker on the next tick and not posted
// again; one that never landed is posted.
func TestGitHubMirrorPostIdempotentAcrossLostSave(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	writeEng(1)(t, f.root, f.slug)
	f.tick(t)
	mustWriteReview(t, f.root, f.slug, task.ReviewerA, 1, task.Review{Verdict: task.VerdictRequestChanges, Summary: "needs tests", Prefix: "R",
		Comments: []task.ReviewComment{highBug}})
	f.tick(t)
	writeEng(2)(t, f.root, f.slug)
	f.tick(t)
	if n := countCalls(f.calls(t), reviewCall); n != 1 {
		t.Fatalf("review posts = %d, want 1", n)
	}
	if n := countCalls(f.calls(t), "pr comment 12 --body-file -"); n != 1 {
		t.Fatalf("engineer comments = %d, want 1", n)
	}
	for _, want := range []string{"<!-- spore-mirror: cycle 1 A/round-1.json -->", "<!-- spore-mirror: cycle 1 engineer-round-2.json -->"} {
		if !strings.Contains(f.log(t), want) {
			t.Fatalf("posts lack marker %q:\n%s", want, f.log(t))
		}
	}

	// Lose the save of both posts: the state still says in flight.
	lose := func(names ...string) {
		t.Helper()
		st := f.state(t)
		var keep []string
		for _, p := range st.Posted {
			if !slices.Contains(names, p) {
				keep = append(keep, p)
			}
		}
		st.Posted, st.Inflight = keep, names[0]
		b, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(githubStatePath(f.root, f.slug), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Two pages, the way --paginate prints them; the marker is on the second.
	page2, err := json.Marshal([]map[string]string{{"body": f.log(t)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("FAKE_GH_LISTED"), append([]byte(`[{"body":"unrelated"}]`+"\n"), page2...), 0o644); err != nil {
		t.Fatal(err)
	}
	lose("A/round-1.json", "engineer-round-2.json")
	f.tick(t)
	calls := f.calls(t)
	if n := countCalls(calls, reviewCall); n != 1 {
		t.Errorf("review reposted after a lost save: %d posts", n)
	}
	if n := countCalls(calls, "pr comment 12 --body-file -"); n != 2 {
		t.Errorf("engineer comments = %d, want a repost: it was not in flight, so it is not looked up", n)
	}
	if n := countCalls(calls, "api repos/acme/widget/pulls/12/reviews --hostname github.com --paginate"); n != 1 {
		t.Errorf("review lookups = %d, want 1", n)
	}
	if st := f.state(t); st.Inflight != "" || !st.posted("A/round-1.json") || !st.posted("engineer-round-2.json") {
		t.Errorf("state = %+v", st)
	}

	// In flight but not on GitHub: the post never landed, so it goes out.
	if err := os.WriteFile(os.Getenv("FAKE_GH_LISTED"), []byte(`[{"body":"unrelated"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	lose("engineer-round-2.json")
	f.tick(t)
	calls = f.calls(t)
	if n := countCalls(calls, "api repos/acme/widget/issues/12/comments --hostname github.com --paginate"); n != 1 {
		t.Errorf("comment lookups = %d, want 1", n)
	}
	if n := countCalls(calls, "pr comment 12 --body-file -"); n != 3 {
		t.Errorf("engineer comments = %d, want 3 (the missing one posted)", n)
	}
}
