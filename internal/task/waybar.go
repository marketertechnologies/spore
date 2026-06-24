package task

import (
	"encoding/json"
	"fmt"
	"os"
)

// WaybarChip is the JSON payload waybar's custom module expects.
type WaybarChip struct {
	Text    string `json:"text"`
	Class   string `json:"class"`
	Tooltip string `json:"tooltip"`
}

// Waybar scans tasksDir and returns a JSON chip for waybar's custom
// module. Counts tasks by status, filters to host-local tasks (host
// matches hostname or is empty), and renders d/a/p/b/e counts. The
// `e` (escalated) bucket is independent of status: a slug counts as
// escalated when the role-loop driver has dropped an escalation
// marker under <projectRoot>/.spore/<slug>/state/, regardless of the
// task's frontmatter status (an escalation can sit on an `active`
// task while the operator decides what to do with it).
func Waybar(tasksDir, projectRoot string) ([]byte, error) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = ""
	}
	metas, err := List(tasksDir)
	if err != nil {
		return nil, err
	}

	var draft, active, paused, blocked, escalated int
	for _, m := range metas {
		if m.Host != "" && m.Host != hostname {
			continue
		}
		switch m.Status {
		case "draft":
			draft++
		case "active":
			active++
		case "paused":
			paused++
		case "blocked":
			blocked++
		}
		if projectRoot != "" {
			esc, err := IsEscalated(projectRoot, m.Slug)
			if err != nil {
				return nil, err
			}
			if esc {
				escalated++
			}
		}
	}

	class := "idle"
	switch {
	case escalated > 0:
		class = "escalated"
	case blocked > 0:
		class = "blocked"
	case active > 0:
		class = "active"
	}

	chip := WaybarChip{
		Text:    fmt.Sprintf("%d/%d/%d/%d/%d", draft, active, paused, blocked, escalated),
		Class:   class,
		Tooltip: fmt.Sprintf("draft:%d active:%d paused:%d blocked:%d escalated:%d", draft, active, paused, blocked, escalated),
	}
	return json.Marshal(chip)
}
