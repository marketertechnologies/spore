package task

import (
	"path/filepath"
	"time"

	"github.com/versality/spore/internal/events"
)

// recordEvent appends e to the ledger of the project that owns
// tasksDir. Best-effort, like every ledger write.
func recordEvent(tasksDir string, e events.Event) {
	root, err := projectRootFromTasksDir(tasksDir)
	if err != nil {
		return
	}
	RecordEvent(root, e)
}

// RecordEvent appends e to projectRoot's ledger (keyed by the main
// repo's basename) and notes the root for `spore report --all`.
func RecordEvent(projectRoot string, e events.Event) {
	root, err := MainRepoRoot(projectRoot)
	if err != nil {
		return
	}
	project := filepath.Base(root)
	events.NoteRoot(project, root)
	events.Record(project, e)
}

// RecordEventThrottled is RecordEvent at most once per every for key
// (see events.RecordThrottled).
func RecordEventThrottled(projectRoot, key string, every time.Duration, e events.Event) {
	root, err := MainRepoRoot(projectRoot)
	if err != nil {
		return
	}
	project := filepath.Base(root)
	events.NoteRoot(project, root)
	events.RecordThrottled(project, key, every, e)
}
