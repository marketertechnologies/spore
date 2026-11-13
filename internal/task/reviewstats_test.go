package task

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// statsFixture lays out two role-loop tasks plus a non-task dir.
//
//	alpha: A/1 request_changes (high, low, question; prefix R), engineer 2
//	       records fixed / declined / answered; A/2 approve (low,
//	       no outcome); B/1 approve, no comments.
//	beta:  A/1 legacy strings (no outcomes); A/2 invalid JSON.
func statsFixture(t *testing.T) string {
	root := t.TempDir()
	spore := filepath.Join(root, RoleTaskRoot)
	writeFixture(t, filepath.Join(spore, "alpha", "reviews", "A", "round-1.json"),
		strings.Replace(reviewJSON("request_changes", highCorrect, lowNit, question), `"summary":"s"`, `"summary":"s","prefix":"R"`, 1))
	writeFixture(t, filepath.Join(spore, "alpha", "responses", "engineer-round-1.json"), `{"author":"engineer","addressed":[],"pushback":[]}`)
	writeFixture(t, filepath.Join(spore, "alpha", "responses", "engineer-round-2.json"), `{"author":"engineer","addressed":["a"],"pushback":["b"],"outcomes":[
		{"reviewer":"A","round":1,"comment":0,"outcome":"fixed"},
		{"reviewer":"A","round":1,"comment":1,"outcome":"declined"},
		{"reviewer":"A","round":1,"comment":2,"outcome":"answered"},
		{"reviewer":"A","round":1,"comment":9,"outcome":"fixed"}]}`)
	writeFixture(t, filepath.Join(spore, "alpha", "reviews", "A", "round-2.json"), reviewJSON("approve", lowNit))
	writeFixture(t, filepath.Join(spore, "alpha", "reviews", "B", "round-1.json"), `{"author":"B","verdict":"approve","summary":"s","comments":[]}`)
	writeFixture(t, filepath.Join(spore, "beta", "reviews", "A", "round-1.json"), `{"author":"A","verdict":"request_changes","summary":"s","comments":["x","y"]}`)
	writeFixture(t, filepath.Join(spore, "beta", "reviews", "A", "round-2.json"), `{not json`)
	if err := os.MkdirAll(filepath.Join(spore, "no-reviews"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestCollectReviewStats(t *testing.T) {
	st, err := CollectReviewStats(statsFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if st.Tasks != 2 || st.Reviews != 4 || st.Skipped != 1 {
		t.Errorf("tasks/reviews/skipped = %d/%d/%d, want 2/4/1", st.Tasks, st.Reviews, st.Skipped)
	}
	sev := map[string]StatsRow{}
	for _, r := range st.BySeverity {
		sev[r.Key] = r
	}
	check := func(key string, raised int, outcomes map[string]int) {
		t.Helper()
		r := sev[key]
		if r.Raised != raised {
			t.Errorf("%s raised = %d, want %d", key, r.Raised, raised)
		}
		for k, v := range outcomes {
			if r.Outcomes[k] != v {
				t.Errorf("%s %s = %d, want %d", key, k, r.Outcomes[k], v)
			}
		}
	}
	check(SeverityHigh, 1, map[string]int{OutcomeFixed: 1})
	check(SeverityLow, 2, map[string]int{OutcomeDeclined: 1, OutcomeUnrecorded: 1})
	check(SeverityQuestion, 1, map[string]int{OutcomeAnswered: 1})
	check("ungraded", 2, map[string]int{OutcomeUnrecorded: 2})
	if _, ok := sev[SeverityMedium]; ok {
		t.Errorf("medium row present with no medium comments")
	}
	var who []string
	for _, r := range st.ByReviewer {
		who = append(who, fmt.Sprintf("%s=%d", r.Key, r.Raised))
	}
	if got, want := strings.Join(who, ","), "Requirements review=3,Reviewer A=3"; got != want {
		t.Errorf("reviewer rows = %s, want %s", got, want)
	}
	var order []string
	for _, r := range st.ByCategory {
		order = append(order, r.Key)
	}
	if got, want := strings.Join(order, ","), "correctness,spec,nit,-"; got != want {
		t.Errorf("category rows = %s, want %s", got, want)
	}
}

func TestCollectReviewStatsNoSporeDir(t *testing.T) {
	st, err := CollectReviewStats(t.TempDir())
	if err != nil || st.Tasks != 0 || st.Reviews != 0 {
		t.Fatalf("st = %+v, err = %v", st, err)
	}
}

func TestWriteReviewStats(t *testing.T) {
	st, err := CollectReviewStats(statsFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := WriteReviewStats(&b, st); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"2 task(s), 4 review file(s), 1 invalid file(s) skipped",
		"SEVERITY  RAISED  FIXED  PUSHED_BACK  DECLINED  ANSWERED  UNRECORDED",
		"high      1       1      0            0         0         0",
		"question  1       0      0            0         1         0",
		"CATEGORY",
		"nit",
		"REVIEWER             RAISED",
		"Requirements review  3       1      0            1         1         0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestParseEngineerResponseOutcomes(t *testing.T) {
	cases := []struct{ name, outcomes, want string }{
		{"valid", `{"reviewer":"B","round":2,"comment":0,"outcome":"pushed_back"}`, ""},
		{"bad outcome", `{"reviewer":"A","round":1,"comment":0,"outcome":"ignored"}`, `outcomes[0]: outcome "ignored" not one of fixed, pushed_back, declined, answered`},
		{"bad reviewer", `{"reviewer":"C","round":1,"comment":0,"outcome":"fixed"}`, `outcomes[0]: roletask: reviewer instance "C"`},
		{"bad round", `{"reviewer":"A","round":0,"comment":0,"outcome":"fixed"}`, `round must be >= 1`},
		{"unknown key", `{"reviewer":"A","round":1,"comment":0,"outcome":"fixed","why":"x"}`, `unknown field "why"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"author":"engineer","addressed":[],"pushback":[],"outcomes":[` + tc.outcomes + `]}`
			_, err := ParseEngineerResponse("x", []byte(body), false)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidRoleFile) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want invalid containing %q", err, tc.want)
			}
		})
	}
}
