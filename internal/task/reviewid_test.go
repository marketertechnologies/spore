package task

import "testing"

func TestReviewerPrefixAndLabel(t *testing.T) {
	cases := []struct {
		name         string
		modeA, modeB string
		wantA, wantB string
		labelA       string
		labelB       string
	}{
		{"default order", "fleet", "code-review", "R", "C", "Requirements review", "Code review"},
		{"old order", "code-review", "fleet", "C", "R", "Code review", "Requirements review"},
		{"both fleet", "fleet", "fleet", "R", "S", "Requirements review", "Requirements review 2"},
		{"both code-review", "code-review", "code-review", "C", "D", "Code review", "Code review 2"},
		{"unknown mode", "", "lgtm", "A", "B", "Reviewer A", "Reviewer B"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := ReviewerPrefix(ReviewerA, c.modeA, c.modeB)
			b := ReviewerPrefix(ReviewerB, c.modeA, c.modeB)
			if a != c.wantA || b != c.wantB {
				t.Errorf("prefixes = %s, %s, want %s, %s", a, b, c.wantA, c.wantB)
			}
			if a == b {
				t.Errorf("slots share prefix %s", a)
			}
			if got := ReviewerLabel(a); got != c.labelA {
				t.Errorf("label A = %q, want %q", got, c.labelA)
			}
			if got := ReviewerLabel(b); got != c.labelB {
				t.Errorf("label B = %q, want %q", got, c.labelB)
			}
		})
	}
}

func TestCommentID(t *testing.T) {
	if got := CommentID("R", 1, 2); got != "R1.3" {
		t.Errorf("CommentID = %q, want R1.3", got)
	}
	if got := CommentID("C", 2, 0); got != "C2.1" {
		t.Errorf("CommentID = %q, want C2.1", got)
	}
}
