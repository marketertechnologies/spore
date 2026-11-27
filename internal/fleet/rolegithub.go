package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/versality/spore/internal/ghapp"
	"github.com/versality/spore/internal/redact"
	"github.com/versality/spore/internal/secrets"
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
	DoneNotified bool `json:"done_notified,omitempty"`
	// PRDescription keys the engineer `pr` title/body last applied
	// (response file name plus content hash), so a re-tick never edits
	// the PR twice.
	PRDescription string `json:"pr_description,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	// Inflight is the event being posted, saved before the post. Still
	// set on the next tick, the post may have landed before the save
	// that records it (a crash, a kill): the mirror looks for the
	// event's marker on the PR before posting again.
	Inflight string `json:"inflight,omitempty"`
	// Reopen is set by `spore task reopen` for the current cycle; the
	// mirror then re-drafts the PR and labels its posts with the cycle.
	Reopen *reopenState `json:"reopen,omitempty"`
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

// readGitHubState loads github.json. On a decode error it still
// returns whatever decoded, which the mirror keeps using.
func readGitHubState(projectRoot, slug string) (githubState, error) {
	var st githubState
	b, err := os.ReadFile(githubStatePath(projectRoot, slug))
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

// mirrorEvent is one GitHub action derived from a file on disk. name
// keys it in githubState.Posted.
type mirrorEvent struct {
	name     string
	kind     string
	round    int
	instance task.ReviewerInstance
	// note is the operator's reason on an override event, captured
	// when the event is listed so posting it never re-reads the file.
	note string
}

const (
	eventEngineer  = "engineer"
	eventReview    = "review"
	eventEscalated = "escalated"
	eventReady     = "ready"
	eventReopened  = "reopened"
	eventOverride  = "override"
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
		if inst == task.ReviewerB && snap.AFixLanded {
			// A's escalation fix pass, answered before the operator
			// force-approved A: the phase machine counts it as one more
			// engineer round, so B's revisions start one later.
			engRound++
			if !addEng() {
				break reviews
			}
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
			if rev.Forced() {
				out = append(out, mirrorEvent{name: fmt.Sprintf("override-%s-%d", inst, r), kind: eventOverride, round: r, instance: inst, note: strings.TrimSpace(rev.OperatorOverride)})
			}
			// A forced B verdict was an escalation whose fix pass landed
			// (see ForceApproveReview); A's is counted by AFixLanded.
			if rev.Verdict == task.VerdictRequestChanges || (inst == task.ReviewerB && rev.Forced()) {
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
	// repo is origin as HOST/OWNER/REPO (GH_REPO form).
	repo     string
	redactor *redact.Redactor
	// brokered routes token-bearing calls through the secrets broker;
	// project is the name the broker knows this project by.
	brokered bool
	project  string
	// checked is the commit the engineer check gate cleared this tick
	// (see holdForCheck); gated is false when no gate applies.
	checked string
	gated   bool
	// marker is the hidden tag of the event being applied; prComment
	// and sendReview end every body with it.
	marker string
}

// mirrorGitHub publishes the role loop's new files to the task's draft
// PR (see docs/todo/github-app-pr-flow.md). Best-effort: a failure is
// logged, retried next tick, and told to the coordinator once per
// distinct error; it never fails the drive. A concurrent drive holding
// the lock makes this tick a no-op.
func mirrorGitHub(projectRoot, tasksDir, slug string, snap Snapshot) {
	statePath := githubStatePath(projectRoot, slug)
	unlock, err := lockGitHubState(statePath)
	if err != nil {
		if !errors.Is(err, errLockHeld) {
			fmt.Fprintf(os.Stderr, "role-loop github mirror %s: %v\n", slug, err)
		}
		return
	}
	defer unlock()

	m := &githubMirror{
		projectRoot: projectRoot,
		tasksDir:    tasksDir,
		slug:        slug,
		worktree:    filepath.Join(projectRoot, ".worktrees", slug),
	}
	m.state, _ = readGitHubState(projectRoot, slug)
	err = m.run(snap)
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
	if err != nil {
		return err
	}
	if r := m.state.Reopen; r != nil {
		events = append([]mirrorEvent{{name: reopenEventName(r.Cycle), kind: eventReopened, round: r.Cycle}}, events...)
	}
	if len(events) == 0 {
		return nil
	}
	pending := events[:0]
	for _, e := range events {
		if !m.state.posted(e.name) {
			pending = append(pending, e)
		}
	}
	pending, m.checked, m.gated, err = holdForCheck(m.projectRoot, m.slug, snap, pending)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	if _, err := m.connect(); err != nil {
		return err
	}
	for _, e := range pending {
		m.marker = mirrorMarker(m.state.Reopen, e.name)
		landed := false
		if m.state.Inflight == e.name {
			if landed, err = m.alreadyPosted(e); err != nil {
				return fmt.Errorf("%s: %w", e.name, err)
			}
		}
		if !landed {
			m.state.Inflight = e.name
			m.save()
			if err := m.apply(e); err != nil {
				return fmt.Errorf("%s: %w", e.name, err)
			}
		}
		m.state.Posted = append(m.state.Posted, e.name)
		m.state.Inflight = ""
		m.save()
	}
	return nil
}

// connect loads github.toml, mints the app token (or defers to the
// secrets broker) and pins gh to origin, so later git/gh calls act as
// the app. Returns the GH_REPO value (HOST/OWNER/REPO).
func (m *githubMirror) connect() (string, error) {
	var err error
	if m.cfg, err = ghapp.LoadConfig(m.projectRoot); err != nil {
		return "", err
	}
	if m.brokered = secrets.Available(); m.brokered {
		if err := m.cfg.ValidateFlow(); err != nil {
			return "", err
		}
		if m.project, err = task.ProjectName(m.projectRoot); err != nil {
			return "", err
		}
		m.env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	} else if err := m.connectToken(); err != nil {
		return "", err
	}
	remote, err := m.git("config", "--get", "remote.origin.url")
	if err != nil {
		return "", fmt.Errorf("no origin remote: %w", err)
	}
	repo, err := ghapp.GHRepo(remote)
	if err != nil {
		return "", err
	}
	m.env = append(m.env, "GH_REPO="+repo)
	m.repo = repo
	return repo, nil
}

// connectToken mints the app token in-process: the path for hosts
// without the secrets broker.
func (m *githubMirror) connectToken() error {
	if err := ghapp.CheckBrokerHost(); err != nil {
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
	return nil
}

func (m *githubMirror) apply(e mirrorEvent) error {
	switch {
	case e.kind == eventEngineer || e.kind == eventReady:
		if err := m.push(m.checked); err != nil {
			return err
		}
	case e.kind == eventReview && m.gated && m.checked != "":
		// The reviewer read the checked commit, which can be past the
		// last engineer push (a fix after a failed check); a later,
		// unchecked HEAD stays local.
		if err := m.pushForward(m.checked); err != nil {
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
		fmt.Fprintf(&b, "**Engineer (%sround %d)**\n\n", m.cyclePrefix(), e.round)
		writeEngineerResponse(&b, m.projectRoot, m.slug, resp)
		return m.prComment(b.String())
	case eventReview:
		rev, err := task.ReadReview(m.projectRoot, m.slug, e.instance, e.round)
		if err != nil {
			return err
		}
		return m.postReview(e.instance, e.round, rev)
	case eventEscalated:
		label, _ := reviewerName(m.projectRoot, m.slug, e.instance, e.round)
		return m.prComment(fmt.Sprintf("**Role loop escalated.** %s requested changes at round %d, the cap of %d rounds per reviewer, with high/medium comments. "+
			"The engineer was asked to address them in one fix pass; no re-review follows. Operator intervention is required: the PR stays draft until the operator decides.",
			label, e.round, MaxReviewerRounds))
	case eventReopened:
		return m.redraft()
	case eventOverride:
		label, _ := reviewerName(m.projectRoot, m.slug, e.instance, e.round)
		return m.prComment(fmt.Sprintf("**Operator force-approved %s (%sround %d).**\n\n%s\n\nIts comments stay on record; the loop moves on.",
			label, m.cyclePrefix(), e.round, e.note))
	case eventReady:
		m.applyPRDescription()
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

// reviewerName returns the display label and comment ID prefix of
// inst's verdict at round, read from the file's `prefix`. A missing or
// invalid file, or one without a prefix, falls back to the instance
// letter ("Reviewer A", A).
func reviewerName(projectRoot, slug string, inst task.ReviewerInstance, round int) (label, prefix string) {
	prefix = string(inst)
	if rev, err := task.ReadReview(projectRoot, slug, inst, round); err == nil {
		prefix = rev.IDPrefix(inst)
	}
	return task.ReviewerLabel(prefix), prefix
}

func reviewBody(inst task.ReviewerInstance, round int, rev task.Review) string {
	return reviewBodyExcept(inst, round, rev, nil)
}

// reviewBodyExcept is reviewBody without the comments whose index is in
// skip (those posted inline).
func reviewBodyExcept(inst task.ReviewerInstance, round int, rev task.Review, skip map[int]bool) string {
	verdict := "approve"
	switch {
	case rev.Forced():
		// Only an escalated request_changes can be forced.
		verdict = "request changes, force-approved by the operator"
	case rev.Verdict == task.VerdictRequestChanges:
		verdict = "request changes"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**%s (round %d): %s**\n\n", task.ReviewerLabel(rev.IDPrefix(inst)), round, verdict)
	if s := strings.TrimSpace(rev.Summary); s != "" {
		b.WriteString(s + "\n\n")
	}
	b.WriteString(renderReviewCommentsExcept(inst, round, rev, skip))
	return b.String()
}

// renderReviewComments lists rev's comments as markdown bullets in
// file order, each led by its stable ID (`**R1.2**`). Strict files are
// sorted by severity on disk (see task.checkSeverityOrder), so file
// order is severity order and ID order at once; an older unsorted file
// keeps its order so its IDs still match the indices engineers cited.
// Graded comments carry grade, category and where inline, the trigger
// on an indented line and a suggestion as an indented fenced block. A
// plain string renders as `ungraded` in a graded review and bare in an
// ungraded one.
func renderReviewComments(inst task.ReviewerInstance, round int, rev task.Review) string {
	return renderReviewCommentsExcept(inst, round, rev, nil)
}

func renderReviewCommentsExcept(inst task.ReviewerInstance, round int, rev task.Review, skip map[int]bool) string {
	graded := rev.Graded()
	prefix := rev.IDPrefix(inst)
	var b strings.Builder
	for i, c := range rev.Comments {
		if skip[i] {
			continue
		}
		id := task.CommentID(prefix, round, i)
		switch {
		case !graded:
			fmt.Fprintf(&b, "- **%s** %s\n", id, c.Text)
		case !c.IsGraded():
			fmt.Fprintf(&b, "- **%s** | ungraded | %s\n", id, c.Text)
		default:
			fmt.Fprintf(&b, "- **%s** | %s\n", id, gradedCommentLine(c))
		}
	}
	return b.String()
}

func gradedCommentLine(c task.ReviewComment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s | %s", c.Severity, c.Category)
	if w := strings.TrimSpace(c.Where); w != "" {
		fmt.Fprintf(&b, " | `%s`", w)
	}
	b.WriteString(" - " + commentDetail(c, "\n  ", "  "))
	return b.String()
}

// commentDetail renders a graded comment's text, reason, trigger and
// suggestion, shared by the review body list and inline comments so the
// two stay alike. triggerSep precedes the Trigger line; indent prefixes
// the suggestion's fenced block.
func commentDetail(c task.ReviewComment, triggerSep, indent string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(c.Text))
	if r := strings.TrimSpace(c.Reason); r != "" {
		fmt.Fprintf(&b, " (%s)", r)
	}
	if t := strings.TrimSpace(c.Trigger); t != "" {
		fmt.Fprintf(&b, "%sTrigger: %s", triggerSep, t)
	}
	if sug := strings.Trim(c.Suggestion, "\n"); strings.TrimSpace(sug) != "" {
		b.WriteString("\n\n" + indentLines(fencedBlock(sug), indent))
	}
	return b.String()
}

// fencedBlock wraps code in a markdown fence longer than any backtick
// run inside it, tagged `diff` when it reads as a unified diff.
func fencedBlock(code string) string {
	longest, run := 0, 0
	for _, r := range code {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	lang := ""
	for _, p := range []string{"diff --git", "--- ", "@@"} {
		if strings.HasPrefix(code, p) {
			lang = "diff"
			break
		}
	}
	return fence + lang + "\n" + code + "\n" + fence
}

func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}

// cyclePrefix labels round headings after a reopen ("cycle 2, "), so
// the restarted round numbers do not read as repeats of cycle 1.
func (m *githubMirror) cyclePrefix() string {
	if m.state.Reopen == nil {
		return ""
	}
	return fmt.Sprintf("cycle %d, ", m.state.Reopen.Cycle)
}

// redraft moves a ready PR back to draft and posts the operator's
// reopen note. A PR already in draft (reopened from escalation) only
// gets the comment.
func (m *githubMirror) redraft() error {
	pr := strconv.Itoa(m.state.PRNumber)
	out, err := m.gh("", "pr", "view", pr, "--json", "isDraft")
	if err != nil {
		return err
	}
	var view struct {
		IsDraft bool `json:"isDraft"`
	}
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		return fmt.Errorf("gh pr view %s: %w", pr, err)
	}
	if !view.IsDraft {
		if _, err := m.gh("", "pr", "ready", pr, "--undo"); err != nil {
			return err
		}
	}
	return m.prComment(reopenComment(*m.state.Reopen))
}

func (m *githubMirror) prComment(body string) error {
	_, err := m.gh(withMarker(body, m.marker), "pr", "comment", strconv.Itoa(m.state.PRNumber), "--body-file", "-")
	return err
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
	title := meta.Title
	if title == "" {
		title = m.slug
	}
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
	m.save()
	recordPROpened(m.projectRoot, m.slug, url, base)
	return nil
}

// prBody is the brief's summary paragraph plus a fixed footer. The rest
// of spec.md stays off the PR: briefs carry host paths and
// coordinator-only notes. A summary naming an absolute path falls back
// to the task title.
func (m *githubMirror) prBody(title string) string {
	spec, _ := os.ReadFile(filepath.Join(task.RoleTaskDir(m.projectRoot, m.slug), "spec.md"))
	summary := specSummary(string(spec))
	if summary == "" || redact.AbsPathRE.MatchString(summary) {
		summary = title
	}
	return summary + "\n\n" + m.prFooter()
}

// prFooter is the fixed spore paragraph every PR body ends with.
func (m *githubMirror) prFooter() string {
	return fmt.Sprintf("Dispatched by @%s. Opened by spore's role loop: reviews arrive as `Requirements review` and `Code review` comments whose items carry stable IDs (`R1.2` is requirements review, round 1, item 2; `C` items come from the code review), and the PR leaves draft when both reviewers approve. @%s gives the only approval and merges.\n",
		m.cfg.OperatorLogin, m.cfg.OperatorLogin)
}

// applyPRDescription replaces the PR title and body with the `pr` of
// the latest engineer response that carries one, body followed by the
// review outcome and the spore footer. No `pr` leaves the PR as
// opened. Best-effort: a failure is logged and told to the coordinator
// but never holds back marking the PR ready, and it is not retried.
func (m *githubMirror) applyPRDescription() {
	name, pr, err := latestPRDescription(m.projectRoot, m.slug)
	if err != nil || pr == nil {
		if err != nil {
			fmt.Fprintf(os.Stderr, "role-loop github mirror %s: pr description: %v\n", m.slug, err)
		}
		return
	}
	sum := sha256.Sum256([]byte(pr.Title + "\x00" + pr.Body))
	key := name + ":" + hex.EncodeToString(sum[:6])
	if m.state.PRDescription == key {
		return
	}
	body := strings.TrimSpace(pr.Body) + "\n\n---\n\n"
	if line := reviewOutcomeLine(m.projectRoot, m.slug); line != "" {
		body += line + "\n\n"
	}
	body += m.prFooter()
	if _, err := m.gh(body, "pr", "edit", strconv.Itoa(m.state.PRNumber), "--title", strings.TrimSpace(pr.Title), "--body-file", "-"); err != nil {
		fmt.Fprintf(os.Stderr, "role-loop github mirror %s: pr edit: %v\n", m.slug, err)
		notifyRoleLoop(m.projectRoot, fmt.Sprintf("%s: could not apply the engineer's PR title/body (%s); PR marked ready with the original", m.slug, mirrorErrorKey(err)))
		return
	}
	m.state.PRDescription = key
	m.save()
}

// latestPRDescription returns the `pr` of the newest valid engineer
// response that carries one, with that response's file name.
func latestPRDescription(projectRoot, slug string) (string, *task.PRDescription, error) {
	rounds, err := task.ListEngineerRounds(projectRoot, slug)
	if err != nil {
		return "", nil, err
	}
	for i := len(rounds) - 1; i >= 0; i-- {
		resp, err := task.ReadEngineerResponse(projectRoot, slug, rounds[i])
		if errors.Is(err, task.ErrInvalidRoleFile) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		if resp.PR != nil {
			return filepath.Base(task.EngineerResponsePath(projectRoot, slug, rounds[i])), resp.PR, nil
		}
	}
	return "", nil, nil
}

// reviewOutcomeLine summarises each reviewer's final verdict, e.g.
// "Requirements review: approved at round 2. Code review: approved at
// round 1."
func reviewOutcomeLine(projectRoot, slug string) string {
	var parts []string
	for _, inst := range []task.ReviewerInstance{task.ReviewerA, task.ReviewerB} {
		rounds, err := task.ListReviewRounds(projectRoot, slug, inst)
		if err != nil || len(rounds) == 0 {
			continue
		}
		last := rounds[len(rounds)-1]
		rev, err := task.ReadReview(projectRoot, slug, inst, last)
		if err != nil {
			continue
		}
		verdict := "approved"
		switch {
		case rev.Forced():
			verdict = "force-approved by the operator"
		case rev.Verdict == task.VerdictRequestChanges:
			verdict = "requested changes"
		}
		parts = append(parts, fmt.Sprintf("%s: %s at round %d.", task.ReviewerLabel(rev.IDPrefix(inst)), verdict, last))
	}
	return strings.Join(parts, " ")
}

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
