package composer

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// MirrorMode picks how the AGENTS.md / CLAUDE.md pair is rendered.
type MirrorMode string

const (
	// MirrorCopy writes the full root body to every target.
	MirrorCopy MirrorMode = "copy"
	// MirrorImport writes the root body to AGENTS.md and a stub that
	// imports it (`@AGENTS.md`) to each sibling CLAUDE.md.
	MirrorImport MirrorMode = "import"
)

// RuleDir is where path-scoped fragments land, relative to the
// project root. Claude Code discovers `.claude/rules/**/*.md`.
const RuleDir = ".claude/rules"

// MarkerPrefix opens the comment that tags a file as compose output.
// Stale cleanup deletes only rule files that carry it.
const MarkerPrefix = "<!-- generated from "

// DefaultTargets is the target set when a consumer has no
// `# target:` directive.
var DefaultTargets = []string{"CLAUDE.md", "AGENTS.md"}

type fragment struct {
	ID    string
	Globs []string
	Body  string
}

// RuleFile is one path-scoped fragment rendered for `.claude/rules/`.
type RuleFile struct {
	ID      string
	Path    string
	Globs   []string
	Content string
}

// Directives are the `# key: value` comment lines of a consumer file.
// Compose skips comments, so they never reach the rendered body.
type Directives struct {
	Targets []string
	Mirror  MirrorMode
}

// Rendered is a full compose result: the root body, the path-scoped
// rule files, and the consumer's directives.
type Rendered struct {
	Root   string
	Marker string
	Rules  []RuleFile
	Directives
}

// ReadDirectives parses `# target: <path>` (repeatable) and
// `# mirror: copy|import` from a consumer file. Mirror defaults to
// MirrorCopy.
func ReadDirectives(consumerPath string) (Directives, error) {
	d := Directives{Mirror: MirrorCopy}
	f, err := os.Open(consumerPath)
	if err != nil {
		return d, fmt.Errorf("composer: open consumer %q: %w", consumerPath, err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "#") {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if v, ok := strings.CutPrefix(body, "target:"); ok {
			if t := strings.TrimSpace(v); t != "" {
				d.Targets = append(d.Targets, t)
			}
			continue
		}
		if v, ok := strings.CutPrefix(body, "mirror:"); ok {
			switch m := MirrorMode(strings.TrimSpace(v)); m {
			case MirrorCopy, MirrorImport:
				d.Mirror = m
			default:
				return d, fmt.Errorf("composer: %s: mirror must be copy or import, got %q", consumerPath, m)
			}
		}
	}
	return d, scanner.Err()
}

// Render composes a consumer. Fragments whose frontmatter declares
// `paths:` become RuleFiles under RuleDir; the rest are concatenated
// into Root. A render stays a pure function of (rulesDir contents,
// consumer file, predicate map).
func Render(rulesDir, consumerPath string, opts Options) (*Rendered, error) {
	ids, err := readConsumer(consumerPath, opts)
	if err != nil {
		return nil, err
	}
	d, err := ReadDirectives(consumerPath)
	if err != nil {
		return nil, err
	}

	var rootPieces []string
	var scoped []fragment
	for _, id := range ids {
		frag, err := readFragment(rulesDir, id)
		if err != nil {
			return nil, err
		}
		if len(frag.Globs) > 0 {
			if err := checkRuleID(id); err != nil {
				return nil, err
			}
			scoped = append(scoped, frag)
			continue
		}
		rootPieces = append(rootPieces, frag.Body)
	}
	if len(rootPieces) == 0 {
		return nil, fmt.Errorf("composer: consumer %q leaves the root file empty (every fragment is path-scoped)", consumerPath)
	}

	r := &Rendered{
		Root:       strings.Join(rootPieces, "\n\n") + "\n",
		Directives: d,
	}
	r.Marker = markerLine(r.Root, consumerPath)
	seen := map[string]string{}
	for _, frag := range scoped {
		p := RuleDir + "/" + frag.ID + ".md"
		if prev, dup := seen[p]; dup {
			return nil, fmt.Errorf("composer: rule %q listed twice (also %q)", frag.ID, prev)
		}
		seen[p] = frag.ID
		r.Rules = append(r.Rules, RuleFile{
			ID:      frag.ID,
			Path:    p,
			Globs:   frag.Globs,
			Content: renderFrontmatter(frag.Globs) + "\n" + r.Marker + "\n\n" + frag.Body + "\n",
		})
	}
	return r, nil
}

func checkRuleID(id string) error {
	if path.IsAbs(id) || path.Clean(id) != id || id == ".." || strings.HasPrefix(id, "../") {
		return fmt.Errorf("composer: rule id %q is not a clean relative path", id)
	}
	return nil
}

// markerLine reuses the root body's leading `<!-- generated from ...
// -->` line (consumers ship it as their header fragment) so every
// output file names the same source. Without one it synthesizes a
// marker from the consumer file name.
func markerLine(root, consumerPath string) string {
	first, _, _ := strings.Cut(root, "\n")
	first = strings.TrimSpace(first)
	if strings.HasPrefix(first, MarkerPrefix) && strings.HasSuffix(first, "-->") {
		return first
	}
	return fmt.Sprintf("%s%s by spore compose; edit fragments, not this file. -->", MarkerPrefix, filepath.Base(consumerPath))
}

// ImportStub is the CLAUDE.md body in MirrorImport mode.
func (r *Rendered) ImportStub() string {
	return r.Marker + "\n\n@AGENTS.md\n"
}

// Files maps every repo-relative output path to its expected content:
// each target (per the mirror mode) plus the rule files. In
// MirrorImport mode a CLAUDE.md target needs an AGENTS.md target in
// the same directory.
func (r *Rendered) Files() (map[string]string, error) {
	targets := r.Targets
	if len(targets) == 0 {
		targets = DefaultTargets
	}
	set := map[string]bool{}
	for _, t := range targets {
		if err := checkOutputPath(t); err != nil {
			return nil, err
		}
		set[path.Clean(t)] = true
	}
	out := map[string]string{}
	for _, t := range targets {
		t = path.Clean(t)
		if r.Mirror == MirrorImport && path.Base(t) == "CLAUDE.md" {
			if !set[path.Join(path.Dir(t), "AGENTS.md")] {
				return nil, fmt.Errorf("composer: mirror import needs an AGENTS.md target next to %s", t)
			}
			out[t] = r.ImportStub()
			continue
		}
		out[t] = r.Root
	}
	for _, rf := range r.Rules {
		if _, clash := out[rf.Path]; clash {
			return nil, fmt.Errorf("composer: rule %q collides with target %s", rf.ID, rf.Path)
		}
		out[rf.Path] = rf.Content
	}
	return out, nil
}

func checkOutputPath(p string) error {
	if p == "" || path.IsAbs(p) || filepath.IsAbs(p) {
		return fmt.Errorf("composer: target %q must be repo-relative", p)
	}
	c := path.Clean(filepath.ToSlash(p))
	if c == ".." || strings.HasPrefix(c, "../") {
		return fmt.Errorf("composer: target %q escapes the repo", p)
	}
	return nil
}

// IsGenerated reports whether content carries the compose marker as
// its first line, or its first line after a frontmatter block.
// Hand-written rule files never match, so cleanup leaves them alone.
func IsGenerated(content string) bool {
	lines := strings.Split(content, "\n")
	i := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == frontmatterFence {
		for i = 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == frontmatterFence {
				i++
				break
			}
		}
	}
	for ; i < len(lines); i++ {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return strings.HasPrefix(t, MarkerPrefix)
		}
	}
	return false
}
