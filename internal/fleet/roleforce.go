package fleet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/versality/spore/internal/task"
)

// ForceApproveReview flips reviewer inst's final, escalated verdict to
// approve with the operator's reason in `operator_override`, which
// exempts it from the grade/round verdict rule (see task.Review.Forced).
// Every other field, comments included, is kept. Returns the round it
// overrode.
//
// Only an escalation whose fix pass has landed can be forced. Earlier
// verdicts were answered (or are being answered) by an engineer round
// the phase machine pairs with them; flipping one would shift that
// pairing onto the next reviewer's verdicts.
func ForceApproveReview(projectRoot, tasksDir, slug string, inst task.ReviewerInstance, reason string) (int, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return 0, fmt.Errorf("force-approve %s: a reason is required; it is posted with the override", slug)
	}
	if err := task.RequireRoleLoop(tasksDir, slug); err != nil {
		return 0, fmt.Errorf("force-approve %s: %w", slug, err)
	}
	unlock, err := lockRoleDrive(projectRoot, slug)
	if err != nil {
		return 0, lockRefusal("force-approve", slug, "a drive tick holds the loop", err)
	}
	defer unlock()
	snap, err := DeriveSnapshot(projectRoot, slug)
	if err != nil {
		return 0, fmt.Errorf("force-approve %s: snapshot: %w", slug, err)
	}
	if snap.Phase != PhaseEscalated || snap.CurrentReviewer != inst {
		return 0, fmt.Errorf("force-approve %s: loop is in phase %s (reviewer %s); only reviewer %s's escalated final verdict can be forced",
			slug, snap.Phase, snap.CurrentReviewer, inst)
	}
	if snap.EscalationFixPending {
		return 0, fmt.Errorf("force-approve %s: the engineer's fix pass for reviewer %s round %d has not landed; retry once engineer-round-%d.json is written",
			slug, inst, snap.ReviewerRound, snap.EngineerRound+1)
	}
	rounds, err := task.ListReviewRounds(projectRoot, slug, inst)
	if err != nil {
		return 0, fmt.Errorf("force-approve %s: %w", slug, err)
	}
	// The snapshot skipped invalid files; force the verdict it counted.
	round := 0
	for i := len(rounds) - 1; i >= 0 && round == 0; i-- {
		if _, err := task.ReadReview(projectRoot, slug, inst, rounds[i]); err == nil {
			round = rounds[i]
		}
	}
	if round == 0 {
		return 0, fmt.Errorf("force-approve %s: reviewer %s has no valid verdict", slug, inst)
	}

	path := task.ReviewPath(projectRoot, slug, inst, round)
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return 0, fmt.Errorf("force-approve %s: %s: %w", slug, path, err)
	}
	doc["verdict"], _ = json.Marshal(task.VerdictApprove)
	doc["operator_override"], _ = json.Marshal(reason)
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return 0, err
	}
	if _, err := task.ParseForcedReviewAt(projectRoot, slug, inst, round, out.Bytes()); err != nil {
		return 0, fmt.Errorf("force-approve %s: override does not validate; verdict left as it was: %w", slug, err)
	}
	// Recorded before the write so the file never sits on disk with an
	// unrecorded override; dropped again if the write fails, or the
	// still-live reviewer could write a self-approval that validates.
	if err := task.RecordOperatorOverride(projectRoot, slug, inst, round); err != nil {
		return 0, fmt.Errorf("force-approve %s: record override: %w", slug, err)
	}
	if err := writeFileAtomic(path, out.Bytes()); err != nil {
		if derr := task.DropOperatorOverride(projectRoot, slug, inst, round); derr != nil {
			return 0, fmt.Errorf("force-approve %s: %v; dropping the override record also failed: %w", slug, err, derr)
		}
		return 0, fmt.Errorf("force-approve %s: %w", slug, err)
	}
	return round, nil
}

// writeFileAtomic replaces path via a sibling temp file, so a reader or
// a crash never sees a truncated file.
func writeFileAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
