package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/versality/spore/internal/ghapp"
	"github.com/versality/spore/internal/task"
)

// githubFlow reports whether the draft-PR mirror owns this project's
// GitHub side. A github.toml that fails to load counts as on, so the
// mirror surfaces the error instead of silently reverting to the
// files-only loop.
func githubFlow(projectRoot string) bool {
	cfg, err := ghapp.LoadConfig(projectRoot)
	return err != nil || cfg.Enabled()
}

// githubState is .spore/<slug>/github.json: what the mirror already
// did, so each tick posts only what is missing.
type githubState struct {
	PRNumber  int      `json:"pr_number,omitempty"`
	PRURL     string   `json:"pr_url,omitempty"`
	PushedSHA string   `json:"pushed_sha,omitempty"`
	Posted    []string `json:"posted,omitempty"`
	Ready     bool     `json:"ready,omitempty"`
	// DoneNotified records the plain "<slug>: done" fallback sent while
	// the mirror was failing at PhaseDone.
	DoneNotified bool   `json:"done_notified,omitempty"`
	LastError    string `json:"last_error,omitempty"`
	// BodyStructure is the structure block last spliced into (or
	// skipped for) the PR body; a differing latest block triggers one
	// sync attempt.
	BodyStructure string `json:"body_structure,omitempty"`
}

func (s *githubState) posted(name string) bool {
	for _, p := range s.Posted {
		if p == name {
			return true
		}
	}
	return false
}

func githubStatePath(projectRoot, slug string) string {
	return filepath.Join(task.RoleTaskDir(projectRoot, slug), "github.json")
}

// mirrorEvent is one GitHub action derived from a file on disk. name
// keys it in githubState.Posted.
type mirrorEvent struct {
	name     string
	kind     string
	round    int
	instance task.ReviewerInstance
}

const (
	eventEngineer  = "engineer"
	eventReview    = "review"
	eventEscalated = "escalated"
	eventReady     = "ready"
)

// mirrorEvents lists, in loop order, every GitHub action the files on
// disk imply: engineer round 1, then each verdict (A rounds, then B
// rounds) followed by the engineer round answering it when the verdict
// asked for changes, then the terminal escalation or ready step. The
// order is recomputed from files each tick, never read back from
// GitHub.
func mirrorEvents(projectRoot, slug string, snap Snapshot) ([]mirrorEvent, error) {
	eng, err := validEngineerRounds(projectRoot, slug)
	if err != nil {
		return nil, err
	}
	if len(eng) == 0 {
		return nil, nil
	}
	var out []mirrorEvent
	engRound := 1
	addEng := func() bool {
		if engRound > len(eng) {
			return false
		}
		out = append(out, mirrorEvent{name: fmt.Sprintf("engineer-round-%d.json", engRound), kind: eventEngineer, round: engRound})
		return true
	}
	addEng()
reviews:
	for _, inst := range []task.ReviewerInstance{task.ReviewerA, task.ReviewerB} {
		rounds, err := task.ListReviewRounds(projectRoot, slug, inst)
		if err != nil {
			return nil, err
		}
		for _, r := range rounds {
			rev, err := task.ReadReview(projectRoot, slug, inst, r)
			if errors.Is(err, task.ErrInvalidRoleFile) {
				continue
			}
			if err != nil {
				return nil, err
			}
			out = append(out, mirrorEvent{name: fmt.Sprintf("%s/round-%d.json", inst, r), kind: eventReview, round: r, instance: inst})
			if rev.Verdict == task.VerdictRequestChanges {
				engRound++
				if !addEng() {
					break reviews
				}
			}
		}
	}
	switch snap.Phase {
	case PhaseEscalated:
		out = append(out, mirrorEvent{
			name: fmt.Sprintf("escalated-%s-%d", snap.CurrentReviewer, snap.ReviewerRound),
			kind: eventEscalated, round: snap.ReviewerRound, instance: snap.CurrentReviewer,
		})
	case PhaseDone:
		out = append(out, mirrorEvent{name: eventReady, kind: eventReady})
	}
	return out, nil
}

// githubMirror carries one tick's mirror pass for a task.
type githubMirror struct {
	projectRoot, tasksDir, slug string
	worktree                    string
	cfg                         ghapp.Config
	state                       githubState
	env                         []string
	// structure is the PR-safe structure block for this tick.
	structure string
}

// mirrorGitHub publishes the role loop's new files to the task's draft
// PR (see docs/todo/github-app-pr-flow.md). Best-effort: a failure is
// logged, retried next tick, and told to the coordinator once per
// distinct error; it never fails the drive. A concurrent drive holding
// the lock makes this tick a no-op.
func mirrorGitHub(projectRoot, tasksDir, slug string, snap Snapshot) {
	statePath := githubStatePath(projectRoot, slug)
	unlock, ok := lockGitHubState(statePath)
	if !ok {
		return
	}
	defer unlock()

	m := &githubMirror{
		projectRoot: projectRoot,
		tasksDir:    tasksDir,
		slug:        slug,
		worktree:    filepath.Join(projectRoot, ".worktrees", slug),
	}
	if b, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(b, &m.state)
	}
	err := m.run(snap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "role-loop github mirror %s: %v\n", slug, err)
		if key := mirrorErrorKey(err); key != m.state.LastError {
			m.state.LastError = key
			notifyRoleLoop(projectRoot, fmt.Sprintf("%s: github mirror failed, retrying each tick: %v", slug, err))
			m.save()
		}
		// The mirror owns the done notice; while it is failing the
		// coordinator still learns once that the loop itself finished.
		if snap.Phase == PhaseDone && !m.state.DoneNotified {
			m.state.DoneNotified = true
			notifyRoleLoop(projectRoot, fmt.Sprintf("%s: done", slug))
			m.save()
		}
		return
	}
	if m.state.LastError != "" {
		m.state.LastError = ""
		m.save()
	}
}

// mirrorErrorKey is err's message with any git/gh stderr cut, so a
// failure that only varies in stderr detail notifies once.
func mirrorErrorKey(err error) string {
	msg := err.Error()
	var ce *cmdError
	if errors.As(err, &ce) {
		msg = strings.TrimSuffix(msg, ": "+ce.stderr)
	}
	return msg
}

// lockGitHubState takes a non-blocking flock beside the state file so
// two drives never post the same event twice.
func lockGitHubState(statePath string) (func(), bool) {
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return nil, false
	}
	f, err := os.OpenFile(statePath+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, false
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true
}

func (m *githubMirror) save() {
	b, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return
	}
	path := githubStatePath(m.projectRoot, m.slug)
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "role-loop github mirror %s: write state: %v\n", m.slug, err)
	}
}

func (m *githubMirror) run(snap Snapshot) error {
	events, err := mirrorEvents(m.projectRoot, m.slug, snap)
	if err != nil || len(events) == 0 {
		return err
	}
	pending := events[:0]
	for _, e := range events {
		if !m.state.posted(e.name) {
			pending = append(pending, e)
		}
	}
	if m.structure, err = prStructure(m.projectRoot, m.slug); err != nil {
		return err
	}
	staleBody := m.state.PRNumber != 0 && m.structure != m.state.BodyStructure
	if len(pending) == 0 && !staleBody {
		return nil
	}
	if m.cfg, err = ghapp.LoadConfig(m.projectRoot); err != nil {
		return err
	}
	if err := m.cfg.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), mirrorCallTimeout)
	defer cancel()
	tok, err := ghapp.ProjectToken(ctx, m.projectRoot)
	if err != nil {
		return err
	}
	m.env = append(ghapp.Env(os.Environ(), tok.Token), "GIT_TERMINAL_PROMPT=0", "GH_PROMPT_DISABLED=1")
	remote, err := m.git("config", "--get", "remote.origin.url")
	if err != nil {
		return fmt.Errorf("no origin remote: %w", err)
	}
	repo, err := ghapp.GHRepo(remote)
	if err != nil {
		return err
	}
	m.env = append(m.env, "GH_REPO="+repo)
	for _, e := range pending {
		if err := m.apply(e); err != nil {
			return fmt.Errorf("%s: %w", e.name, err)
		}
		m.state.Posted = append(m.state.Posted, e.name)
		m.save()
	}
	return m.syncBody()
}

// syncBody splices the latest structure block into the live PR body,
// replacing the previous block in place so operator edits survive. It
// leaves the body alone when it lacks the mirror's footer (a PR opened
// outside the loop) or no longer carries the previous block verbatim
// (the operator edited it); either way the block is recorded so the
// next tick does not retry. A body that changes between the read and
// the edit is left for the next tick.
func (m *githubMirror) syncBody() error {
	if m.state.PRNumber == 0 || m.structure == m.state.BodyStructure {
		return nil
	}
	num := strconv.Itoa(m.state.PRNumber)
	view := func() (string, error) {
		out, err := m.runRaw("gh", "", "pr", "view", num, "--json", "body", "-q", ".body")
		return strings.ReplaceAll(strings.TrimSuffix(out, "\n"), "\r\n", "\n"), err
	}
	body, err := view()
	if err != nil {
		return err
	}
	if next, ok := spliceStructure(body, m.state.BodyStructure, m.structure); ok && next != body {
		again, err := view()
		if err != nil {
			return err
		}
		if again != body {
			return nil
		}
		if _, err := m.gh(next, "pr", "edit", num, "--body-file", "-"); err != nil {
			return err
		}
	}
	m.state.BodyStructure = m.structure
	m.save()
	return nil
}

const prFooterMarker = "Opened by spore's role loop"

// spliceStructure swaps prev for next in an LF-normalized,
// mirror-written body, working only above the footer line: the last
// copy of prev there is the mirror's (an operator quote sits higher).
// With no prev it inserts next just above the footer unless the body
// already carries it; an empty next removes prev.
func spliceStructure(body, prev, next string) (string, bool) {
	marker := strings.LastIndex(body, prFooterMarker)
	if marker < 0 {
		return "", false
	}
	foot := strings.LastIndex(body[:marker], "\n") + 1
	head, tail := body[:foot], body[foot:]
	join := func(parts ...string) string {
		var keep []string
		for _, p := range parts {
			if p = strings.Trim(p, "\n"); p != "" {
				keep = append(keep, p)
			}
		}
		if len(keep) == 0 {
			return tail
		}
		return strings.Join(keep, "\n\n") + "\n\n" + tail
	}
	if prev != "" {
		i := strings.LastIndex(head, prev)
		if i < 0 {
			return "", false
		}
		if next == "" {
			return join(head[:i], head[i+len(prev):]), true
		}
		return head[:i] + next + head[i+len(prev):] + tail, true
	}
	if next == "" || strings.Contains(head, next) {
		return body, true
	}
	return join(head, next), true
}

func (m *githubMirror) apply(e mirrorEvent) error {
	if e.kind == eventEngineer || e.kind == eventReady {
		if err := m.push(); err != nil {
			return err
		}
	}
	if err := m.ensurePR(); err != nil {
		return err
	}
	switch e.kind {
	case eventEngineer:
		if e.round == 1 {
			return nil
		}
		resp, err := task.ReadEngineerResponse(m.projectRoot, m.slug, e.round)
		if err != nil {
			return err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "**Engineer (round %d)**\n\n", e.round)
		writeEngineerResponse(&b, resp)
		return m.prComment(b.String())
	case eventReview:
		rev, err := task.ReadReview(m.projectRoot, m.slug, e.instance, e.round)
		if err != nil {
			return err
		}
		_, err = m.gh(reviewBody(e.instance, e.round, rev), "pr", "review", strconv.Itoa(m.state.PRNumber), "--comment", "--body-file", "-")
		return err
	case eventEscalated:
		return m.prComment(fmt.Sprintf("**Role loop escalated.** %s requested changes at round %d, the cap of %d rounds per reviewer. The PR stays draft until the operator decides.",
			reviewLabel(e.instance), e.round, MaxReviewerRounds))
	case eventReady:
		if _, err := m.gh("", "pr", "ready", strconv.Itoa(m.state.PRNumber)); err != nil {
			return err
		}
		m.state.Ready = true
		notifyRoleLoop(m.projectRoot, fmt.Sprintf("%s: done, PR #%d ready for operator approval: %s",
			m.slug, m.state.PRNumber, m.state.PRURL))
		return nil
	}
	return fmt.Errorf("unknown mirror event kind %q", e.kind)
}

// reviewLabel names a reviewer instance the way the PR thread does.
func reviewLabel(inst task.ReviewerInstance) string {
	if inst == task.ReviewerB {
		return "Review #2"
	}
	return "Review #1"
}

func reviewBody(inst task.ReviewerInstance, round int, rev task.Review) string {
	verdict := "approve"
	if rev.Verdict == task.VerdictRequestChanges {
		verdict = "request changes"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**%s (round %d): %s**\n\n", reviewLabel(inst), round, verdict)
	if s := strings.TrimSpace(rev.Summary); s != "" {
		b.WriteString(s + "\n\n")
	}
	for _, c := range rev.Comments {
		fmt.Fprintf(&b, "- %s\n", c)
	}
	return b.String()
}

func (m *githubMirror) prComment(body string) error {
	_, err := m.gh(body, "pr", "comment", strconv.Itoa(m.state.PRNumber), "--body-file", "-")
	return err
}

// push sends the worktree HEAD to origin as the app. A branch whose
// last pushed commit is no longer an ancestor of HEAD was rewritten
// and goes up with --force-with-lease pinned to that commit.
func (m *githubMirror) push() error {
	sha, err := m.git("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if sha == m.state.PushedSHA {
		return nil
	}
	branch, err := m.branch()
	if err != nil {
		return err
	}
	ref := "refs/heads/" + branch
	args := []string{"push", "--quiet", "origin", "HEAD:" + ref}
	if prev := m.state.PushedSHA; prev != "" {
		if _, err := m.git("merge-base", "--is-ancestor", prev, "HEAD"); err != nil {
			args = append(args, "--force-with-lease="+ref+":"+prev)
		}
	}
	if _, err := m.git(args...); err != nil {
		return err
	}
	m.state.PushedSHA = sha
	m.save()
	return nil
}

func (m *githubMirror) branch() (string, error) {
	b, err := m.git("symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("worktree HEAD is not on a branch: %w", err)
	}
	return b, nil
}

// ensurePR finds or opens the task's draft PR. An open PR for the
// branch is adopted, so a lost github.json never opens a duplicate.
func (m *githubMirror) ensurePR() error {
	if m.state.PRNumber != 0 {
		return nil
	}
	branch, err := m.branch()
	if err != nil {
		return err
	}
	out, err := m.gh("", "pr", "list", "--head", branch, "--state", "open", "--json", "number,url", "--limit", "1")
	if err != nil {
		return err
	}
	var found []struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal([]byte(out), &found); err != nil {
		return fmt.Errorf("gh pr list: %w", err)
	}
	if len(found) == 1 {
		// An adopted PR was opened outside the loop, so it lacks the
		// operator assignee that `pr create` sets below.
		if _, err := m.gh("", "pr", "edit", strconv.Itoa(found[0].Number), "--add-assignee", m.cfg.OperatorLogin); err != nil {
			return err
		}
		m.state.PRNumber, m.state.PRURL = found[0].Number, found[0].URL
		m.save()
		return nil
	}
	meta, err := readRoleTaskMeta(m.tasksDir, m.slug)
	if err != nil {
		return err
	}
	base := meta.Extra["base"]
	if base == "" {
		head, err := m.git("symbolic-ref", "--short", "refs/remotes/origin/HEAD")
		if err != nil {
			return fmt.Errorf("no PR base: set `base:` in tasks/%s.md or run `git remote set-head origin --auto`", m.slug)
		}
		base = strings.TrimPrefix(head, "origin/")
	}
	title := prTitle(meta.Title, m.slug)
	out, err = m.gh(m.prBody(title), "pr", "create", "--draft", "--base", base, "--head", branch,
		"--title", title, "--body-file", "-", "--assignee", m.cfg.OperatorLogin)
	if err != nil {
		return err
	}
	url := lastLine(out)
	n, err := strconv.Atoi(url[strings.LastIndexByte(url, '/')+1:])
	if err != nil {
		return fmt.Errorf("gh pr create: no PR number in %q", url)
	}
	m.state.PRNumber, m.state.PRURL = n, url
	m.state.BodyStructure = m.structure
	m.save()
	return nil
}

func prTitle(title, slug string) string {
	if title == "" {
		return slug
	}
	return title
}

// prBody is the brief's summary paragraph, the engineer's structure
// block, and a fixed footer. The rest of spec.md stays off the PR:
// briefs carry host paths and coordinator-only notes. A summary naming
// an absolute path falls back to the task title.
func (m *githubMirror) prBody(title string) string {
	spec, _ := os.ReadFile(filepath.Join(task.RoleTaskDir(m.projectRoot, m.slug), "spec.md"))
	summary := specSummary(string(spec))
	if summary == "" || absPathRE.MatchString(summary) {
		summary = title
	}
	if m.structure != "" {
		summary += "\n\n" + m.structure
	}
	return fmt.Sprintf("%s\n\nDispatched by @%s. "+prFooterMarker+": reviews arrive as `Review #1` / `Review #2` comments, and the PR leaves draft when both reviewers approve. @%s gives the only approval and merges.\n",
		summary, m.cfg.OperatorLogin, m.cfg.OperatorLogin)
}

// prStructure is latestStructure with the summary's absolute-path
// guard applied per line, diff markers stripped: a block naming a host
// path is dropped.
func prStructure(projectRoot, slug string) (string, error) {
	s, err := latestStructure(projectRoot, slug)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(s, "\n") {
		if absPathRE.MatchString(strings.TrimLeft(line, "+-")) {
			return "", nil
		}
	}
	return s, nil
}

var absPathRE = regexp.MustCompile("(^|[\\s(`'\"])~?/[^\\s/]+/")

// specSummary returns the spec's first prose paragraph, skipping
// headings and the **Status** line.
func specSummary(spec string) string {
	for _, para := range strings.Split(spec, "\n\n") {
		p := strings.TrimSpace(para)
		if p == "" || strings.HasPrefix(p, "#") || strings.HasPrefix(p, "**Status**") || strings.HasPrefix(p, "```") {
			continue
		}
		return p
	}
	return ""
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	return strings.TrimSpace(s[strings.LastIndexByte(s, '\n')+1:])
}

func (m *githubMirror) git(args ...string) (string, error) {
	return m.run1("git", "", args...)
}

func (m *githubMirror) gh(stdin string, args ...string) (string, error) {
	return m.run1("gh", stdin, args...)
}

// mirrorCallTimeout bounds every git/gh call and the token mint: the
// mirror runs inline in the fleet reconcile, so a hung call would stall
// every task's role loop.
var mirrorCallTimeout = 60 * time.Second

// cmdError is a failed git/gh call. key drops stderr, which often
// carries request ids, so a persistent failure notifies once.
type cmdError struct {
	key, stderr string
}

func (e *cmdError) Error() string { return e.key + ": " + e.stderr }

// run1 runs name in the worktree as the app and returns trimmed
// stdout; a failure carries stderr.
func (m *githubMirror) run1(name, stdin string, args ...string) (string, error) {
	out, err := m.runRaw(name, stdin, args...)
	return strings.TrimSpace(out), err
}

// runRaw is run1 without trimming stdout, for output whose whitespace
// is content (a PR body).
func (m *githubMirror) runRaw(name, stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mirrorCallTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	cmd.Dir = m.worktree
	cmd.Env = m.env
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("timed out after %s", mirrorCallTimeout)
		}
		return "", &cmdError{
			key:    fmt.Sprintf("%s %s: %v", name, strings.Join(args[:min(2, len(args))], " "), err),
			stderr: strings.TrimSpace(stderr.String()),
		}
	}
	return stdout.String(), nil
}
