package spore

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every spore state path falls back to ~/.local/state only when
// XDG_STATE_HOME is unset. A file that joins ".local", "state" without
// reading XDG_STATE_HOME builds a path a sandbox with a private
// XDG_STATE_HOME cannot redirect.
func TestStatePathsHonorXDGStateHome(t *testing.T) {
	notSpore := map[string]string{
		"internal/budget/usage.go":     "claude-accounts belongs to the account switcher, not spore",
		"internal/hooks/watchinbox.go": "WT_STATE (wt's layout) overrides it; wt does not read XDG_STATE_HOME",
	}
	var bad []string
	for _, root := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			s := string(b)
			if strings.Contains(s, `".local", "state"`) && !strings.Contains(s, "XDG_STATE_HOME") && notSpore[filepath.ToSlash(p)] == "" {
				bad = append(bad, p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(bad) > 0 {
		t.Errorf("state paths ignore XDG_STATE_HOME (use events.BaseDir or task.CoordinatorStateDir): %v", bad)
	}
}
