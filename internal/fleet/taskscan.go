package fleet

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/versality/spore/internal/hooks"
	"github.com/versality/spore/internal/task"
	"github.com/versality/spore/internal/task/frontmatter"
)

// badTask is a tasks/*.md file the reconciler cannot act on.
type badTask struct {
	File   string
	Reason string
}

// scanTasks is task.List for the reconciler: it parses every
// tasks/*.md and splits the files into usable metas and bad ones
// (no frontmatter, no slug, slug not matching the file name, or a
// slug that cannot name the wt/<slug> branch). A bad file must never
// reach assignAgent or task.Ensure, where it would abort the pass.
func scanTasks(tasksDir string) ([]frontmatter.Meta, []badTask, error) {
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var metas []frontmatter.Meta
	var bad []badTask
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(tasksDir, e.Name()))
		if err != nil {
			bad = append(bad, badTask{File: e.Name(), Reason: err.Error()})
			continue
		}
		m, _, err := frontmatter.Parse(b)
		if err == nil {
			err = checkTaskSlug(e.Name(), m.Slug)
		}
		if err != nil {
			bad = append(bad, badTask{File: e.Name(), Reason: err.Error()})
			continue
		}
		metas = append(metas, m)
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Slug < metas[j].Slug })
	return metas, bad, nil
}

func checkTaskSlug(file, slug string) error {
	if slug == "" {
		return errors.New("no slug in frontmatter")
	}
	if stem := strings.TrimSuffix(file, ".md"); slug != stem {
		return fmt.Errorf("slug %q does not match file name %q", slug, stem)
	}
	if strings.Contains(slug, "/") || !validBranchName("wt/"+slug) {
		return fmt.Errorf("slug %q is not a valid branch name (wt/%s)", slug, slug)
	}
	return nil
}

// validBranchName applies the `git check-ref-format --branch` rules
// in-process, so a bad slug is caught without a git call.
func validBranchName(name string) bool {
	if name == "" || name == "@" || strings.HasPrefix(name, "-") ||
		strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") ||
		strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.Contains(name, "//") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return false
		}
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// noticeBadTasks tells the coordinator once per bad file and reason.
// Markers live under the project state dir; a marker whose file is no
// longer bad is dropped, so a file that breaks again is noticed again.
func noticeBadTasks(projectRoot string, bad []badTask) []string {
	var warnings []string
	for _, b := range bad {
		w := fmt.Sprintf("task file %s skipped: %s", b.File, b.Reason)
		fmt.Fprintln(os.Stderr, w)
		warnings = append(warnings, w)
	}
	dir, err := fleetStateDir(projectRoot, "bad-tasks")
	if err != nil {
		fmt.Fprintf(os.Stderr, "fleet: bad-task markers: %v\n", err)
		return warnings
	}
	keep := map[string]bool{}
	for _, b := range bad {
		sum := sha256.Sum256([]byte(b.File + "\x00" + b.Reason))
		name := hex.EncodeToString(sum[:8])
		keep[name] = true
		created, err := createMarkerExcl(filepath.Join(dir, name))
		if err != nil {
			fmt.Fprintf(os.Stderr, "fleet: bad-task marker: %v\n", err)
			continue
		}
		if created {
			notifyFleet(projectRoot, fmt.Sprintf("tasks/%s skipped by the fleet: %s. Fix the frontmatter; the other tasks keep running.", b.File, b.Reason))
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !keep[e.Name()] {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return warnings
}

// fleetStateDir returns (and creates) <project state dir>/fleet/<sub>:
// reconciler bookkeeping that must not live in the repo tree.
func fleetStateDir(projectRoot string, sub ...string) (string, error) {
	base, err := task.StateDirForProject(projectRoot)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(append([]string{base, "fleet"}, sub...)...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// notifyFleet drops a tell envelope into the coordinator inbox.
func notifyFleet(projectRoot, body string) {
	project, err := task.ProjectName(projectRoot)
	if err != nil || project == "" {
		project = filepath.Base(projectRoot)
	}
	if err := hooks.NotifyCoordinatorEvent(project, "fleet", body); err != nil {
		fmt.Fprintf(os.Stderr, "fleet notify coordinator: %v\n", err)
	}
}
