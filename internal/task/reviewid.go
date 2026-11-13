package task

import "fmt"

// Reviewer slot modes, mirrored by fleet.ReviewerModeFleet and
// fleet.ReviewerModeCodeReview. They live here so display identity
// (ReviewerPrefix) can be derived without importing fleet.
const (
	ReviewModeFleet      = "fleet"
	ReviewModeCodeReview = "code-review"
)

// reviewerPrefixes maps each comment ID prefix to its display name.
// R and C name a slot by its mode; S and D are the second slot when
// both slots share a mode. A and B are the fallback for verdict files
// that predate prefixes (or come from a project role body that does
// not write one).
var reviewerPrefixes = map[string]string{
	"R": "Requirements review",
	"S": "Requirements review 2",
	"C": "Code review",
	"D": "Code review 2",
	"A": "Reviewer A",
	"B": "Reviewer B",
}

// ReviewerPrefix returns the comment ID prefix of inst's slot, given
// both slots' resolved modes: R for a fleet (requirements) slot, C for
// a code-review slot. When both slots share a mode, B takes the next
// letter (S or D) so IDs stay unique per task. An unknown mode falls
// back to the instance letter.
func ReviewerPrefix(inst ReviewerInstance, modeA, modeB string) string {
	base := func(mode string) string {
		switch mode {
		case ReviewModeFleet:
			return "R"
		case ReviewModeCodeReview:
			return "C"
		}
		return ""
	}
	mine := base(modeA)
	if inst == ReviewerB {
		mine = base(modeB)
		if mine != "" && mine == base(modeA) {
			mine = string(rune(mine[0] + 1))
		}
	}
	if mine == "" {
		return string(inst)
	}
	return mine
}

// ReviewerLabel returns the display name for a comment ID prefix:
// "Requirements review", "Code review 2", "Reviewer A".
func ReviewerLabel(prefix string) string {
	if l, ok := reviewerPrefixes[prefix]; ok {
		return l
	}
	return "Reviewer " + prefix
}

func knownPrefix(prefix string) bool {
	_, ok := reviewerPrefixes[prefix]
	return ok
}

// IDPrefix returns the verdict's comment ID prefix: its recorded
// `prefix`, else the instance letter for files written without one.
func (r Review) IDPrefix(inst ReviewerInstance) string {
	if r.Prefix != "" {
		return r.Prefix
	}
	return string(inst)
}

// CommentID renders the stable visible ID of the comment at 0-based
// index in a round's verdict: `<prefix><round>.<index+1>`, e.g. R1.3.
func CommentID(prefix string, round, index int) string {
	return fmt.Sprintf("%s%d.%d", prefix, round, index+1)
}
