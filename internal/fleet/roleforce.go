package fleet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/versality/spore/internal/task"
)

// ForceApproveReview flips reviewer inst's latest verdict to approve
// with the operator's reason in `operator_override`, which exempts it
// from the grade/round verdict rule (see task.Review.Forced). Every
// other field, comments included, is kept. Returns the
// round it overrode.
func ForceApproveReview(projectRoot, tasksDir, slug string, inst task.ReviewerInstance, reason string) (int, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return 0, fmt.Errorf("force-approve %s: a reason is required; it is posted with the override", slug)
	}
	if err := task.RequireRoleLoop(tasksDir, slug); err != nil {
		return 0, fmt.Errorf("force-approve %s: %w", slug, err)
	}
	rounds, err := task.ListReviewRounds(projectRoot, slug, inst)
	if err != nil {
		return 0, fmt.Errorf("force-approve %s: %w", slug, err)
	}
	if len(rounds) == 0 {
		return 0, fmt.Errorf("force-approve %s: reviewer %s has no verdict yet", slug, inst)
	}
	round := rounds[len(rounds)-1]
	unlock, ok := lockRoleDrive(projectRoot, slug)
	if !ok {
		return 0, fmt.Errorf("force-approve %s: a drive tick holds the loop; retry", slug)
	}
	defer unlock()

	path := task.ReviewPath(projectRoot, slug, inst, round)
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return 0, fmt.Errorf("force-approve %s: %s: %w", slug, path, err)
	}
	var verdict string
	_ = json.Unmarshal(doc["verdict"], &verdict)
	if verdict == task.VerdictApprove {
		return 0, fmt.Errorf("force-approve %s: reviewer %s round %d already approves", slug, inst, round)
	}
	doc["verdict"], _ = json.Marshal(task.VerdictApprove)
	doc["operator_override"], _ = json.Marshal(reason)
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return 0, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out.Bytes(), 0o644); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, err
	}
	if _, err := task.ReadReview(projectRoot, slug, inst, round); err != nil {
		return 0, fmt.Errorf("force-approve %s: override written but the file does not validate: %w", slug, err)
	}
	return round, nil
}
