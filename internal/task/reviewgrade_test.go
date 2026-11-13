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
	highCorrect  = `{"severity":"high","category":"correctness","where":"a.go:3","text":"nil deref","reason":"every request hits it","trigger":"GET /x with no session"}`
	medEdge      = `{"severity":"medium","category":"edge-case","text":"empty list","reason":"plausible but harmless","trigger":"run with an empty tasks/ dir"}`
	medSpec      = `{"severity":"medium","category":"spec","text":"wrong flag","reason":"spec names --x","trigger":"spore foo --x"}`
	lowNit       = `{"severity":"low","category":"nit","text":"rename foo","suggestion":"func bar() {}"}`
	question     = `{"severity":"question","category":"spec","text":"is the retry cap intended?"}`
	legacyString = `"split commit X"`
)

func TestParseReviewGraded(t *testing.T) {
	cases := []struct {
		name  string
		round int
		body  string
		want  string
	}{
		// Round 1: any comment requests changes, none approves.
		{"r1 high", 1, reviewJSON("request_changes", highCorrect, lowNit), ""},
		{"r1 low only", 1, reviewJSON("request_changes", lowNit), ""},
		{"r1 question only", 1, reviewJSON("request_changes", question), ""},
		{"r1 no comments approve", 1, reviewJSON("approve"), ""},
		{"r1 low approve", 1, reviewJSON("approve", lowNit), `round 1 verdict "approve" but the review has 1 comment(s): in round 1 any comment (high, medium, low or question) requires "request_changes"`},
		{"r1 question approve", 1, reviewJSON("approve", question), `round 1 verdict "approve"`},
		// Rounds 2-3: high/medium request changes (round 3 escalates);
		// lows and questions never block.
		{"r2 medium", 2, reviewJSON("request_changes", medEdge), ""},
		{"r2 low only approve", 2, reviewJSON("approve", lowNit, question), ""},
		{"r2 low request_changes", 2, reviewJSON("request_changes", lowNit), `round 2 verdict "request_changes" but every comment is low or question: from round 2 those never block and the verdict must be "approve"`},
		{"r2 question request_changes", 2, reviewJSON("request_changes", question), `every comment is low or question`},
		{"r2 medium approve", 2, reviewJSON("approve", lowNit, medEdge), `round 2 verdict "approve" but comments[1] is medium: from round 2 any high or medium requires "request_changes"`},
		{"r3 low only approve", 3, reviewJSON("approve", lowNit, lowNit), ""},
		{"r3 no comments approve", 3, reviewJSON("approve"), ""},
		{"r3 medium request_changes", 3, reviewJSON("request_changes", medEdge, lowNit), ""},
		{"r3 high request_changes", 3, reviewJSON("request_changes", highCorrect), ""},
		{"r3 medium approve", 3, reviewJSON("approve", lowNit, medSpec), `round 3 verdict "approve" but comments[1] is medium`},
		{"r3 low request_changes", 3, reviewJSON("request_changes", lowNit), `every comment is low or question`},
		{"r3 string in graded blocks", 3, reviewJSON("approve", lowNit, legacyString), `comments[1] is an ungraded string`},
		// All-string reviews keep the written verdict at any round.
		{"legacy strings approve", 1, reviewJSON("approve", legacyString), ""},
		{"legacy strings request_changes r3", 3, reviewJSON("request_changes", legacyString), ""},
		{"legacy no comments request_changes", 1, reviewJSON("request_changes"), ""},
		// Comment shape.
		{"high missing reason", 1, reviewJSON("request_changes", `{"severity":"high","category":"security","text":"x","trigger":"t"}`), `comments[0]: high comment missing required field "reason"`},
		{"medium missing reason", 1, reviewJSON("request_changes", `{"severity":"medium","category":"perf","text":"x","trigger":"t"}`), `medium comment missing required field "reason"`},
		{"high missing trigger", 1, reviewJSON("request_changes", `{"severity":"high","category":"security","text":"x","reason":"r"}`), `comments[0]: high comment missing required field "trigger"`},
		{"medium blank trigger", 1, reviewJSON("request_changes", `{"severity":"medium","category":"perf","text":"x","reason":"r","trigger":" "}`), `medium comment missing required field "trigger"`},
		{"low without trigger or reason", 1, reviewJSON("request_changes", `{"severity":"low","category":"nit","text":"x"}`), ""},
		{"question without trigger or reason", 1, reviewJSON("request_changes", `{"severity":"question","category":"spec","text":"x"}`), ""},
		{"suggestion on high", 1, reviewJSON("request_changes", `{"severity":"high","category":"correctness","text":"x","reason":"r","trigger":"t","suggestion":"--- a\n+++ b"}`), ""},
		{"unknown severity", 1, reviewJSON("request_changes", `{"severity":"critical","category":"nit","text":"x","reason":"r"}`), `severity "critical" not one of high, medium, low, question`},
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
			if !errors.Is(err, ErrInvalidRoleFile) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want invalid containing %q", err, tc.want)
			}
		})
	}
}

// Category is a label only: a medium of any category behaves the same.
func TestCategoryDoesNotChangeVerdictRule(t *testing.T) {
	for _, cat := range ReviewCategories {
		c := `{"severity":"medium","category":"` + cat + `","text":"x","reason":"r","trigger":"t"}`
		for _, round := range []int{2, 3} {
			if _, err := ParseReview("x", []byte(reviewJSON("request_changes", c)), ReviewerA, round, false); err != nil {
				t.Errorf("%s medium at r%d request_changes: %v", cat, round, err)
			}
			if _, err := ParseReview("x", []byte(reviewJSON("approve", c)), ReviewerA, round, false); err == nil {
				t.Errorf("%s medium at r%d approve accepted", cat, round)
			}
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
		{Severity: "high", Category: "correctness", Where: "a.go:3", Text: "nil deref", Reason: "every request hits it", Trigger: "GET /x with no session"},
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

func TestParseReviewSeverityOrder(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"sorted all grades", reviewJSON("request_changes", highCorrect, medEdge, medSpec, lowNit, question), ""},
		{"ties keep any order", reviewJSON("request_changes", medSpec, medEdge), ""},
		{"string sorts as medium", reviewJSON("request_changes", highCorrect, legacyString, lowNit), ""},
		{"all-string review any order", reviewJSON("request_changes", legacyString, `"another"`), ""},
		{"low before high", reviewJSON("request_changes", lowNit, highCorrect), "comments[1] (high) follows comments[0] (low): sort comments by severity"},
		{"question before low", reviewJSON("request_changes", question, lowNit), "comments[1] (low) follows comments[0] (question)"},
		{"medium before high", reviewJSON("request_changes", medEdge, highCorrect), "comments[1] (high) follows comments[0] (medium)"},
		{"low before string", reviewJSON("request_changes", lowNit, legacyString), "comments[1] (ungraded) follows comments[0] (low)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseReview("x.json", []byte(tc.body), ReviewerA, 1, false)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidRoleFile) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want invalid containing %q", err, tc.want)
			}
			// Legacy files keep any order.
			if _, err := ParseReview("x.json", []byte(tc.body), ReviewerA, 1, true); err != nil {
				t.Errorf("legacy: %v", err)
			}
		})
	}
}

func TestParseReviewPrefix(t *testing.T) {
	ok := `{"author":"A","verdict":"approve","summary":"s","prefix":"R","comments":[]}`
	r, err := ParseReview("x", []byte(ok), ReviewerA, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.IDPrefix(ReviewerA); got != "R" {
		t.Errorf("IDPrefix = %q, want R", got)
	}
	bad := `{"author":"A","verdict":"approve","summary":"s","prefix":"X","comments":[]}`
	if _, err := ParseReview("x", []byte(bad), ReviewerA, 1, false); err == nil || !strings.Contains(err.Error(), `prefix "X"`) {
		t.Errorf("unknown prefix: err = %v", err)
	}
	old, err := ParseReview("x", []byte(reviewJSON("approve")), ReviewerA, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := old.IDPrefix(ReviewerA); got != "A" {
		t.Errorf("IDPrefix without prefix = %q, want instance letter A", got)
	}
}

func TestReviewCounts(t *testing.T) {
	r := Review{Comments: []ReviewComment{
		{Severity: "high"}, {Severity: "medium"}, {Severity: "low"}, {Severity: "low"}, {Severity: "question"}, Ungraded("x"),
	}}
	if got, want := r.Counts(), (GradeCounts{HighMedium: 2, Low: 2, Question: 1, Ungraded: 1}); got != want {
		t.Errorf("Counts = %+v, want %+v", got, want)
	}
}
