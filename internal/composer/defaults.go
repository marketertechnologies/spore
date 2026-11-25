package composer

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// DefaultCore lists the core fragments every consumer list carries.
// Bootstrap seeds new projects with them; the consumer-defaults lint
// and `spore compose --add-defaults` bring existing lists up to date.
var DefaultCore = []string{
	"core/pr-description",
}

// MissingDefaults returns the DefaultCore ids the consumer list does
// not name. A predicate-gated line counts as naming its id.
func MissingDefaults(consumerPath string) ([]string, error) {
	listed, err := listedIDs(consumerPath)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, id := range DefaultCore {
		if !listed[id] {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

// AddDefaults appends each missing DefaultCore id to the consumer list
// and copies its fragment from bundled (a pool rooted like rulesDir)
// into rulesDir when the pool lacks it. An existing fragment file is
// never overwritten. Returns the ids it appended.
func AddDefaults(rulesDir, consumerPath string, bundled fs.FS) ([]string, error) {
	missing, err := MissingDefaults(consumerPath)
	if err != nil || len(missing) == 0 {
		return nil, err
	}
	for _, id := range missing {
		if err := ensureFragment(rulesDir, id, bundled); err != nil {
			return nil, err
		}
	}
	raw, err := os.ReadFile(consumerPath)
	if err != nil {
		return nil, err
	}
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		raw = append(raw, '\n')
	}
	raw = append(raw, strings.Join(missing, "\n")+"\n"...)
	if err := os.WriteFile(consumerPath, raw, 0o644); err != nil {
		return nil, err
	}
	return missing, nil
}

func ensureFragment(rulesDir, id string, bundled fs.FS) error {
	dst := filepath.Join(rulesDir, filepath.FromSlash(id)+".md")
	if _, err := os.Stat(dst); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	body, err := fs.ReadFile(bundled, path.Clean(id)+".md")
	if err != nil {
		return fmt.Errorf("composer: bundled fragment %q: %w", id, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, body, 0o644)
}

func listedIDs(consumerPath string) (map[string]bool, error) {
	f, err := os.Open(consumerPath)
	if err != nil {
		return nil, fmt.Errorf("composer: open consumer %q: %w", consumerPath, err)
	}
	defer f.Close()
	ids := map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "?") {
			if _, rule, ok := splitPredicate(line); ok {
				ids[rule] = true
			}
			continue
		}
		ids[line] = true
	}
	return ids, scanner.Err()
}
