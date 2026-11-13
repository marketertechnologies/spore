package task

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
)

// OutcomeUnrecorded counts comments no engineer response gave an
// outcome for.
const OutcomeUnrecorded = "unrecorded"

// statsColumns are the outcome columns of a ReviewStats row, in order.
var statsColumns = append(append([]string{}, CommentOutcomes...), OutcomeUnrecorded)

// StatsRow tallies the comments of one severity or category.
type StatsRow struct {
	Key      string
	Raised   int
	Outcomes map[string]int
}

// ReviewStats aggregates review comments across every role-loop task
// under a project root.
type ReviewStats struct {
	Tasks, Reviews, Skipped            int
	ByReviewer, BySeverity, ByCategory []StatsRow
}

type commentKey struct {
	inst         ReviewerInstance
	round, index int
}

// CollectReviewStats scans `<projectRoot>/.spore/*/reviews/{A,B}/
// round-*.json` and `responses/engineer-round-*.json`. Each comment
// counts once under its reviewer label, severity and category (ungraded strings
// under "ungraded" and "-"); its outcome is the last one an engineer
// response recorded for it, else "unrecorded". Invalid files are
// skipped and counted in Skipped.
func CollectReviewStats(projectRoot string) (ReviewStats, error) {
	var st ReviewStats
	entries, err := os.ReadDir(filepath.Join(projectRoot, RoleTaskRoot))
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	sev := newTally(SeverityHigh, SeverityMedium, SeverityLow, SeverityQuestion, "ungraded")
	cat := newTally(append(append([]string{}, ReviewCategories...), "-")...)
	var labels []string
	for _, p := range []string{"R", "S", "C", "D", "A", "B"} {
		labels = append(labels, ReviewerLabel(p))
	}
	who := newTally(labels...)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slug := e.Name()
		outcomes, err := slugOutcomes(projectRoot, slug, &st)
		if err != nil {
			return st, err
		}
		seen := false
		for _, inst := range []ReviewerInstance{ReviewerA, ReviewerB} {
			rounds, err := ListReviewRounds(projectRoot, slug, inst)
			if err != nil {
				return st, err
			}
			for _, r := range rounds {
				rev, err := ReadReview(projectRoot, slug, inst, r)
				if errors.Is(err, ErrInvalidRoleFile) {
					st.Skipped++
					continue
				}
				if err != nil {
					return st, err
				}
				seen = true
				st.Reviews++
				label := ReviewerLabel(rev.IDPrefix(inst))
				for i, c := range rev.Comments {
					out, ok := outcomes[commentKey{inst, r, i}]
					if !ok {
						out = OutcomeUnrecorded
					}
					s, k := c.Severity, c.Category
					if !c.IsGraded() {
						s, k = "ungraded", "-"
					}
					sev.add(s, out)
					cat.add(k, out)
					who.add(label, out)
				}
			}
		}
		if seen {
			st.Tasks++
		}
	}
	st.ByReviewer, st.BySeverity, st.ByCategory = who.rows(), sev.rows(), cat.rows()
	return st, nil
}

func slugOutcomes(projectRoot, slug string, st *ReviewStats) (map[commentKey]string, error) {
	out := map[commentKey]string{}
	rounds, err := ListEngineerRounds(projectRoot, slug)
	if err != nil {
		return nil, err
	}
	for _, r := range rounds {
		resp, err := ReadEngineerResponse(projectRoot, slug, r)
		if errors.Is(err, ErrInvalidRoleFile) {
			st.Skipped++
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, o := range resp.Outcomes {
			out[commentKey{o.Reviewer, o.Round, o.Comment}] = o.Outcome
		}
	}
	return out, nil
}

type tally struct {
	order []string
	byKey map[string]*StatsRow
}

func newTally(keys ...string) *tally {
	return &tally{order: keys, byKey: map[string]*StatsRow{}}
}

func (t *tally) add(key, outcome string) {
	row, ok := t.byKey[key]
	if !ok {
		row = &StatsRow{Key: key, Outcomes: map[string]int{}}
		t.byKey[key] = row
	}
	row.Raised++
	row.Outcomes[outcome]++
}

// rows returns the non-empty rows in the tally's key order.
func (t *tally) rows() []StatsRow {
	var out []StatsRow
	for _, k := range t.order {
		if r, ok := t.byKey[k]; ok {
			out = append(out, *r)
		}
	}
	return out
}

// WriteReviewStats prints st as three tables: by reviewer (display
// label from the verdict's ID prefix), by severity, and by category.
func WriteReviewStats(w io.Writer, st ReviewStats) error {
	fmt.Fprintf(w, "%d task(s), %d review file(s)", st.Tasks, st.Reviews)
	if st.Skipped > 0 {
		fmt.Fprintf(w, ", %d invalid file(s) skipped", st.Skipped)
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, tbl := range []struct {
		name string
		rows []StatsRow
	}{{"REVIEWER", st.ByReviewer}, {"SEVERITY", st.BySeverity}, {"CATEGORY", st.ByCategory}} {
		fmt.Fprintf(tw, "\n%s\tRAISED", tbl.name)
		for _, c := range statsColumns {
			fmt.Fprintf(tw, "\t%s", strings.ToUpper(c))
		}
		fmt.Fprintln(tw)
		for _, r := range tbl.rows {
			fmt.Fprintf(tw, "%s\t%d", r.Key, r.Raised)
			for _, c := range statsColumns {
				fmt.Fprintf(tw, "\t%d", r.Outcomes[c])
			}
			fmt.Fprintln(tw)
		}
	}
	return tw.Flush()
}
