package task

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func reviewJSON(verdict string, comments ...string) string {
	return `{"author":"A","verdict":"` + verdict + `","summary":"s","comments":[` + strings.Join(comments, ",") + `]}`
}

const (
	highCorrect  = `{"severity":"high","category":"correctness","where":"a.go:3","text":"nil deref","reason":"every request hits it"}`
	medEdge      = `{"severity":"medium","category":"edge-case","text":"empty list","reason":"plausible but harmless"}`
	medSpec      = `{"severity":"medium","category":"spec","text":"wrong flag","reason":"spec names --x"}`
	lowNit       = `{"severity":"low","category":"nit","text":"rename foo"}`
	legacyString = `"split commit X"`
)

func TestParseReviewGraded(t *testing.T) {
	cases := []struct {
		name  string
		round int
		body  string
		want  string
	}{
		// Rounds 1-2: any comment requests changes, none approves.
		{"r1 high", 1, reviewJSON("request_changes", highCorrect, lowNit), ""},
		{"r1 low only", 1, reviewJSON("request_changes", lowNit), ""},
		{"r2 medium", 2, reviewJSON("request_changes", medEdge), ""},
		{"r1 no comments approve", 1, reviewJSON("approve"), ""},
		{"r1 low approve", 1, reviewJSON("approve", lowNit), `round 1 verdict "approve" but the review has 1 comment(s): in rounds 1-2 any comment (high, medium or low) requires "request_changes"`},
		{"r2 medium approve", 2, reviewJSON("approve", medEdge, lowNit), `round 2 verdict "approve" but the review has 2 comment(s)`},
		// Final round: high/medium escalate, lows are ignored.
		{"r3 low only approve", 3, reviewJSON("approve", lowNit, lowNit), ""},
		{"r3 no comments approve", 3, reviewJSON("approve"), ""},
		{"r3 medium request_changes", 3, reviewJSON("request_changes", lowNit, medEdge), ""},
		{"r3 high request_changes", 3, reviewJSON("request_changes", highCorrect), ""},
		{"r3 medium approve", 3, reviewJSON("approve", lowNit, medSpec), `round 3 (final) verdict "approve" but comments[1] is medium: at the final round any high or medium requires "request_changes"`},
		{"r3 low request_changes", 3, reviewJSON("request_changes", lowNit), `round 3 (final) verdict "request_changes" but every comment is low: at the final round lows are ignored and the verdict must be "approve"`},
		{"r3 string in graded blocks", 3, reviewJSON("approve", lowNit, legacyString), `comments[1] is an ungraded string`},
		// All-string reviews keep the written verdict at any round.
		{"legacy strings approve", 1, reviewJSON("approve", legacyString), ""},
		{"legacy strings request_changes r3", 3, reviewJSON("request_changes", legacyString), ""},
		{"legacy no comments request_changes", 1, reviewJSON("request_changes"), ""},
		// Comment shape.
		{"high missing reason", 1, reviewJSON("request_changes", `{"severity":"high","category":"security","text":"x"}`), `comments[0]: high comment missing required field "reason"`},
		{"medium missing reason", 1, reviewJSON("request_changes", `{"severity":"medium","category":"perf","text":"x"}`), `medium comment missing required field "reason"`},
		{"unknown severity", 1, reviewJSON("request_changes", `{"severity":"critical","category":"nit","text":"x","reason":"r"}`), `severity "critical" not one of`},
		{"unknown category", 1, reviewJSON("request_changes", `{"severity":"low","category":"style","text":"x"}`), `category "style" not one of`},
		{"missing severity", 1, reviewJSON("request_changes", `{"category":"nit","text":"x"}`), `missing required field "severity"`},
		{"missing text", 1, reviewJSON("request_changes", `{"severity":"low","category":"nit"}`), `missing required field "text"`},
		{"unknown comment key", 1, reviewJSON("request_changes", `{"severity":"low","category":"nit","text":"x","file":"a.go"}`), `unknown field "file"`},
		{"non-object comment", 1, reviewJSON("approve", `3`), "decode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseReview("x.json", []byte(tc.body), ReviewerA, tc.round, false)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidRoleFile) {
				t.Fatalf("err = %v, want ErrInvalidRoleFile", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want substring %q", err, tc.want)
			}
		})
	}
}

// Category is a label only: a medium of any category behaves the same.
func TestCategoryDoesNotChangeVerdictRule(t *testing.T) {
	for _, cat := range ReviewCategories {
		c := `{"severity":"medium","category":"` + cat + `","text":"x","reason":"r"}`
		if _, err := ParseReview("x", []byte(reviewJSON("request_changes", c)), ReviewerA, 3, false); err != nil {
			t.Errorf("%s medium at r3 request_changes: %v", cat, err)
		}
		if _, err := ParseReview("x", []byte(reviewJSON("approve", c)), ReviewerA, 3, false); err == nil {
			t.Errorf("%s medium at r3 approve accepted", cat)
		}
	}
}

// Grandfathered files hold plain strings only; legacy mode must keep
// accepting them under either verdict.
func TestParseReviewLegacyStringsUnchanged(t *testing.T) {
	for _, v := range []string{"approve", "request_changes"} {
		body := `{"verdict":"` + v + `","comments":["a","b"],"extra":1}`
		r, err := ParseReview("x", []byte(body), ReviewerA, 3, true)
		if err != nil {
			t.Fatalf("%s: %v", v, err)
		}
		if r.Graded() || len(r.Comments) != 2 || r.Comments[1].Text != "b" {
			t.Errorf("%s: comments = %+v", v, r.Comments)
		}
	}
}

func TestParseReviewObjectShape(t *testing.T) {
	r, err := ParseReview("x", []byte(reviewJSON("request_changes", highCorrect, legacyString)), ReviewerA, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []ReviewComment{
		{Severity: "high", Category: "correctness", Where: "a.go:3", Text: "nil deref", Reason: "every request hits it"},
		{Text: "split commit X"},
	}
	if !reflect.DeepEqual(r.Comments, want) {
		t.Errorf("comments = %+v\nwant %+v", r.Comments, want)
	}
}

func TestReviewCommentMarshalRoundTrip(t *testing.T) {
	in := []ReviewComment{Ungraded("plain"), {Severity: "low", Category: "nit", Text: "x"}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `["plain",{"severity":"low","category":"nit","text":"x"}]`; got != want {
		t.Errorf("marshal = %s, want %s", got, want)
	}
	var out []ReviewComment
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip = %+v, want %+v", out, in)
	}
}

func TestReviewSeverityCounts(t *testing.T) {
	r := Review{Comments: []ReviewComment{
		{Severity: "high"}, {Severity: "medium"}, {Severity: "low"}, {Severity: "low"}, Ungraded("x"),
	}}
	hm, low, ung := r.SeverityCounts()
	if hm != 2 || low != 2 || ung != 1 {
		t.Errorf("SeverityCounts = %d, %d, %d; want 2, 2, 1", hm, low, ung)
	}
}
