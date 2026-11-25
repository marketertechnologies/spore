package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/versality/spore/internal/task"
)

// Warnings receives non-fatal notices, such as a stale flag.
var Warnings io.Writer = os.Stderr

// Store is one project's eval state: host-side cases, flags and
// dismissals under <state>/evals/, plus the kernel cases a repo may
// carry under evals/cases/.
type Store struct {
	ProjectRoot string
	State       string
}

// OpenStore resolves projectRoot (a worktree hops to its main repo)
// and its host state dir.
func OpenStore(projectRoot string) (Store, error) {
	root, err := task.MainRepoRoot(projectRoot)
	if err != nil {
		return Store{}, err
	}
	state, err := task.StateDirForProject(root)
	if err != nil {
		return Store{}, err
	}
	return Store{ProjectRoot: root, State: state}, nil
}

func (s Store) evalsDir() string { return filepath.Join(s.State, "evals") }

// CasesDir holds host-side cases; never inside the project tree.
func (s Store) CasesDir() string { return filepath.Join(s.evalsDir(), "cases") }

// KernelCasesDir holds cases checked into the repo. Only spore's own
// runs belong here: they contain no consumer code.
func (s Store) KernelCasesDir() string { return filepath.Join(s.ProjectRoot, "evals", "cases") }

// RunsDir holds one directory per `spore eval run`.
func (s Store) RunsDir() string { return filepath.Join(s.evalsDir(), "runs") }

func (s Store) dismissedPath(id string) string {
	return filepath.Join(s.evalsDir(), "dismissed", id)
}

func (s Store) flagsDir() string { return filepath.Join(s.evalsDir(), "flags") }

// Cases returns host-side plus kernel cases.
func (s Store) Cases() ([]Case, error) {
	host, err := LoadCases(s.CasesDir())
	if err != nil {
		return nil, err
	}
	kernel, err := LoadCases(s.KernelCasesDir())
	if err != nil {
		return nil, err
	}
	return append(host, kernel...), nil
}

func (s Store) closed(id string) (bool, error) {
	for _, p := range []string{
		filepath.Join(s.CasesDir(), id),
		filepath.Join(s.KernelCasesDir(), id),
		s.dismissedPath(id),
	} {
		_, err := os.Stat(p)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

// validID keeps an id usable as one path component.
func validID(id string) error {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("invalid candidate id %q", id)
	}
	return nil
}

// Dismiss records a no for id so it stops showing.
func (s Store) Dismiss(id string) error {
	if _, err := s.Candidate(id); err != nil {
		return err
	}
	p := s.dismissedPath(id)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
}

// Flag is the operator marking one reviewer comment as a non-issue.
type Flag struct {
	Slug     string `json:"slug"`
	Reviewer string `json:"reviewer"`
	Round    int    `json:"round"`
	// Comment is the 1-based index into the verdict's comments.
	Comment int       `json:"comment"`
	Text    string    `json:"text"`
	Why     string    `json:"why,omitempty"`
	Created time.Time `json:"created"`
}

func (f Flag) id() string {
	return fmt.Sprintf("%s.invalid.%s%d.op.c%d", f.Slug, f.Reviewer, f.Round, f.Comment)
}

// AddFlag records a flag on comment ref of slot's round verdict. ref
// is a 1-based comment index, or text matching exactly one comment.
func (s Store) AddFlag(slug string, slot task.ReviewerInstance, round int, ref, why string) (string, error) {
	if err := validID(slug); err != nil {
		return "", err
	}
	v, err := findVerdict(s.ProjectRoot, slug, slot, round)
	if err != nil {
		return "", err
	}
	idx, err := commentIndex(v.review.Comments, ref)
	if err != nil {
		return "", err
	}
	f := Flag{Slug: slug, Reviewer: string(slot), Round: round, Comment: idx, Text: v.review.Comments[idx-1], Why: why, Created: time.Now().UTC()}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.flagsDir(), 0o755); err != nil {
		return "", err
	}
	return f.id(), os.WriteFile(filepath.Join(s.flagsDir(), f.id()+".json"), append(b, '\n'), 0o644)
}

func commentIndex(comments []string, ref string) (int, error) {
	if n, err := strconv.Atoi(ref); err == nil {
		if n < 1 || n > len(comments) {
			return 0, fmt.Errorf("comment %d out of range (verdict has %d)", n, len(comments))
		}
		return n, nil
	}
	hit := 0
	for i, c := range comments {
		if strings.Contains(c, ref) {
			if hit != 0 {
				return 0, fmt.Errorf("comment ref %q matches more than one comment; use its index", ref)
			}
			hit = i + 1
		}
	}
	if hit == 0 {
		return 0, fmt.Errorf("comment ref %q matches no comment", ref)
	}
	return hit, nil
}

func (s Store) flagCandidates() ([]Candidate, error) {
	entries, err := os.ReadDir(s.flagsDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.flagsDir(), e.Name()))
		if err != nil {
			return nil, err
		}
		var f Flag
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("flag %s: %w", e.Name(), err)
		}
		slot := task.ReviewerInstance(f.Reviewer)
		v, err := findVerdict(s.ProjectRoot, f.Slug, slot, f.Round)
		if err != nil {
			// A flag whose verdict is gone must not brick every
			// other candidate; it simply stops showing.
			fmt.Fprintf(Warnings, "spore eval: skipping flag %s: %v\n", e.Name(), err)
			continue
		}
		out = append(out, Candidate{
			ID:       f.id(),
			Kind:     KindInvalid,
			Slug:     f.Slug,
			Reviewer: slot,
			Round:    f.Round,
			Source:   SourceOperator,
			Evidence: []string{f.Text},
			Why:      f.Why,
			Trigger:  v.trigger,
		})
	}
	return out, nil
}

// ConfirmOptions tunes Confirm. Zero values use the detected or
// drafted defaults.
type ConfirmOptions struct {
	Expect      string
	HeadSHA     string
	BaseBranch  string
	ConfirmedBy string
	// Kernel writes the case under the repo's evals/cases/ with a
	// relative repo, for spore's own runs only.
	Kernel bool
}

// Confirm turns open candidate id into a case dir and returns it.
func (s Store) Confirm(ctx context.Context, id string, opt ConfirmOptions) (Case, error) {
	cand, err := s.Candidate(id)
	if err != nil {
		return Case{}, err
	}
	if err := validID(id); err != nil {
		return Case{}, err
	}
	head := opt.HeadSHA
	if head == "" {
		if head, err = reviewedSHA(ctx, s.ProjectRoot, cand.Slug, cand.Reviewer, cand.Round); err != nil {
			return Case{}, err
		}
	}
	if head, err = resolveRef(ctx, s.ProjectRoot, head); err != nil {
		return Case{}, err
	}
	branch := opt.BaseBranch
	if branch == "" {
		if branch, err = baseBranch(ctx, s.ProjectRoot, cand.Slug); err != nil {
			return Case{}, err
		}
	}
	baseRef, err := resolveRef(ctx, s.ProjectRoot, branch)
	if err != nil {
		return Case{}, err
	}
	base, err := git(ctx, s.ProjectRoot, "merge-base", baseRef, head)
	if err != nil {
		return Case{}, err
	}
	project, err := task.ProjectName(s.ProjectRoot)
	if err != nil {
		return Case{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	c := Case{
		ID: id, Kind: cand.Kind, Project: project, SourceSlug: cand.Slug,
		Reviewer: string(cand.Reviewer), Round: cand.Round, Created: now,
		Repo: s.ProjectRoot, BaseBranch: branch, BaseSHA: base, HeadSHA: head,
		Expect: opt.Expect, Source: cand.Source,
		ConfirmedBy: opt.ConfirmedBy, ConfirmedAt: now,
	}
	if c.Expect == "" {
		c.Expect = cand.DraftExpect()
	}
	root := s.CasesDir()
	if opt.Kernel {
		if err := s.requireKernelRepo(); err != nil {
			return Case{}, err
		}
		root, c.Repo = s.KernelCasesDir(), "."
	}
	c.Dir = filepath.Join(root, id)
	if err := c.validate(); err != nil {
		return Case{}, err
	}
	if err := os.MkdirAll(filepath.Dir(c.Dir), 0o755); err != nil {
		return Case{}, err
	}
	// Mkdir, not MkdirAll: a racing confirm of the same id fails here
	// and must not remove the dir the other one is writing.
	if err := os.Mkdir(c.Dir, 0o755); err != nil {
		return Case{}, err
	}
	if err := s.writeCaseDir(ctx, c, cand); err != nil {
		_ = os.RemoveAll(c.Dir)
		return Case{}, err
	}
	return c, nil
}

// kernelModule is the go.mod module line of the spore repo, the only
// repo whose cases may be checked in.
const kernelModule = "module github.com/versality/spore"

// requireKernelRepo refuses a kernel case outside the spore repo: a
// case carries the project's code and spec, and spore never writes a
// consumer repo.
func (s Store) requireKernelRepo() error {
	b, err := os.ReadFile(filepath.Join(s.ProjectRoot, "go.mod"))
	first, _, _ := strings.Cut(string(b), "\n")
	if err != nil || strings.TrimSpace(first) != kernelModule {
		return fmt.Errorf("--kernel: %s is not the spore repo; kernel cases hold only spore's own runs", s.ProjectRoot)
	}
	return nil
}

func (s Store) writeCaseDir(ctx context.Context, c Case, cand Candidate) error {
	src := task.RoleTaskDir(s.ProjectRoot, cand.Slug)
	if err := copyFile(filepath.Join(src, "spec.md"), filepath.Join(c.Dir, "spec.md")); err != nil {
		return err
	}
	for r := 1; r <= cand.Trigger; r++ {
		p := task.EngineerResponsePath(s.ProjectRoot, cand.Slug, r)
		if err := copyFile(p, filepath.Join(c.Dir, "responses", filepath.Base(p))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	// The slot's own earlier verdicts: a round 2+ reviewer numbers
	// its file from them.
	for r := 1; r < cand.Round; r++ {
		p := task.ReviewPath(s.ProjectRoot, cand.Slug, cand.Reviewer, r)
		if err := copyFile(p, filepath.Join(c.Dir, "reviews", string(cand.Reviewer), filepath.Base(p))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := copyFile(task.ReviewPath(s.ProjectRoot, cand.Slug, cand.Reviewer, cand.Round), filepath.Join(c.Dir, "original-verdict.json")); err != nil {
		return err
	}
	if err := writeBundle(ctx, s.ProjectRoot, c.BaseSHA, c.HeadSHA, c.Dir); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(c.Dir, "case.toml"), c.Marshal(), 0o644)
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// copyTree copies the regular files under src into dst.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p == src {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		return copyFile(p, filepath.Join(dst, rel))
	})
}
