package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Severity values a graded review comment carries.
const (
	SeverityHigh   = "high"
	SeverityMedium = "medium"
	SeverityLow    = "low"
)

// ReviewCategories is the closed set of graded comment categories.
var ReviewCategories = []string{
	"security", "correctness", "spec", "data-loss", "edge-case",
	"test", "perf", "maintainability", "nit",
}

// FinalReviewRound is each reviewer's last round (fleet's
// MaxReviewerRounds). The verdict rule changes there: lows no longer
// block, and a request_changes escalates to the operator.
const FinalReviewRound = 3

// ReviewComment is one entry of a review's `comments`. On disk it is
// either a graded object or a plain string. A plain string decodes
// with only Text set (Severity empty) and is "ungraded": see
// Review.Graded for how ungraded comments count.
type ReviewComment struct {
	Severity string `json:"severity"`
	Category string `json:"category"`
	Where    string `json:"where,omitempty"`
	Text     string `json:"text"`
	Reason   string `json:"reason,omitempty"`
}

// Ungraded returns a legacy plain-string comment.
func Ungraded(text string) ReviewComment { return ReviewComment{Text: text} }

// IsGraded reports whether c came from the object shape.
func (c ReviewComment) IsGraded() bool { return c.Severity != "" }

// UnmarshalJSON accepts a plain string (ungraded) or the graded
// object. The object is always decoded strictly: it postdates the
// legacy grandfathering, so no old file can carry a stray key in it.
func (c *ReviewComment) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*c = ReviewComment{Text: s}
		return nil
	}
	type plain ReviewComment
	var p plain
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return fmt.Errorf("comment: %v", err)
	}
	if p.Severity == "" {
		return errors.New(`comment object missing required field "severity"`)
	}
	*c = ReviewComment(p)
	return nil
}

// MarshalJSON writes an ungraded comment back as a plain string so a
// legacy review round-trips byte-compatible.
func (c ReviewComment) MarshalJSON() ([]byte, error) {
	if !c.IsGraded() {
		return json.Marshal(c.Text)
	}
	type plain ReviewComment
	return json.Marshal(plain(c))
}

func (c ReviewComment) validate() error {
	switch c.Severity {
	case SeverityHigh, SeverityMedium, SeverityLow:
	default:
		return fmt.Errorf("severity %q not one of high, medium, low", c.Severity)
	}
	if !knownCategory(c.Category) {
		return fmt.Errorf("category %q not one of %s", c.Category, strings.Join(ReviewCategories, ", "))
	}
	if strings.TrimSpace(c.Text) == "" {
		return errors.New(`missing required field "text"`)
	}
	if c.Severity != SeverityLow && strings.TrimSpace(c.Reason) == "" {
		return fmt.Errorf(`%s comment missing required field "reason" (one line why this severity)`, c.Severity)
	}
	return nil
}

func knownCategory(cat string) bool {
	for _, k := range ReviewCategories {
		if k == cat {
			return true
		}
	}
	return false
}

// Graded reports whether r grades any comment. A graded review's
// verdict is derived from its grades and round; an ungraded one (only
// plain strings, or no comments) keeps the verdict as written, which
// is how every review behaved before grading existed.
func (r Review) Graded() bool {
	for _, c := range r.Comments {
		if c.IsGraded() {
			return true
		}
	}
	return false
}

// SeverityCounts tallies r's comments by grade: high or medium, low,
// and ungraded (plain strings).
func (r Review) SeverityCounts() (highMedium, low, ungraded int) {
	for _, c := range r.Comments {
		switch c.Severity {
		case SeverityHigh, SeverityMedium:
			highMedium++
		case SeverityLow:
			low++
		default:
			ungraded++
		}
	}
	return highMedium, low, ungraded
}

// checkGrades validates every graded comment and, for a graded
// review, that the verdict follows from the grades at this round:
//
//   - rounds before FinalReviewRound: any comment requires
//     request_changes (the engineer revises, the reviewer re-reviews);
//   - FinalReviewRound: any high or medium requires request_changes
//     (escalation); only lows requires approve (lows are dropped).
//
// A plain string inside a graded review counts like a medium.
func checkGrades(r Review, round int) error {
	for i, c := range r.Comments {
		if !c.IsGraded() {
			continue
		}
		if err := c.validate(); err != nil {
			return fmt.Errorf("comments[%d]: %v", i, err)
		}
	}
	if !r.Graded() {
		return nil
	}
	if round < FinalReviewRound {
		if r.Verdict != VerdictRequestChanges {
			return fmt.Errorf("round %d verdict %q but the review has %d comment(s): in rounds 1-%d any comment (high, medium or low) requires %q; approve only with no comments",
				round, r.Verdict, len(r.Comments), FinalReviewRound-1, VerdictRequestChanges)
		}
		return nil
	}
	first := -1
	for i, c := range r.Comments {
		if c.Severity != SeverityLow {
			first = i
			break
		}
	}
	switch {
	case first >= 0 && r.Verdict != VerdictRequestChanges:
		grade := r.Comments[first].Severity
		if grade == "" {
			grade = "an ungraded string"
		}
		return fmt.Errorf("round %d (final) verdict %q but comments[%d] is %s: at the final round any high or medium requires %q (escalates to the operator)",
			round, r.Verdict, first, grade, VerdictRequestChanges)
	case first < 0 && r.Verdict != VerdictApprove:
		return fmt.Errorf("round %d (final) verdict %q but every comment is low: at the final round lows are ignored and the verdict must be %q",
			round, r.Verdict, VerdictApprove)
	}
	return nil
}
