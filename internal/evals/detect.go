package evals

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/versality/spore/internal/task"
)

// Slots is the reviewer phase order. The loop runs one slot's phase
// to approval before the next slot starts; detection relies only on
// that order, never on what a slot checks.
var Slots = []task.ReviewerInstance{task.ReviewerA, task.ReviewerB}

// Candidate sources.
const (
	SourceEngineer = "engineer"
	SourceOperator = "operator"
	sourceReviewer = "reviewer:"
)

// Candidate is a detected reviewer mistake not yet confirmed or
// dismissed.
type Candidate struct {
	ID   string
	Kind string
	Slug string
	// Reviewer and Round name the verdict being replayed: the slot
	// that made the mistake and its round.
	Reviewer task.ReviewerInstance
	Round    int
	Source   string
	// Evidence is the text the expect draft is built from: the later
	// slot's blocker comments, the engineer's pushback entry, or the
	// flagged comment.
	Evidence []string
	Why      string
	// Trigger is the engineer round the replayed verdict answered;
	// the replay sees responses 1..Trigger.
	Trigger int
}

// DraftExpect is the judge-facing expect text confirm writes when
// the operator passes none.
func (c Candidate) DraftExpect() string {
	switch c.Kind {
	case KindMiss:
		return "A [blocker] comment flags the defect a later review caught: " + strings.Join(c.Evidence, " / ")
	default:
		s := "No comment raises this rejected point again: " + strings.Join(c.Evidence, " / ")
		if c.Why != "" {
			s += ". Why it is wrong: " + c.Why
		}
		return s
	}
}

type verdict struct {
	slot    task.ReviewerInstance
	round   int
	review  task.Review
	trigger int
}

// timeline returns every valid verdict of slug in phase order, each
// with the engineer round it answered. Engineer round 1 is the
// initial handoff; every request_changes consumes the next one.
func timeline(projectRoot, slug string) ([]verdict, error) {
	var out []verdict
	rc := 0
	for _, slot := range Slots {
		rounds, err := task.ListReviewRounds(projectRoot, slug, slot)
		if err != nil {
			return nil, err
		}
		for _, r := range rounds {
			rev, err := task.ReadReview(projectRoot, slug, slot, r)
			if errors.Is(err, task.ErrInvalidRoleFile) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("%s: read %s round %d: %w", slug, slot, r, err)
			}
			out = append(out, verdict{slot: slot, round: r, review: rev, trigger: 1 + rc})
			if rev.Verdict == task.VerdictRequestChanges {
				rc++
			}
		}
	}
	return out, nil
}

func slotIndex(s task.ReviewerInstance) int {
	for i, x := range Slots {
		if x == s {
			return i
		}
	}
	return -1
}

// blockers returns the comments a verdict blocks on: tagged
// [blocker] or untagged, which the loop treats as a blocker.
func blockers(comments []string) []string {
	var out []string
	for _, c := range comments {
		t := strings.TrimSpace(c)
		if strings.HasPrefix(t, "[nice-to-have]") || strings.HasPrefix(t, "[nit]") {
			continue
		}
		out = append(out, t)
	}
	return out
}

// detectSlug returns the miss and engineer-pushback candidates of
// one task tree.
func detectSlug(projectRoot, slug string) ([]Candidate, error) {
	tl, err := timeline(projectRoot, slug)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, x := range tl {
		if x.review.Verdict != task.VerdictApprove {
			continue
		}
		for _, y := range tl {
			// Only a later verdict on the same engineer round saw the
			// code x approved; later rounds review fix code.
			if slotIndex(y.slot) <= slotIndex(x.slot) || y.review.Verdict != task.VerdictRequestChanges || y.trigger != x.trigger {
				continue
			}
			out = append(out, Candidate{
				ID:       fmt.Sprintf("%s.miss.%s%d.%s%d", slug, x.slot, x.round, y.slot, y.round),
				Kind:     KindMiss,
				Slug:     slug,
				Reviewer: x.slot,
				Round:    x.round,
				Source:   sourceReviewer + string(y.slot),
				Evidence: blockers(y.review.Comments),
				Trigger:  x.trigger,
			})
		}
	}
	rcs := make([]verdict, 0, len(tl))
	for _, v := range tl {
		if v.review.Verdict == task.VerdictRequestChanges {
			rcs = append(rcs, v)
		}
	}
	engRounds, err := task.ListEngineerRounds(projectRoot, slug)
	if err != nil {
		return nil, err
	}
	for _, r := range engRounds {
		if r < 2 || r-2 >= len(rcs) {
			continue
		}
		resp, err := task.ReadEngineerResponse(projectRoot, slug, r)
		if errors.Is(err, task.ErrInvalidRoleFile) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s: read engineer round %d: %w", slug, r, err)
		}
		owner := rcs[r-2]
		for i, p := range resp.Pushback {
			p = strings.TrimSpace(p)
			if p == "" || strings.HasPrefix(strings.ToLower(p), "skipped:") {
				continue
			}
			out = append(out, Candidate{
				ID:       fmt.Sprintf("%s.invalid.%s%d.eng%d.p%d", slug, owner.slot, owner.round, r, i+1),
				Kind:     KindInvalid,
				Slug:     slug,
				Reviewer: owner.slot,
				Round:    owner.round,
				Source:   SourceEngineer,
				Evidence: []string{p},
				Trigger:  owner.trigger,
			})
		}
	}
	return out, nil
}

// slugs lists the task trees under <projectRoot>/.spore/.
func slugs(projectRoot string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(projectRoot, task.RoleTaskRoot))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// AllCandidates detects every candidate in the project, confirmed and
// dismissed ones included. Candidates lists only the open ones.
func (s Store) AllCandidates() ([]Candidate, error) {
	names, err := slugs(s.ProjectRoot)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, slug := range names {
		cs, err := detectSlug(s.ProjectRoot, slug)
		if err != nil {
			return nil, err
		}
		out = append(out, cs...)
	}
	flags, err := s.flagCandidates()
	if err != nil {
		return nil, err
	}
	out = append(out, flags...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Candidates lists the candidates not yet turned into cases or
// dismissed.
func (s Store) Candidates() ([]Candidate, error) {
	all, err := s.AllCandidates()
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, c := range all {
		closed, err := s.closed(c.ID)
		if err != nil {
			return nil, err
		}
		if !closed {
			out = append(out, c)
		}
	}
	return out, nil
}

// Candidate returns the open candidate id.
func (s Store) Candidate(id string) (Candidate, error) {
	open, err := s.Candidates()
	if err != nil {
		return Candidate{}, err
	}
	for _, c := range open {
		if c.ID == id {
			return c, nil
		}
	}
	return Candidate{}, fmt.Errorf("no open candidate %q (see `spore eval candidates`)", id)
}

// findVerdict returns the timeline entry for slot round.
func findVerdict(projectRoot, slug string, slot task.ReviewerInstance, round int) (verdict, error) {
	tl, err := timeline(projectRoot, slug)
	if err != nil {
		return verdict{}, err
	}
	for _, v := range tl {
		if v.slot == slot && v.round == round {
			return v, nil
		}
	}
	return verdict{}, fmt.Errorf("%s: no valid verdict for reviewer %s round %d", slug, slot, round)
}
