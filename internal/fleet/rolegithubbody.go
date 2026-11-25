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
// commit subjects as change bullets, then the engineer's structure
// block and the fixed footer syncBody splices above. The rest of
// spec.md stays off the PR: briefs carry host paths and
// coordinator-only notes. A summary naming an absolute path falls back
// to the task title. Review rounds go to PR comments, never here.
func (m *githubMirror) prBody(meta frontmatter.Meta, base string) string {
	spec, _ := os.ReadFile(filepath.Join(task.RoleTaskDir(m.projectRoot, m.slug), "spec.md"))
	summary := specSummary(string(spec))
	if summary == "" || absPathRE.MatchString(summary) {
		summary = prTitle(meta.Title, m.slug)
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
	if m.structure != "" {
		b.WriteString("\n" + m.structure + "\n")
	}
	fmt.Fprintf(&b, "\nDispatched by @%s. "+prFooterMarker+": reviews arrive as `Review #1` / `Review #2` comments, and the PR leaves draft when both reviewers approve. @%s gives the only approval and merges.\n",
		m.cfg.OperatorLogin, m.cfg.OperatorLogin)
	return b.String()
}

// refreshPRBody rewrites the body from the final branch before the PR
// leaves draft, so the bullets cover fix-round commits too. It leaves
// alone an adopted PR's body, a body someone edited since the loop
// wrote it, and any body once the task file is gone (its ticket link
// and base would be lost).
func (m *githubMirror) refreshPRBody() error {
	if m.state.Body == "" {
		return nil
	}
	meta, err := readRoleTaskMeta(m.tasksDir, m.slug)
	if err != nil || meta.Slug == "" {
		return err
	}
	current, err := m.viewBody()
	if err != nil {
		return err
	}
	if !sameBody(current, m.state.Body) {
		return nil
	}
	base, err := m.prBase(meta)
	if err != nil {
		return err
	}
	body := m.prBody(meta, base)
	if body == m.state.Body {
		return nil
	}
	if _, err := m.gh(body, "pr", "edit", strconv.Itoa(m.state.PRNumber), "--body-file", "-"); err != nil {
		return err
	}
	m.state.Body, m.state.BodyStructure = body, m.structure
	m.save()
	return nil
}

// ticketLine is "<Tracker>: [<ID>](<url>)" from the matter keys a
// tracker adapter stamps, or "" for a task with no ticket. GitHub
// issues read "Issue:", per rules/core/pr-description.md.
func ticketLine(meta frontmatter.Meta) string {
	id, url := meta.Extra[matter.MatterIDKey], meta.Extra[matter.MatterURLKey]
	if id == "" {
		return ""
	}
	label := "Ticket"
	switch src := meta.Extra[matter.MatterKey]; {
	case strings.EqualFold(src, "github"):
		label = "Issue"
	case src != "":
		label = strings.ToUpper(src[:1]) + src[1:]
	}
	if url == "" {
		return label + ": " + id
	}
	return fmt.Sprintf("%s: [%s](%s)", label, id, url)
}

// changeBullets is up to maxBodyBullets non-merge commit subjects on
// the branch, oldest first, with any "area: " prefix stripped. The base is tried as origin's ref first:
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
		s = strings.TrimSpace(areaPrefixRE.ReplaceAllString(s, ""))
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

// areaPrefixRE matches a lowercase "area: " or "a, b: " subject prefix.
var areaPrefixRE = regexp.MustCompile(`^[a-z0-9_/.-]+(, ?[a-z0-9_/.-]+)*: `)

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
