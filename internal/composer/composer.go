// Package composer renders agent instructions by concatenating named
// rule fragments listed in a consumer file. Pure stdlib.
//
// The rules pool has ONE source dir, no override layering. Compose
// reads only the rulesDir argument it is given: no env-var redirects,
// no ~/.config/ overlays, no per-user shadow paths. A render is a
// pure function of (rulesDir contents, consumer file, predicate map).
// If a future change wants per-user overrides, it must change this
// signature, not slip in via an environment side channel.
package composer

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Options configures Compose. Predicates gates lines that begin
// with "?<name> ": the rule is included only when Predicates[name]
// is true. Lines without a "?" prefix are unconditional.
type Options struct {
	Predicates map[string]bool
}

// Compose reads a consumer config (line-per-rule plain text, "#"
// and blank lines ignored) and returns the rendered instruction
// string built by concatenating the named rule fragments from
// rulesDir, separated by a single blank line. Returns error on
// missing rule, missing consumer, or read failure.
//
// Lines may be prefixed with "?<predicate> <id>". The rule is
// included only when opts.Predicates[predicate] is true. An unknown
// predicate (not present in the map) is treated as false, so
// predicate-gated lines are dropped by default.
func Compose(rulesDir, consumerPath string, opts Options) (string, error) {
	ids, err := readConsumer(consumerPath, opts)
	if err != nil {
		return "", err
	}

	pieces := make([]string, 0, len(ids))
	for _, id := range ids {
		frag, err := readRule(rulesDir, id)
		if err != nil {
			return "", err
		}
		pieces = append(pieces, frag)
	}

	return strings.Join(pieces, "\n\n") + "\n", nil
}

func readConsumer(path string, opts Options) ([]string, error) {
	var ids []string
	err := eachEntry(path, func(pred, id string) {
		if pred == "" || opts.Predicates[pred] {
			ids = append(ids, id)
		}
	})
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("composer: consumer %q lists no rules", path)
	}
	return ids, nil
}

// eachEntry calls fn for every rule line of a consumer list, with pred
// empty for an unconditional line. Comments and blanks are skipped.
func eachEntry(path string, fn func(pred, id string)) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("composer: open consumer %q: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "?") {
			pred, rule, ok := splitPredicate(line)
			if !ok {
				return fmt.Errorf("composer: %s: malformed predicate line %q", path, line)
			}
			fn(pred, rule)
			continue
		}
		fn("", line)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("composer: read consumer %q: %w", path, err)
	}
	return nil
}

// Targets returns the "# target: <repo-relative-path>" directives of a
// consumer list: the files its render is written to.
func Targets(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var targets []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "#") {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if v, ok := strings.CutPrefix(body, "target:"); ok {
			if target := strings.TrimSpace(v); target != "" {
				targets = append(targets, target)
			}
		}
	}
	return targets, scanner.Err()
}

func splitPredicate(line string) (pred, rule string, ok bool) {
	body := strings.TrimPrefix(line, "?")
	sp := strings.IndexAny(body, " \t")
	if sp <= 0 {
		return "", "", false
	}
	pred = strings.TrimSpace(body[:sp])
	rule = strings.TrimSpace(body[sp+1:])
	if pred == "" || rule == "" {
		return "", "", false
	}
	return pred, rule, true
}

func readRule(rulesDir, id string) (string, error) {
	if id == "" {
		return "", errors.New("composer: empty rule id")
	}
	rel := filepath.FromSlash(id) + ".md"
	full := filepath.Join(rulesDir, rel)
	b, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("composer: read rule %q (%s): %w", id, full, err)
	}
	return strings.TrimSpace(string(b)), nil
}
