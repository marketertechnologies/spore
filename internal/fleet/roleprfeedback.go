package fleet

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// PRActivity is the slice of a PR's GitHub history that
// `spore task reopen --from-pr` reads: the loop's own ready events and
// reopen comments set the cutoff, the rest is candidate feedback.
type PRActivity struct {
	ReadyEvents []PREvent
	Reviews     []PRReview
	Threads     []PRThread
	Comments    []PRComment
}

// PRAuthor is a comment or event author; Bot covers the GitHub App
// itself and any other app account.
type PRAuthor struct {
	Login string
	Bot   bool
}

type PREvent struct {
	Actor PRAuthor
	At    time.Time
}

// PRReview is a submitted review. State is GitHub's enum
// (COMMENTED, CHANGES_REQUESTED, APPROVED, DISMISSED, PENDING).
type PRReview struct {
	Author PRAuthor
	State  string
	Body   string
	At     time.Time
}

// PRThread is one inline review thread. Line is 0 when the thread is
// outdated; OriginalLine then locates it in the diff it was left on.
type PRThread struct {
	Path         string
	Line         int
	OriginalLine int
	Resolved     bool
	Comments     []PRComment
}

type PRComment struct {
	Author   PRAuthor
	Body     string
	DiffHunk string
	At       time.Time
}

// PRFeedbackSource fetches a PR's activity; tests substitute a fake.
type PRFeedbackSource interface {
	PRActivity(pr int) (PRActivity, error)
}

// reopenCommentLead opens every reopen comment the mirror posts (see
// reopenComment); the newest one marks where the last reopen began.
const reopenCommentLead = "**Reopened by the operator (cycle "

// prFeedbackHeading finds the rendered feedback section in a reopen
// note; renderPRFeedback always appends it last.
var prFeedbackHeading = regexp.MustCompile(`(?m)^### PR #[0-9]+ feedback$`)

// reopenCommentBudget keeps the reopen comment under GitHub's 65536
// character body limit; a rejected post would stall the mirror.
const reopenCommentBudget = 60000

// prCommentNote is the reopen note as posted on the PR. The feedback
// section stays in the spec only: on the PR it would duplicate the
// comments and @-mention every author again.
func prCommentNote(note string) string {
	if loc := prFeedbackHeading.FindStringIndex(note); loc != nil {
		pointer := "The human feedback left on this PR since the last cycle went to the engineer with this note."
		if head := strings.TrimSpace(note[:loc[0]]); head != "" {
			note = head + "\n\n" + pointer
		} else {
			note = pointer
		}
	}
	if len(note) <= reopenCommentBudget {
		return note
	}
	cut := reopenCommentBudget
	for cut > 0 && !utf8.RuneStart(note[cut]) {
		cut--
	}
	return note[:cut] + "\n\n(truncated; the engineer has the full note)"
}

// hunkContextLines is how many trailing diff hunk lines an inline
// comment keeps; the commented line is the hunk's last.
const hunkContextLines = 4

// ReopenNoteFromPR returns the reopen note: note, then the PR's human
// feedback since the loop last marked the PR ready or was last
// reopened. It refuses when both are empty.
func ReopenNoteFromPR(projectRoot, slug, note string, src PRFeedbackSource) (string, error) {
	pr, err := recordedPR(projectRoot, slug)
	if err != nil {
		return "", err
	}
	act, err := src.PRActivity(pr)
	if err != nil {
		return "", fmt.Errorf("reopen %s: fetch PR #%d feedback: %w", slug, pr, err)
	}
	feedback := renderPRFeedback(pr, act)
	note = strings.TrimSpace(note)
	switch {
	case feedback == "" && note == "":
		return "", fmt.Errorf("reopen %s: PR #%d has no new human feedback since %s; pass --note to reopen anyway",
			slug, pr, cutoffLabel(feedbackCutoff(act)))
	case feedback == "":
		return note, nil
	case note == "":
		return feedback, nil
	}
	return note + "\n\n" + feedback, nil
}

func recordedPR(projectRoot, slug string) (int, error) {
	st, err := readGitHubState(projectRoot, slug)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, fmt.Errorf("reopen %s: --from-pr needs the draft-PR flow; no github.json for this task", slug)
	}
	if err != nil {
		return 0, fmt.Errorf("reopen %s: github.json: %w", slug, err)
	}
	if st.PRNumber == 0 {
		return 0, fmt.Errorf("reopen %s: github.json records no PR yet", slug)
	}
	return st.PRNumber, nil
}

// feedbackCutoff is the newest of the loop's ready events and its
// reopen comments; zero when the loop has done neither.
func feedbackCutoff(act PRActivity) time.Time {
	var at time.Time
	for _, e := range act.ReadyEvents {
		if e.Actor.Bot && e.At.After(at) {
			at = e.At
		}
	}
	for _, c := range act.Comments {
		if c.Author.Bot && strings.HasPrefix(c.Body, reopenCommentLead) && c.At.After(at) {
			at = c.At
		}
	}
	return at
}

func cutoffLabel(at time.Time) string {
	if at.IsZero() {
		return "the PR was opened"
	}
	return at.UTC().Format("2006-01-02 15:04 UTC")
}

func humanAfter(a PRAuthor, at, cutoff time.Time) bool {
	return !a.Bot && !strings.HasSuffix(a.Login, "[bot]") && at.After(cutoff)
}

type inlineItem struct {
	path, loc, hunk string
	line, thread    int
	c               PRComment
}

// renderPRFeedback formats the new human feedback as a markdown note:
// inline comments grouped by file, then review bodies and issue
// comments as general comments. Empty when there is none.
func renderPRFeedback(pr int, act PRActivity) string {
	cutoff := feedbackCutoff(act)
	var inline []inlineItem
	for ti, t := range act.Threads {
		if t.Resolved {
			continue
		}
		loc, line := threadLocation(t)
		hunk := ""
		if len(t.Comments) > 0 {
			hunk = trimHunk(t.Comments[0].DiffHunk)
		}
		for _, c := range t.Comments {
			if humanAfter(c.Author, c.At, cutoff) && strings.TrimSpace(c.Body) != "" {
				inline = append(inline, inlineItem{path: t.Path, loc: loc, hunk: hunk, line: line, thread: ti, c: c})
				hunk = ""
			}
		}
	}
	type general struct {
		kind string
		c    PRComment
	}
	var gen []general
	for _, r := range act.Reviews {
		if (r.State != "COMMENTED" && r.State != "CHANGES_REQUESTED") || strings.TrimSpace(r.Body) == "" {
			continue
		}
		if humanAfter(r.Author, r.At, cutoff) {
			kind := "review"
			if r.State == "CHANGES_REQUESTED" {
				kind = "review, changes requested"
			}
			gen = append(gen, general{kind, PRComment{Author: r.Author, Body: r.Body, At: r.At}})
		}
	}
	for _, c := range act.Comments {
		if humanAfter(c.Author, c.At, cutoff) && strings.TrimSpace(c.Body) != "" {
			gen = append(gen, general{"comment", c})
		}
	}
	if len(inline) == 0 && len(gen) == 0 {
		return ""
	}
	sort.SliceStable(inline, func(i, j int) bool {
		a, b := inline[i], inline[j]
		if a.path != b.path {
			return a.path < b.path
		}
		if a.line != b.line {
			return a.line < b.line
		}
		return a.thread < b.thread
	})
	sort.SliceStable(gen, func(i, j int) bool { return gen[i].c.At.Before(gen[j].c.At) })

	var b strings.Builder
	fmt.Fprintf(&b, "### PR #%d feedback\n\nHuman comments on the PR since %s. Address each one or say in your response why not.\n",
		pr, sinceLabel(cutoff))
	path := ""
	for _, it := range inline {
		if it.path != path {
			path = it.path
			fmt.Fprintf(&b, "\n#### `%s`\n\n", path)
		}
		fmt.Fprintf(&b, "- **%s** at `%s`:\n", authorName(it.c.Author), it.loc)
		if it.hunk != "" {
			b.WriteString("\n" + indentLines(fencedBlock(it.hunk), "  ") + "\n\n")
		}
		b.WriteString(indentLines(strings.TrimSpace(it.c.Body), "  ") + "\n")
	}
	if len(gen) > 0 {
		b.WriteString("\n#### General comments\n\n")
		for _, g := range gen {
			fmt.Fprintf(&b, "- **%s** (%s):\n%s\n", authorName(g.c.Author), g.kind, indentLines(strings.TrimSpace(g.c.Body), "  "))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func sinceLabel(cutoff time.Time) string {
	if cutoff.IsZero() {
		return "it was opened"
	}
	return "the loop last marked it ready or was reopened (" + cutoffLabel(cutoff) + ")"
}

func authorName(a PRAuthor) string {
	if a.Login == "" {
		return "ghost"
	}
	return "@" + a.Login
}

// threadLocation returns path:line for display and the line to sort
// by. An outdated thread falls back to its original line.
func threadLocation(t PRThread) (string, int) {
	switch {
	case t.Line > 0:
		return t.Path + ":" + strconv.Itoa(t.Line), t.Line
	case t.OriginalLine > 0:
		return t.Path + ":" + strconv.Itoa(t.OriginalLine) + " (outdated)", t.OriginalLine
	}
	return t.Path, 0
}

func trimHunk(h string) string {
	lines := strings.Split(strings.TrimRight(h, "\n"), "\n")
	if len(lines) > hunkContextLines {
		lines = lines[len(lines)-hunkContextLines:]
	}
	return strings.Join(lines, "\n")
}

// NewGitHubPRFeedbackSource reads PR activity with `gh api graphql`,
// run as the GitHub App the same way the draft-PR mirror runs gh.
func NewGitHubPRFeedbackSource(projectRoot, slug string) PRFeedbackSource {
	return &ghFeedbackSource{m: &githubMirror{
		projectRoot: projectRoot,
		slug:        slug,
		worktree:    filepath.Join(projectRoot, ".worktrees", slug),
	}}
}

type ghFeedbackSource struct {
	m *githubMirror
}

// prActivityQuery takes the latest 100 of each list and the latest 50
// comments per thread; older feedback predates any useful cutoff on a
// PR the loop already cycled.
const prActivityQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      timelineItems(last: 50, itemTypes: [READY_FOR_REVIEW_EVENT]) {
        nodes { ... on ReadyForReviewEvent { createdAt actor { __typename login } } }
      }
      reviews(last: 100) {
        nodes { state body submittedAt createdAt author { __typename login } }
      }
      reviewThreads(last: 100) {
        nodes {
          isResolved path line originalLine
          comments(last: 50) { nodes { body diffHunk createdAt author { __typename login } } }
        }
      }
      comments(last: 100) {
        nodes { body createdAt author { __typename login } }
      }
    }
  }
}`

func (s *ghFeedbackSource) PRActivity(pr int) (PRActivity, error) {
	repo, err := s.m.connect()
	if err != nil {
		return PRActivity{}, err
	}
	parts := strings.Split(repo, "/")
	out, err := s.m.gh("", "api", "graphql", "--hostname", parts[0],
		"-f", "query="+prActivityQuery, "-f", "owner="+parts[1], "-f", "name="+parts[2], "-F", "number="+strconv.Itoa(pr))
	if err != nil {
		return PRActivity{}, err
	}
	return parsePRActivity([]byte(out))
}

type gqlAuthor struct {
	Typename string `json:"__typename"`
	Login    string `json:"login"`
}

func (a *gqlAuthor) author() PRAuthor {
	if a == nil {
		return PRAuthor{}
	}
	return PRAuthor{Login: a.Login, Bot: a.Typename == "Bot"}
}

type gqlComment struct {
	Body      string     `json:"body"`
	DiffHunk  string     `json:"diffHunk"`
	CreatedAt time.Time  `json:"createdAt"`
	Author    *gqlAuthor `json:"author"`
}

func (c gqlComment) comment() PRComment {
	return PRComment{Author: c.Author.author(), Body: c.Body, DiffHunk: c.DiffHunk, At: c.CreatedAt}
}

func parsePRActivity(raw []byte) (PRActivity, error) {
	var resp struct {
		Data struct {
			Repository struct {
				PullRequest *struct {
					TimelineItems struct {
						Nodes []struct {
							CreatedAt time.Time  `json:"createdAt"`
							Actor     *gqlAuthor `json:"actor"`
						} `json:"nodes"`
					} `json:"timelineItems"`
					Reviews struct {
						Nodes []struct {
							State       string     `json:"state"`
							Body        string     `json:"body"`
							SubmittedAt *time.Time `json:"submittedAt"`
							CreatedAt   time.Time  `json:"createdAt"`
							Author      *gqlAuthor `json:"author"`
						} `json:"nodes"`
					} `json:"reviews"`
					ReviewThreads struct {
						Nodes []struct {
							IsResolved   bool   `json:"isResolved"`
							Path         string `json:"path"`
							Line         int    `json:"line"`
							OriginalLine int    `json:"originalLine"`
							Comments     struct {
								Nodes []gqlComment `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
					Comments struct {
						Nodes []gqlComment `json:"nodes"`
					} `json:"comments"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return PRActivity{}, fmt.Errorf("gh api graphql: %w", err)
	}
	if len(resp.Errors) > 0 {
		return PRActivity{}, fmt.Errorf("gh api graphql: %s", resp.Errors[0].Message)
	}
	pr := resp.Data.Repository.PullRequest
	if pr == nil {
		return PRActivity{}, errors.New("gh api graphql: pull request not found")
	}
	var act PRActivity
	for _, n := range pr.TimelineItems.Nodes {
		act.ReadyEvents = append(act.ReadyEvents, PREvent{Actor: n.Actor.author(), At: n.CreatedAt})
	}
	for _, n := range pr.Reviews.Nodes {
		at := n.CreatedAt
		if n.SubmittedAt != nil {
			at = *n.SubmittedAt
		}
		act.Reviews = append(act.Reviews, PRReview{Author: n.Author.author(), State: n.State, Body: n.Body, At: at})
	}
	for _, n := range pr.ReviewThreads.Nodes {
		t := PRThread{Path: n.Path, Line: n.Line, OriginalLine: n.OriginalLine, Resolved: n.IsResolved}
		for _, c := range n.Comments.Nodes {
			t.Comments = append(t.Comments, c.comment())
		}
		act.Threads = append(act.Threads, t)
	}
	for _, c := range pr.Comments.Nodes {
		act.Comments = append(act.Comments, c.comment())
	}
	return act, nil
}
