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
		name, body, want string
	}{
		{"high blocks", reviewJSON("request_changes", highCorrect, lowNit), ""},
		{"medium spec blocks", reviewJSON("request_changes", medSpec), ""},
		{"medium edge-case allows approve", reviewJSON("approve", medEdge, lowNit), ""},
		{"low without reason ok", reviewJSON("approve", lowNit), ""},
		{"legacy strings approve", reviewJSON("approve", legacyString), ""},
		{"legacy strings request_changes", reviewJSON("request_changes", legacyString), ""},
		{"mixed string blocks", reviewJSON("request_changes", legacyString, lowNit), ""},
		{"mixed string cannot approve", reviewJSON("approve", legacyString, lowNit), `comments[0] (an ungraded string) is blocking`},
		{"approve with high", reviewJSON("approve", lowNit, highCorrect), `verdict "approve" but comments[1] (high correctness) is blocking`},
		{"approve with medium spec", reviewJSON("approve", medSpec), `must be "request_changes"`},
		{"request_changes with only optional", reviewJSON("request_changes", medEdge, lowNit), `verdict "request_changes" but no comment is blocking`},
		{"high missing reason", reviewJSON("request_changes", `{"severity":"high","category":"security","text":"x"}`), `comments[0]: high comment missing required field "reason"`},
		{"medium missing reason", reviewJSON("approve", `{"severity":"medium","category":"perf","text":"x"}`), `medium comment missing required field "reason"`},
		{"unknown severity", reviewJSON("approve", `{"severity":"critical","category":"nit","text":"x","reason":"r"}`), `severity "critical" not one of`},
		{"unknown category", reviewJSON("approve", `{"severity":"low","category":"style","text":"x"}`), `category "style" not one of`},
		{"missing severity", reviewJSON("approve", `{"category":"nit","text":"x"}`), `missing required field "severity"`},
		{"missing text", reviewJSON("approve", `{"severity":"low","category":"nit"}`), `missing required field "text"`},
		{"unknown comment key", reviewJSON("approve", `{"severity":"low","category":"nit","text":"x","file":"a.go"}`), `unknown field "file"`},
		{"non-object comment", reviewJSON("approve", `3`), "decode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseReview("x.json", []byte(tc.body), ReviewerA, false)
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

// Grandfathered files hold plain strings only; legacy mode must keep
// accepting them under either verdict.
func TestParseReviewLegacyStringsUnchanged(t *testing.T) {
	for _, v := range []string{"approve", "request_changes"} {
		body := `{"verdict":"` + v + `","comments":["a","b"],"extra":1}`
		r, err := ParseReview("x", []byte(body), ReviewerA, true)
		if err != nil {
			t.Fatalf("%s: %v", v, err)
		}
		if r.Graded() || len(r.Comments) != 2 || r.Comments[1].Text != "b" {
			t.Errorf("%s: comments = %+v", v, r.Comments)
		}
	}
}

func TestParseReviewObjectShape(t *testing.T) {
	r, err := ParseReview("x", []byte(reviewJSON("request_changes", highCorrect, legacyString)), ReviewerA, false)
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

func TestReviewCountBlocking(t *testing.T) {
	cases := []struct {
		name           string
		r              Review
		wantB, wantNon int
	}{
		{"graded", Review{Verdict: VerdictRequestChanges, Comments: []ReviewComment{
			{Severity: "high", Category: "perf"},
			{Severity: "medium", Category: "data-loss"},
			{Severity: "medium", Category: "test"},
			{Severity: "low", Category: "security"},
			Ungraded("x"),
		}}, 3, 2},
		{"ungraded request_changes", Review{Verdict: VerdictRequestChanges, Comments: []ReviewComment{Ungraded("a"), Ungraded("b")}}, 2, 0},
		{"ungraded approve", Review{Verdict: VerdictApprove, Comments: []ReviewComment{Ungraded("a")}}, 0, 1},
	}
	for _, tc := range cases {
		b, n := tc.r.CountBlocking()
		if b != tc.wantB || n != tc.wantNon {
			t.Errorf("%s: CountBlocking = %d, %d; want %d, %d", tc.name, b, n, tc.wantB, tc.wantNon)
		}
	}
}
