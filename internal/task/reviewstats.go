package task

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
// round-*.json` and `responses/engineer-round-*.json`, plus the same
// files of every archived cycle under `.spore/*/cycles/<n>/`, each
// cycle validated against its own state markers (operator overrides
// included). Each comment counts once under its reviewer label,
// severity and category (ungraded strings under "ungraded" and "-");
// its outcome is the last one an engineer response of the same cycle
// recorded for it, else "unrecorded". Invalid files are skipped and
// counted in Skipped.
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
		trees, err := cycleTrees(projectRoot, e.Name())
		if err != nil {
			return st, err
		}
		seen := false
		for _, tree := range trees {
			found, err := collectTree(projectRoot, tree, &st, sev, cat, who)
			if err != nil {
				return st, err
			}
			seen = seen || found
		}
		if seen {
			st.Tasks++
		}
	}
	st.ByReviewer, st.BySeverity, st.ByCategory = who.rows(), sev.rows(), cat.rows()
	return st, nil
}

// cycleTrees returns the role-file trees of slug as pseudo-slugs that
// RoleTaskDir resolves: each archived cycle (`<slug>/cycles/<n>`, in
// cycle order), then the live one. An archived cycle keeps the live
// tree's layout and its own state/, so every per-slug reader and
// marker check works on it unchanged.
func cycleTrees(projectRoot, slug string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(RoleTaskDir(projectRoot, slug), "cycles"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	var cycles []int
	for _, e := range entries {
		if n, err := strconv.Atoi(e.Name()); err == nil && e.IsDir() {
			cycles = append(cycles, n)
		}
	}
	sort.Ints(cycles)
	var out []string
	for _, n := range cycles {
		out = append(out, filepath.Join(slug, "cycles", strconv.Itoa(n)))
	}
	return append(out, slug), nil
}

// collectTree tallies one tree's reviews; found reports whether it
// held a valid one.
func collectTree(projectRoot, tree string, st *ReviewStats, sev, cat, who *tally) (bool, error) {
	outcomes, err := slugOutcomes(projectRoot, tree, st)
	if err != nil {
		return false, err
	}
	found := false
	for _, inst := range []ReviewerInstance{ReviewerA, ReviewerB} {
		rounds, err := ListReviewRounds(projectRoot, tree, inst)
		if err != nil {
			return false, err
		}
		for _, r := range rounds {
			rev, err := ReadReview(projectRoot, tree, inst, r)
			if errors.Is(err, ErrInvalidRoleFile) {
				st.Skipped++
				continue
			}
			if err != nil {
				return false, err
			}
			found = true
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
	return found, nil
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
