package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/versality/spore/internal/matter"
	"github.com/versality/spore/internal/task"
	"github.com/versality/spore/internal/task/frontmatter"
)

// maxBodyBullets caps the change bullets, per rules/core/pr-description.md.
const maxBodyBullets = 5

// prBase is the task's `base:` frontmatter, else origin's default branch.
func (m *githubMirror) prBase(meta frontmatter.Meta) (string, error) {
	if base := meta.Extra["base"]; base != "" {
		return base, nil
	}
	head, err := m.git("symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", fmt.Errorf("no PR base: set `base:` in tasks/%s.md or run `git remote set-head origin --auto`", m.slug)
	}
	return strings.TrimPrefix(head, "origin/"), nil
}

// prBody renders the PR description in the rules/core/pr-description.md
// shape: ticket link, the brief's summary paragraph, and the branch's
// commit subjects as change bullets. The rest of spec.md stays off the
// PR: briefs carry host paths and coordinator-only notes. A summary
// naming an absolute path falls back to the task title. Review rounds
// go to PR comments, never here.
func (m *githubMirror) prBody(meta frontmatter.Meta, base string) string {
	spec, _ := os.ReadFile(filepath.Join(task.RoleTaskDir(m.projectRoot, m.slug), "spec.md"))
	summary := specSummary(string(spec))
	if summary == "" || absPathRE.MatchString(summary) {
		summary = meta.Title
		if summary == "" {
			summary = m.slug
		}
	}
	var b strings.Builder
	if link := ticketLine(meta); link != "" {
		b.WriteString(link + "\n\n")
	}
	b.WriteString(summary + "\n")
	if bullets := m.changeBullets(base); len(bullets) > 0 {
		b.WriteString("\n")
		for _, s := range bullets {
			b.WriteString("- " + s + "\n")
		}
	}
	return b.String()
}

// refreshPRBody rewrites the body from the final branch before the PR
// leaves draft, so the bullets cover fix-round commits too.
func (m *githubMirror) refreshPRBody() error {
	meta, err := readRoleTaskMeta(m.tasksDir, m.slug)
	if err != nil {
		return err
	}
	base, err := m.prBase(meta)
	if err != nil {
		return err
	}
	_, err = m.gh(m.prBody(meta, base), "pr", "edit", strconv.Itoa(m.state.PRNumber), "--body-file", "-")
	return err
}

// ticketLine is "<Tracker>: [<ID>](<url>)" from the matter keys a
// tracker adapter stamps, or "" for a task with no ticket.
func ticketLine(meta frontmatter.Meta) string {
	id, url := meta.Extra[matter.MatterIDKey], meta.Extra[matter.MatterURLKey]
	if id == "" {
		return ""
	}
	label := "Ticket"
	if src := meta.Extra[matter.MatterKey]; src != "" {
		label = strings.ToUpper(src[:1]) + src[1:]
	}
	if url == "" {
		return label + ": " + id
	}
	return fmt.Sprintf("%s: [%s](%s)", label, id, url)
}

// changeBullets is up to maxBodyBullets non-merge commit subjects on
// the branch, oldest first. The base is tried as origin's ref first:
// a stack parent may exist only on the remote.
func (m *githubMirror) changeBullets(base string) []string {
	var out string
	var err error
	for _, ref := range []string{"origin/" + base, base} {
		out, err = m.git("log", "--no-merges", "--reverse", "--format=%s", ref+"..HEAD")
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var bullets []string
	for _, s := range strings.Split(out, "\n") {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] || absPathRE.MatchString(s) {
			continue
		}
		seen[s] = true
		bullets = append(bullets, s)
		if len(bullets) == maxBodyBullets {
			break
		}
	}
	return bullets
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
