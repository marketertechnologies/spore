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

// blockingMediumCategories are the categories that make a medium
// comment blocking. Any high comment blocks regardless of category.
var blockingMediumCategories = map[string]bool{
	"security": true, "correctness": true, "spec": true, "data-loss": true,
}

// blockingRule is quoted in verdict-mismatch errors so the reviewer
// sees the exact rule it broke.
const blockingRule = "blocking = any high, or medium security/correctness/spec/data-loss"

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

// Blocking reports whether a graded comment forces request_changes.
// Ungraded comments return false here; Review.Blocking decides them.
func (c ReviewComment) Blocking() bool {
	switch c.Severity {
	case SeverityHigh:
		return true
	case SeverityMedium:
		return blockingMediumCategories[c.Category]
	}
	return false
}

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
// verdict is derived from its grades; an ungraded one (only plain
// strings, or no comments) keeps the verdict as written, which is how
// every review behaved before grading existed.
func (r Review) Graded() bool {
	for _, c := range r.Comments {
		if c.IsGraded() {
			return true
		}
	}
	return false
}

// Blocking reports whether c must be resolved before r can approve.
// In a graded review a plain string counts as blocking (the reviewer
// gave it no grade to relax it). In an ungraded review every comment
// follows the written verdict.
func (r Review) Blocking(c ReviewComment) bool {
	if c.IsGraded() {
		return c.Blocking()
	}
	if r.Graded() {
		return true
	}
	return r.Verdict == VerdictRequestChanges
}

// CountBlocking returns how many comments of r are blocking and how
// many are not.
func (r Review) CountBlocking() (blocking, nonBlocking int) {
	for _, c := range r.Comments {
		if r.Blocking(c) {
			blocking++
		} else {
			nonBlocking++
		}
	}
	return blocking, nonBlocking
}

// checkGrades validates every graded comment and, for a graded
// review, that the verdict follows from the grades.
func checkGrades(r Review) error {
	firstBlocking := -1
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
	for i, c := range r.Comments {
		if r.Blocking(c) {
			firstBlocking = i
			break
		}
	}
	switch {
	case firstBlocking >= 0 && r.Verdict != VerdictRequestChanges:
		c := r.Comments[firstBlocking]
		grade := "an ungraded string"
		if c.IsGraded() {
			grade = c.Severity + " " + c.Category
		}
		return fmt.Errorf("verdict %q but comments[%d] (%s) is blocking (%s; a plain string in a graded review also blocks): verdict must be %q",
			r.Verdict, firstBlocking, grade, blockingRule, VerdictRequestChanges)
	case firstBlocking < 0 && r.Verdict != VerdictApprove:
		return fmt.Errorf("verdict %q but no comment is blocking (%s): verdict must be %q, the rest are optional for the engineer",
			r.Verdict, blockingRule, VerdictApprove)
	}
	return nil
}

// OptionalPass reports whether r earns the engineer one optional pass:
// an approve that grades at least one comment. The pass never
// re-triggers review. An ungraded approve (plain strings only, as in
// grandfathered files and pre-grading role bodies) advances at once,
// as it always did, so an in-flight loop never rewinds into a pass.
func (r Review) OptionalPass() bool {
	return r.Verdict == VerdictApprove && r.Graded()
}
