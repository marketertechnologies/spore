package linear

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/versality/spore/internal/task"
	"github.com/versality/spore/internal/task/frontmatter"
)

// linearCommentsCursorKey is the per-task frontmatter stamp Sync writes
// after every comment-fetch pass: the createdAt of the most recent
// comment already projected, in RFC3339Nano. The next pass only emits
// comments newer than this cursor. adoptIssue seeds it with the
// adoption time so pre-existing ticket comments do not flood a freshly
// spawned rover; legacy tasks with no cursor get a one-shot seed on
// the first projection pass and skip emission for that pass.
const linearCommentsCursorKey = "linear_comments_cursor"

// linearComment is the GraphQL projection of a Linear Comment. URL is
// optional: tests built around old fixtures may omit it, and Linear's
// API returns "" rather than null for missing.
type linearComment struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"createdAt"`
	User      *struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	} `json:"user"`
}

func (c linearComment) author() string {
	if c.User == nil {
		return ""
	}
	if c.User.DisplayName != "" {
		return c.User.DisplayName
	}
	return c.User.Name
}

// commentsClock is the time source for cursor seeding. Tests override
// it via withClock to make seeding deterministic.
var commentsClock = func() time.Time { return time.Now() }

// projectComments walks the active linear-backed tasks under tasksDir,
// fetches comments newer than each task's cursor, and writes a tell
// envelope per new comment into the slug's spore inbox under
// projectRoot. The cursor advances after every successful envelope
// write (not per batch) so a mid-loop crash re-emits at most the next
// undelivered comment instead of the whole batch.
func (s *Source) projectComments(projectRoot, tasksDir string) error {
	rows, err := activeLinearTasks(tasksDir)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := s.projectCommentsForTask(projectRoot, tasksDir, r); err != nil {
			return err
		}
	}
	return nil
}

func (s *Source) projectCommentsForTask(projectRoot, tasksDir string, r linearActiveTask) error {
	if r.cursor == "" {
		// Legacy task adopted before this feature shipped: seed the
		// cursor to "now" and skip emission so the rover does not get
		// flooded with the entire historical comment thread.
		return stampCommentsCursor(tasksDir, r.slug, commentsClock())
	}
	cursor, err := time.Parse(time.RFC3339Nano, r.cursor)
	if err != nil {
		return fmt.Errorf("matter.linear: parse cursor for %s: %w", r.slug, err)
	}

	comments, err := s.listIssueComments(r.identifier)
	if err != nil {
		return fmt.Errorf("matter.linear: comments for %s: %w", r.identifier, err)
	}
	sort.Slice(comments, func(i, j int) bool { return comments[i].CreatedAt.Before(comments[j].CreatedAt) })

	inboxDir, err := task.InboxDirForProject(projectRoot, r.slug)
	if err != nil {
		return err
	}
	for _, c := range comments {
		if !c.CreatedAt.After(cursor) {
			continue
		}
		if err := writeCommentEnvelope(inboxDir, r.identifier, c); err != nil {
			return fmt.Errorf("matter.linear: project comment %s for %s: %w", c.ID, r.identifier, err)
		}
		if err := stampCommentsCursor(tasksDir, r.slug, c.CreatedAt); err != nil {
			return fmt.Errorf("matter.linear: stamp cursor for %s: %w", r.slug, err)
		}
	}
	return nil
}

type linearActiveTask struct {
	slug       string
	identifier string
	cursor     string
}

// activeLinearTasks returns rows for every non-done linear-backed task
// in tasksDir. Iteration order is slug-sorted so the projected envelope
// stream is deterministic across passes.
func activeLinearTasks(tasksDir string) ([]linearActiveTask, error) {
	var rows []linearActiveTask
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(tasksDir, e.Name()))
		if err != nil {
			return nil, err
		}
		m, _, err := frontmatter.Parse(raw)
		if err != nil {
			continue
		}
		if m.Status == "done" {
			continue
		}
		id := linearIDFromMeta(m.Extra)
		if id == "" {
			continue
		}
		rows = append(rows, linearActiveTask{slug: m.Slug, identifier: id, cursor: m.Extra[linearCommentsCursorKey]})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].slug < rows[j].slug })
	return rows, nil
}

func stampCommentsCursor(tasksDir, slug string, latest time.Time) error {
	path := filepath.Join(tasksDir, slug+".md")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	m, body, err := frontmatter.Parse(raw)
	if err != nil {
		return err
	}
	if m.Extra == nil {
		m.Extra = map[string]string{}
	}
	m.Extra[linearCommentsCursorKey] = latest.UTC().Format(time.RFC3339Nano)
	return os.WriteFile(path, frontmatter.Write(m, body), 0o644)
}

// listIssueComments fetches every comment on the issue identified by
// issueID. Linear's `issue(id:)` query accepts either the canonical UUID
// or the human identifier (e.g. "MAR-12"); the caller hands us whichever
// the task's matter_id slot carries. Pagination is intentionally absent:
// active rover tickets carry small comment threads, and a future scaling
// problem is cheaper to address by adding a `first`/cursor pager than to
// preempt one.
func (s *Source) listIssueComments(issueID string) ([]linearComment, error) {
	const q = `query IssueComments($id: String!) {
  issue(id: $id) {
    comments {
      nodes { id body url createdAt user { name displayName } }
    }
  }
}`
	var resp struct {
		Data struct {
			Issue struct {
				Comments struct {
					Nodes []linearComment `json:"nodes"`
				} `json:"comments"`
			} `json:"issue"`
		} `json:"data"`
	}
	if err := s.graphQL(q, map[string]any{"id": issueID}, &resp); err != nil {
		return nil, err
	}
	return resp.Data.Issue.Comments.Nodes, nil
}

// writeCommentEnvelope drops a {ts, source, body} tell envelope into
// inboxDir using the canonical "<unix-nanos>-<rand-hex>.json" filename
// shape. The write is atomic: write to inbox/.tmp, fsync-less rename
// into place, so the rover-side watch-inbox drainer never sees a
// partially written file.
func writeCommentEnvelope(inboxDir, identifier string, c linearComment) error {
	if err := ensureCommentInbox(inboxDir); err != nil {
		return err
	}
	env := struct {
		Ts     string `json:"ts"`
		Source string `json:"source"`
		Body   string `json:"body"`
	}{
		Ts:     c.CreatedAt.UTC().Format(time.RFC3339Nano),
		Source: "linear",
		Body:   commentBody(identifier, c),
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return atomicWriteUnique(inboxDir, raw)
}

func commentBody(identifier string, c linearComment) string {
	var b strings.Builder
	b.WriteString(identifier)
	b.WriteString(" comment")
	if a := c.author(); a != "" {
		b.WriteString(" from ")
		b.WriteString(a)
	}
	b.WriteString(":\n\n")
	b.WriteString(strings.TrimSpace(c.Body))
	if c.URL != "" {
		b.WriteString("\n\n")
		b.WriteString(c.URL)
	}
	return b.String()
}

func atomicWriteUnique(inboxDir string, raw []byte) error {
	for n := 0; n < 32; n++ {
		suffix, err := randHex(4)
		if err != nil {
			return err
		}
		name := fmt.Sprintf("%d-%s.json", time.Now().UnixNano(), suffix)
		dst := filepath.Join(inboxDir, name)
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		tmp := filepath.Join(inboxDir, ".tmp", name)
		if err := os.WriteFile(tmp, raw, 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		return nil
	}
	return fmt.Errorf("matter.linear: no free inbox filename")
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func ensureCommentInbox(dir string) error {
	for _, sub := range []string{"", ".tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	return nil
}
