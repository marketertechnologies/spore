// Package evals is the reviewer regression suite behind `spore eval`:
// candidate detection over `.spore/<slug>/` trees, the host-side case
// store, the headless replay runner, and the rule ship gate. See
// docs/todo/fleet-evals.md.
package evals

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Case kinds.
const (
	KindMiss    = "miss"
	KindInvalid = "invalid"
)

// Case is one `case.toml`. Dir is where it was loaded from and is
// not persisted.
type Case struct {
	ID         string
	Kind       string
	Project    string
	SourceSlug string
	Reviewer   string
	Round      int
	Created    time.Time
	// Repo is the project repo the case replays against. A relative
	// path resolves against the repo that holds the cases dir, so
	// kernel cases can say "." without naming a host path.
	Repo        string
	BaseBranch  string
	BaseSHA     string
	HeadSHA     string
	Expect      string
	Source      string
	ConfirmedBy string
	ConfirmedAt time.Time
	Rule        string

	Dir string
}

// RepoRoot returns c.Repo made absolute. Kernel cases live at
// <repo>/evals/cases/<id>/, so a relative Repo resolves against the
// directory three levels above the case dir.
func (c Case) RepoRoot() string {
	if filepath.IsAbs(c.Repo) || c.Dir == "" {
		return c.Repo
	}
	return filepath.Clean(filepath.Join(c.Dir, "..", "..", "..", c.Repo))
}

func (c Case) validate() error {
	switch {
	case c.ID == "":
		return fmt.Errorf("case: empty id")
	case c.Kind != KindMiss && c.Kind != KindInvalid:
		return fmt.Errorf("case %s: kind %q: want %q or %q", c.ID, c.Kind, KindMiss, KindInvalid)
	case c.Reviewer == "":
		return fmt.Errorf("case %s: empty reviewer", c.ID)
	case c.Round < 1:
		return fmt.Errorf("case %s: round %d: want >= 1", c.ID, c.Round)
	case c.Repo == "" || c.HeadSHA == "" || c.BaseSHA == "":
		return fmt.Errorf("case %s: repo, base_sha and head_sha are required", c.ID)
	case strings.TrimSpace(c.Expect) == "":
		return fmt.Errorf("case %s: empty expect", c.ID)
	}
	return nil
}

// Marshal renders c as case.toml: flat keys, basic strings.
func (c Case) Marshal() []byte {
	var b bytes.Buffer
	str := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s = %s\n", k, strconv.QuoteToASCII(v))
		}
	}
	tm := func(k string, v time.Time) {
		if !v.IsZero() {
			str(k, v.UTC().Format(time.RFC3339))
		}
	}
	str("id", c.ID)
	str("kind", c.Kind)
	str("project", c.Project)
	str("source_slug", c.SourceSlug)
	str("reviewer", c.Reviewer)
	fmt.Fprintf(&b, "round = %d\n", c.Round)
	tm("created", c.Created)
	str("repo", c.Repo)
	str("base_branch", c.BaseBranch)
	str("base_sha", c.BaseSHA)
	str("head_sha", c.HeadSHA)
	str("expect", c.Expect)
	str("source", c.Source)
	str("confirmed_by", c.ConfirmedBy)
	tm("confirmed_at", c.ConfirmedAt)
	str("rule", c.Rule)
	return b.Bytes()
}

// ParseCase reads the subset of TOML Marshal writes.
func ParseCase(b []byte) (Case, error) {
	var c Case
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, raw, ok := strings.Cut(line, "=")
		if !ok {
			return Case{}, fmt.Errorf("line %d: want key = value", n)
		}
		k, raw = strings.TrimSpace(k), strings.TrimSpace(raw)
		if k == "round" {
			r, err := strconv.Atoi(raw)
			if err != nil {
				return Case{}, fmt.Errorf("line %d: round: %w", n, err)
			}
			c.Round = r
			continue
		}
		v, err := strconv.Unquote(raw)
		if err != nil {
			return Case{}, fmt.Errorf("line %d: %s: want a quoted string", n, k)
		}
		if err := c.set(k, v); err != nil {
			return Case{}, fmt.Errorf("line %d: %w", n, err)
		}
	}
	if err := sc.Err(); err != nil {
		return Case{}, err
	}
	return c, c.validate()
}

func (c *Case) set(k, v string) error {
	var err error
	switch k {
	case "id":
		c.ID = v
	case "kind":
		c.Kind = v
	case "project":
		c.Project = v
	case "source_slug":
		c.SourceSlug = v
	case "reviewer":
		c.Reviewer = v
	case "created":
		c.Created, err = time.Parse(time.RFC3339, v)
	case "repo":
		c.Repo = v
	case "base_branch":
		c.BaseBranch = v
	case "base_sha":
		c.BaseSHA = v
	case "head_sha":
		c.HeadSHA = v
	case "expect":
		c.Expect = v
	case "source":
		c.Source = v
	case "confirmed_by":
		c.ConfirmedBy = v
	case "confirmed_at":
		c.ConfirmedAt, err = time.Parse(time.RFC3339, v)
	case "rule":
		c.Rule = v
	default:
		return fmt.Errorf("unknown key %q", k)
	}
	return err
}

// LoadCase reads <dir>/case.toml.
func LoadCase(dir string) (Case, error) {
	b, err := os.ReadFile(filepath.Join(dir, "case.toml"))
	if err != nil {
		return Case{}, err
	}
	c, err := ParseCase(b)
	if err != nil {
		return Case{}, fmt.Errorf("%s: %w", dir, err)
	}
	c.Dir = dir
	return c, nil
}

// LoadCases reads every case dir under root, sorted by id. A missing
// root holds no cases.
func LoadCases(root string) ([]Case, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Case
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		c, err := LoadCase(filepath.Join(root, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
