package fleet

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/versality/spore/internal/task"
)

// reviewPayload is the body of POST /repos/{owner}/{repo}/pulls/{n}/reviews.
type reviewPayload struct {
	CommitID string          `json:"commit_id,omitempty"`
	Event    string          `json:"event"`
	Body     string          `json:"body"`
	Comments []inlineComment `json:"comments,omitempty"`
}

type inlineComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
	idx  int
}

// postReview posts rev as one COMMENT review anchored to PushedSHA,
// each graded comment with a parseable `where` as an inline comment and
// the rest in the body. GitHub rejects the whole review with 422 when
// any anchor is outside the diff and posts nothing, so the retry cannot
// double-post: it keeps only the anchors the PR's file patches show in
// the diff, and when those cannot be told apart (or still fail) it
// posts everything in the body.
func (m *githubMirror) postReview(inst task.ReviewerInstance, round int, rev task.Review) error {
	anchors := reviewAnchors(inst, round, rev)
	err := m.sendReview(inst, round, rev, anchors)
	if len(anchors) == 0 || !isHTTP422(err) {
		return err
	}
	if kept, ok := m.anchorsInDiff(anchors); ok && len(kept) > 0 && len(kept) < len(anchors) {
		if err = m.sendReview(inst, round, rev, kept); !isHTTP422(err) {
			return err
		}
	}
	return m.sendReview(inst, round, rev, nil)
}

func (m *githubMirror) sendReview(inst task.ReviewerInstance, round int, rev task.Review, anchors []inlineComment) error {
	inline := make(map[int]bool, len(anchors))
	for _, a := range anchors {
		inline[a.idx] = true
	}
	body := strings.Replace(reviewBodyExcept(inst, round, rev, inline), "(round ", "("+m.cyclePrefix()+"round ", 1)
	p := reviewPayload{Event: "COMMENT", Body: body, Comments: anchors}
	// A body-only review needs no commit, so a 422 caused by the commit
	// itself (head moved outside the mirror) cannot block the fallback.
	if len(anchors) > 0 {
		p.CommitID = m.state.PushedSHA
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(p); err != nil {
		return err
	}
	host, ownerRepo := m.repoParts()
	_, err := m.gh(buf.String(), "api", fmt.Sprintf("repos/%s/pulls/%d/reviews", ownerRepo, m.state.PRNumber),
		"--hostname", host, "--method", "POST", "--input", "-")
	return err
}

func (m *githubMirror) repoParts() (host, ownerRepo string) {
	host, ownerRepo, _ = strings.Cut(m.repo, "/")
	return host, ownerRepo
}

func isHTTP422(err error) bool {
	var ce *cmdError
	return errors.As(err, &ce) && strings.Contains(ce.stderr, "HTTP 422")
}

// reviewAnchors returns an inline comment for each graded comment whose
// `where` is `path:line`, in ID order.
func reviewAnchors(inst task.ReviewerInstance, round int, rev task.Review) []inlineComment {
	if !rev.Graded() {
		return nil
	}
	prefix := rev.IDPrefix(inst)
	var out []inlineComment
	for i, c := range rev.Comments {
		if !c.IsGraded() {
			continue
		}
		file, line, ok := parseWhere(c.Where)
		if !ok {
			continue
		}
		out = append(out, inlineComment{
			Path: file, Line: line, Side: "RIGHT", idx: i,
			Body: inlineCommentBody(task.CommentID(prefix, round, i), c),
		})
	}
	return out
}

// parseWhere splits `path:line` into a repo-relative path and a
// positive line; a trailing `:col` is dropped. Ranges, bare paths and
// absolute paths do not parse.
func parseWhere(where string) (string, int, bool) {
	w := strings.Trim(strings.TrimSpace(where), "`")
	file, num, ok := cutLast(w, ":")
	if !ok {
		return "", 0, false
	}
	if f, l, ok := cutLast(file, ":"); ok && isDigits(l) {
		file, num = f, l
	}
	line, err := strconv.Atoi(num)
	file = strings.TrimPrefix(file, "./")
	if err != nil || line <= 0 || file == "" || path.IsAbs(file) || strings.ContainsAny(file, " \t") {
		return "", 0, false
	}
	return file, line, true
}

func isDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

func cutLast(s, sep string) (before, after string, ok bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

func inlineCommentBody(id string, c task.ReviewComment) string {
	return fmt.Sprintf("**%s** | %s | %s\n\n%s", id, c.Severity, c.Category, commentDetail(c, "\n\n", ""))
}

// anchorsInDiff keeps the anchors whose line is on the right side of the
// PR's file patches. ok is false when the patches cannot be read.
func (m *githubMirror) anchorsInDiff(anchors []inlineComment) ([]inlineComment, bool) {
	host, ownerRepo := m.repoParts()
	out, err := m.gh("", "api", fmt.Sprintf("repos/%s/pulls/%d/files", ownerRepo, m.state.PRNumber),
		"--hostname", host, "--paginate", "--jq", ".[] | {filename, patch}")
	if err != nil {
		return nil, false
	}
	lines := map[string]map[int]bool{}
	dec := json.NewDecoder(strings.NewReader(out))
	for {
		var f struct {
			Filename string `json:"filename"`
			Patch    string `json:"patch"`
		}
		if err := dec.Decode(&f); err == io.EOF {
			break
		} else if err != nil {
			return nil, false
		}
		lines[f.Filename] = rightSideLines(f.Patch)
	}
	var kept []inlineComment
	for _, a := range anchors {
		if lines[a.Path][a.Line] {
			kept = append(kept, a)
		}
	}
	return kept, true
}

var hunkHeaderRE = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// rightSideLines returns the new-file line numbers a unified diff patch
// shows (added and context lines), the lines GitHub accepts for
// `side: RIGHT`.
func rightSideLines(patch string) map[int]bool {
	out := map[int]bool{}
	line := 0
	for _, l := range strings.Split(patch, "\n") {
		if m := hunkHeaderRE.FindStringSubmatch(l); m != nil {
			line, _ = strconv.Atoi(m[1])
			continue
		}
		if line == 0 || l == "" {
			continue
		}
		switch l[0] {
		case ' ', '+':
			out[line] = true
			line++
		}
	}
	return out
}
