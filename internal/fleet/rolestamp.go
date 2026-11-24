package fleet

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/versality/spore/internal/task"
)

// stampReviewedSHAs records the commit each unstamped verdict
// reviewed, for `spore eval confirm`. The worktree HEAD counts only
// when it is no newer than the verdict file: a newer commit means the
// engineer moved on and the reviewed commit is unknown, so the round
// gets an empty stamp rather than a wrong one. Best effort: a failure
// here must not stall the loop.
func stampReviewedSHAs(projectRoot, slug string) {
	var head string
	var headAt time.Time
	loaded := false
	for _, inst := range []task.ReviewerInstance{task.ReviewerA, task.ReviewerB} {
		rounds, err := task.ListReviewRounds(projectRoot, slug, inst)
		if err != nil {
			continue
		}
		for _, r := range rounds {
			stamp := task.ReviewedSHAPath(projectRoot, slug, inst, r)
			if _, err := os.Stat(stamp); err == nil {
				continue
			}
			fi, err := os.Stat(task.ReviewPath(projectRoot, slug, inst, r))
			if err != nil {
				continue
			}
			if !loaded {
				head, headAt = worktreeHead(filepath.Join(projectRoot, ".worktrees", slug))
				loaded = true
			}
			if head == "" {
				continue
			}
			if headAt.After(fi.ModTime()) {
				// Unknowable from here on: an empty stamp stops the
				// re-check, and confirm reads it as absent.
				_ = writePrivateFile(stamp, "")
				continue
			}
			_ = writePrivateFile(stamp, head+"\n")
		}
	}
}

func worktreeHead(dir string) (string, time.Time) {
	out, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%H %ct").Output()
	if err != nil {
		return "", time.Time{}
	}
	sha, ts, ok := strings.Cut(strings.TrimSpace(string(out)), " ")
	sec, err := strconv.ParseInt(ts, 10, 64)
	if !ok || err != nil {
		return "", time.Time{}
	}
	return sha, time.Unix(sec, 0)
}
