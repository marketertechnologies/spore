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
	// SeverityQuestion asks the engineer for an answer. It requests
	// changes only at round 1 (so the engineer answers it) and never
	// blocks after that.
	SeverityQuestion = "question"
)

// ReviewCategories is the closed set of graded comment categories.
var ReviewCategories = []string{
	"security", "correctness", "spec", "data-loss", "edge-case",
	"test", "perf", "maintainability", "nit",
}

// FinalReviewRound is each reviewer's last round (fleet's
// MaxReviewerRounds). A request_changes there escalates to the
// operator instead of looping.
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
	// Trigger is the concrete inputs or steps that cause the failure.
	// Required for high and medium, optional otherwise.
	Trigger string `json:"trigger,omitempty"`
	// Suggestion is an optional fix for Where: replacement code or a
	// small unified diff.
	Suggestion string `json:"suggestion,omitempty"`
}

// blocksAfterFirstRound reports whether c forces request_changes in
// rounds 2+: a high or medium, or a plain string inside a graded
// review (counted like a medium).
func (c ReviewComment) blocksAfterFirstRound() bool {
	switch c.Severity {
	case SeverityLow, SeverityQuestion:
		return false
	}
	return true
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
	case SeverityHigh, SeverityMedium, SeverityLow, SeverityQuestion:
	default:
		return fmt.Errorf("severity %q not one of high, medium, low, question", c.Severity)
	}
	if !knownCategory(c.Category) {
		return fmt.Errorf("category %q not one of %s", c.Category, strings.Join(ReviewCategories, ", "))
	}
	if strings.TrimSpace(c.Text) == "" {
		return errors.New(`missing required field "text"`)
	}
	if c.Severity != SeverityHigh && c.Severity != SeverityMedium {
		return nil
	}
	if strings.TrimSpace(c.Reason) == "" {
		return fmt.Errorf(`%s comment missing required field "reason" (one line why this severity)`, c.Severity)
	}
	if strings.TrimSpace(c.Trigger) == "" {
		return fmt.Errorf(`%s comment missing required field "trigger" (the inputs or steps that cause the failure; file it as low or question if you could not find one)`, c.Severity)
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

// GradeCounts tallies a review's comments by grade.
type GradeCounts struct {
	HighMedium, Low, Question, Ungraded int
}

// Counts tallies r's comments by grade; plain strings are Ungraded.
func (r Review) Counts() GradeCounts {
	var n GradeCounts
	for _, c := range r.Comments {
		switch c.Severity {
		case SeverityHigh, SeverityMedium:
			n.HighMedium++
		case SeverityLow:
			n.Low++
		case SeverityQuestion:
			n.Question++
		default:
			n.Ungraded++
		}
	}
	return n
}

// checkGrades validates every graded comment and, for a graded
// review, that the verdict follows from the grades at this round:
//
//   - round 1: any comment (any grade, questions included) requires
//     request_changes; none requires approve;
//   - rounds 2+: any high or medium requires request_changes (at
//     FinalReviewRound that escalates); only lows and questions
//     requires approve (they stay on record, nobody is woken).
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
	if round <= 1 {
		if r.Verdict != VerdictRequestChanges {
			return fmt.Errorf("round 1 verdict %q but the review has %d comment(s): in round 1 any comment (high, medium, low or question) requires %q; approve only with no comments",
				r.Verdict, len(r.Comments), VerdictRequestChanges)
		}
		return nil
	}
	first := -1
	for i, c := range r.Comments {
		if c.blocksAfterFirstRound() {
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
		return fmt.Errorf("round %d verdict %q but comments[%d] is %s: from round 2 any high or medium requires %q",
			round, r.Verdict, first, grade, VerdictRequestChanges)
	case first < 0 && r.Verdict != VerdictApprove:
		return fmt.Errorf("round %d verdict %q but every comment is low or question: from round 2 those never block and the verdict must be %q",
			round, r.Verdict, VerdictApprove)
	}
	return nil
}

// severityOrder ranks grades in the order a verdict lists them. A
// plain string in a graded review counts like a medium.
func severityOrder(c ReviewComment) int {
	switch c.Severity {
	case SeverityHigh:
		return 0
	case SeverityLow:
		return 2
	case SeverityQuestion:
		return 3
	}
	return 1
}

// checkSeverityOrder requires a graded review's comments sorted high,
// medium, low, question, so the on-disk index order is the ID order
// the engineer and the PR see. Ungraded reviews keep any order.
func checkSeverityOrder(r Review) error {
	if !r.Graded() {
		return nil
	}
	for i := 1; i < len(r.Comments); i++ {
		prev, cur := r.Comments[i-1], r.Comments[i]
		if severityOrder(cur) < severityOrder(prev) {
			return fmt.Errorf("comments[%d] (%s) follows comments[%d] (%s): sort comments by severity, high, medium, low, question, so IDs follow severity",
				i, gradeName(cur), i-1, gradeName(prev))
		}
	}
	return nil
}

func gradeName(c ReviewComment) string {
	if c.Severity == "" {
		return "ungraded"
	}
	return c.Severity
}
