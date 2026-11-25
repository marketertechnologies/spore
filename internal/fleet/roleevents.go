package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/versality/spore/internal/events"
	"github.com/versality/spore/internal/task"
)

// recordRoleProgress writes the role loop's ledger lines: role.verdict
// once per valid review file, and role.phase when the derived phase
// differs from the one the previous drive saw. The snapshot is derived
// from files each tick, so the last phase lives in state/event-phase
// and each verdict claims a state/event-verdict-<inst>-<round> marker.
func recordRoleProgress(projectRoot, slug string, snap Snapshot) {
	stateDir := filepath.Join(task.RoleTaskDir(projectRoot, slug), "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return
	}
	for _, inst := range []task.ReviewerInstance{task.ReviewerA, task.ReviewerB} {
		rounds, err := task.ListReviewRounds(projectRoot, slug, inst)
		if err != nil {
			continue
		}
		for _, r := range rounds {
			marker := filepath.Join(stateDir, fmt.Sprintf("event-verdict-%s-%d", inst, r))
			if _, err := os.Stat(marker); err == nil {
				continue
			}
			rev, err := task.ReadReview(projectRoot, slug, inst, r)
			if err != nil {
				continue
			}
			if created, err := createMarkerExcl(marker); err != nil || !created {
				continue
			}
			n := rev.Counts()
			fields := map[string]string{
				"reviewer":    string(inst),
				"round":       strconv.Itoa(r),
				"verdict":     rev.Verdict,
				"high_medium": strconv.Itoa(n.HighMedium),
				"low":         strconv.Itoa(n.Low),
			}
			if rev.OperatorOverride != "" {
				fields["forced"] = "true"
			}
			task.RecordEvent(projectRoot, events.Event{Kind: events.RoleVerdict, Slug: slug, Fields: fields})
		}
	}

	last := filepath.Join(stateDir, "event-phase")
	prev := ""
	if b, err := os.ReadFile(last); err == nil {
		prev = strings.TrimSpace(string(b))
	}
	if prev == string(snap.Phase) || snap.Phase == "" {
		return
	}
	if err := os.WriteFile(last, []byte(string(snap.Phase)+"\n"), 0o644); err != nil {
		return
	}
	task.RecordEvent(projectRoot, events.Event{Kind: events.RolePhase, Slug: slug, Detail: string(snap.Phase),
		Fields: map[string]string{
			"from":           prev,
			"engineer_round": strconv.Itoa(snap.EngineerRound),
			"reviewer":       string(snap.CurrentReviewer),
			"reviewer_round": strconv.Itoa(snap.ReviewerRound),
		}})
}
