package composer

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WriteResult lists the repo-relative paths a Write touched.
type WriteResult struct {
	Written []string
	Removed []string
}

// Write renders r's files under root, skipping files whose content is
// already current, then deletes stale generated rule files: every
// marker-bearing `.md` under RuleDir that r no longer produces.
// Hand-written rule files and symlinks are never removed.
func Write(root string, r *Rendered) (WriteResult, error) {
	var res WriteResult
	files, err := r.Files()
	if err != nil {
		return res, err
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		full := filepath.Join(root, filepath.FromSlash(p))
		if cur, err := os.ReadFile(full); err == nil && string(cur) == files[p] {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return res, err
		}
		if err := os.WriteFile(full, []byte(files[p]), 0o644); err != nil {
			return res, err
		}
		res.Written = append(res.Written, p)
	}

	stale, err := StaleRuleFiles(root, files)
	if err != nil {
		return res, err
	}
	for _, p := range stale {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(p))); err != nil {
			return res, err
		}
		res.Removed = append(res.Removed, p)
	}
	if len(stale) > 0 {
		pruneEmptyDirs(filepath.Join(root, filepath.FromSlash(RuleDir)))
	}
	return res, nil
}

// StaleRuleFiles returns the repo-relative paths of generated rule
// files under root's RuleDir that are not keys of keep.
func StaleRuleFiles(root string, keep map[string]string) ([]string, error) {
	base := filepath.Join(root, filepath.FromSlash(RuleDir))
	var stale []string
	err := filepath.WalkDir(base, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && full == base {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() || d.Type()&fs.ModeSymlink != 0 || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, ok := keep[rel]; ok {
			return nil
		}
		b, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		if IsGenerated(string(b)) {
			stale = append(stale, rel)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return stale, nil
}

func pruneEmptyDirs(base string) {
	var dirs []string
	_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && p != base {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i])
	}
}
