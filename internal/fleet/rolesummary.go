package fleet

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/versality/spore/internal/task"
)

// summaryFingerprintPrefix is the leading marker the synthesised
// summary embeds (after the date line). Encodes the counts of inputs
// the summary was built from, so a later drive tick can tell whether
// the on-disk file is stale relative to the current artifact tree.
const summaryFingerprintPrefix = "<!-- summary-fingerprint:"

// writeSummary synthesises .spore/<slug>/summary.md from the on-disk
// review threads. Idempotent across drive ticks with stable inputs,
// but regenerates when the embedded fingerprint disagrees with the
// current artifact counts: a leftover summary from a previous task
// lifecycle (different engineer/reviewer round counts) cannot shadow
// a fresh run, and a hand-edited summary that strips the fingerprint
// is treated as stale (the marker fence is rebuilt).
func writeSummary(projectRoot, slug string) error {
	path := filepath.Join(task.RoleTaskDir(projectRoot, slug), "summary.md")
	fp, err := computeSummaryFingerprint(projectRoot, slug)
	if err != nil {
		return err
	}
	if existing, err := os.ReadFile(path); err == nil {
		if extractSummaryFingerprint(existing) == fp {
			return nil
		}
	}
	body, err := buildSummary(projectRoot, slug, fp)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

// computeSummaryFingerprint returns the artifact-count signature the
// next writeSummary will compare against the file's embedded marker.
// Shape: "engineer=<E> A=<NA> B=<NB>".
func computeSummaryFingerprint(projectRoot, slug string) (string, error) {
	eng, err := task.ListEngineerRounds(projectRoot, slug)
	if err != nil {
		return "", err
	}
	a, err := task.ListReviewRounds(projectRoot, slug, task.ReviewerA)
	if err != nil {
		return "", err
	}
	b, err := task.ListReviewRounds(projectRoot, slug, task.ReviewerB)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("engineer=%d A=%d B=%d", len(eng), len(a), len(b)), nil
}

// extractSummaryFingerprint reads the embedded marker out of an
// existing summary.md body. Returns "" when the marker is missing or
// malformed; the caller treats that as "regenerate".
func extractSummaryFingerprint(body []byte) string {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, summaryFingerprintPrefix) {
			continue
		}
		line = strings.TrimPrefix(line, summaryFingerprintPrefix)
		line = strings.TrimSuffix(line, "-->")
		return strings.TrimSpace(line)
	}
	return ""
}

func buildSummary(projectRoot, slug, fingerprint string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: review summary\n\n", slug)
	fmt.Fprintf(&b, "Generated %s\n\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "%s %s -->\n\n", summaryFingerprintPrefix, fingerprint)

	for _, instance := range []task.ReviewerInstance{task.ReviewerA, task.ReviewerB} {
		rounds, err := task.ListReviewRounds(projectRoot, slug, instance)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "## reviewer %s\n\n", instance)
		if len(rounds) == 0 {
			b.WriteString("No rounds recorded.\n\n")
			continue
		}
		for _, r := range rounds {
			rev, err := task.ReadReview(projectRoot, slug, instance, r)
			if errors.Is(err, task.ErrInvalidRoleFile) {
				continue
			}
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "### round %d -> %s\n\n", r, rev.Verdict)
			if rev.Summary != "" {
				fmt.Fprintf(&b, "%s\n\n", rev.Summary)
			}
			if len(rev.Comments) > 0 {
				b.WriteString(renderReviewComments(instance, r, rev))
				b.WriteString("\n")
			}
		}
	}

	engRounds, err := task.ListEngineerRounds(projectRoot, slug)
	if err != nil {
		return "", err
	}
	if len(engRounds) > 0 {
		b.WriteString("## engineer responses\n\n")
		for _, r := range engRounds {
			resp, err := task.ReadEngineerResponse(projectRoot, slug, r)
			if errors.Is(err, task.ErrInvalidRoleFile) {
				continue
			}
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "### round %d\n\n", r)
			writeEngineerResponse(&b, projectRoot, slug, resp)
		}
	}

	return b.String(), nil
}

// writeEngineerResponse renders resp for the summary and the PR
// mirror's engineer comment: a table of its structured `outcomes` in
// ID order (grade joined from the verdict files), then the free-text
// lists and notes. An `addressed` / `pushback` entry that leads with
// an ID the table already details is dropped as a repeat.
func writeEngineerResponse(b *strings.Builder, projectRoot, slug string, resp task.EngineerResponse) {
	rows := outcomeRows(projectRoot, slug, resp.Outcomes)
	covered := map[string]bool{}
	if len(rows) > 0 {
		b.WriteString("| ID | Grade | Outcome | Details |\n| --- | --- | --- | --- |\n")
		for _, r := range rows {
			fmt.Fprintf(b, "| %s | %s | %s | %s |\n", r.id, r.grade, r.outcome, tableCell(r.details))
			if r.details != "" {
				covered[r.id] = true
			}
		}
		b.WriteString("\n")
	}
	list := func(head string, items []string) {
		var keep []string
		for _, c := range items {
			if m := leadingIDRE.FindStringSubmatch(c); m != nil && covered[m[1]] {
				continue
			}
			keep = append(keep, c)
		}
		if len(keep) == 0 {
			return
		}
		b.WriteString(head + ":\n")
		for _, c := range keep {
			fmt.Fprintf(b, "- %s\n", c)
		}
		b.WriteString("\n")
	}
	list("Addressed", resp.Addressed)
	list("Pushback", resp.Pushback)
	if resp.Notes != "" {
		fmt.Fprintf(b, "Notes: %s\n\n", resp.Notes)
	}
	list("Proposed follow-ups", resp.Followups)
}

// leadingIDRE matches a free-text entry that starts with a comment ID
// such as `R1.3:` or `**C2.1**`.
var leadingIDRE = regexp.MustCompile(`^\W*([A-Z]\d+\.\d+)\b`)

type outcomeRow struct {
	inst                        task.ReviewerInstance
	round, index                int
	id, grade, outcome, details string
}

// outcomeRows resolves each outcome against its verdict file for the
// ID prefix and grade, sorted by slot, round, then comment index. A
// verdict that is missing or invalid, or an index past its comments,
// renders grade `?` with the instance letter as prefix.
func outcomeRows(projectRoot, slug string, outs []task.CommentOutcome) []outcomeRow {
	type key struct {
		inst  task.ReviewerInstance
		round int
	}
	reviews := map[key]*task.Review{}
	rows := make([]outcomeRow, 0, len(outs))
	for _, o := range outs {
		k := key{o.Reviewer, o.Round}
		rev, seen := reviews[k]
		if !seen {
			if r, err := task.ReadReview(projectRoot, slug, o.Reviewer, o.Round); err == nil {
				rev = &r
			}
			reviews[k] = rev
		}
		prefix, grade := string(o.Reviewer), "?"
		if rev != nil {
			prefix = rev.IDPrefix(o.Reviewer)
			if o.Comment < len(rev.Comments) {
				grade = rev.Comments[o.Comment].Severity
				if grade == "" {
					grade = "ungraded"
				}
			}
		}
		details := strings.TrimSpace(o.Detail)
		if o.SHA != "" {
			details = strings.TrimSpace("`" + o.SHA + "` " + details)
		}
		rows = append(rows, outcomeRow{
			inst: o.Reviewer, round: o.Round, index: o.Comment,
			id: task.CommentID(prefix, o.Round, o.Comment), grade: grade,
			outcome: strings.ReplaceAll(o.Outcome, "_", " "), details: details,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.inst != b.inst {
			return a.inst < b.inst
		}
		if a.round != b.round {
			return a.round < b.round
		}
		return a.index < b.index
	})
	return rows
}

// tableCell keeps a value on one markdown table row.
func tableCell(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.ReplaceAll(s, "|", "\\|")
}
