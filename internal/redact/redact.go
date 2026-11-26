// Package redact scrubs secrets from text spore posts to GitHub:
// values from the layered secrets.env files, well-known token shapes,
// PEM private keys and home-directory paths.
package redact

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/versality/spore/internal/task"
)

// MinSecretLen is the shortest secrets.env value redacted. Shorter
// values ("1", "true", "prod") would shred ordinary prose.
const MinSecretLen = 8

const Mark = "[redacted]"

type secret struct{ name, value string }

// Redactor holds the secrets.env values loaded for one project. The
// zero value still redacts token shapes and home paths.
type Redactor struct {
	secrets []secret
	home    *regexp.Regexp
}

var tokenRE = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{16,}|sk-ant-[A-Za-z0-9_-]{16,})`)

// pemRE also takes a BEGIN line with no END: a truncated key is still
// key material.
var pemRE = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|\z)`)

// AbsPathRE flags text naming an absolute or ~ path; the PR body
// falls back to the task title when its summary matches.
var AbsPathRE = regexp.MustCompile("(^|[\\s(`'\"])~?/[^\\s/]+/")

const pathTail = "(?:/[^\\s\"'`<>()\\[\\]{}]*|\\b)"

// SecretsFiles returns the layered secrets.env paths in the order
// spore-with-secrets sources them: global, then per-project.
func SecretsFiles(projectRoot string) []string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		dir = filepath.Join(home, ".config")
	}
	dir = filepath.Join(dir, "spore")
	files := []string{filepath.Join(dir, "secrets.env")}
	if projectRoot != "" {
		if name, err := task.ProjectName(projectRoot); err == nil && name != "" {
			files = append(files, filepath.Join(dir, name, "secrets.env"))
		}
	}
	return files
}

// New loads the secrets layered for projectRoot. Missing or unreadable
// files are skipped: redaction never blocks a post.
func New(projectRoot string) *Redactor {
	r := &Redactor{}
	for _, f := range SecretsFiles(projectRoot) {
		if b, err := os.ReadFile(f); err == nil {
			r.secrets = append(r.secrets, parseEnv(b)...)
		}
	}
	sort.SliceStable(r.secrets, func(i, j int) bool { return len(r.secrets[i].value) > len(r.secrets[j].value) })
	home, _ := os.UserHomeDir()
	homes := `(?:home|Users)/[\w.-]+`
	if home != "" && home != "/" && !strings.HasPrefix(home, "/home/") && !strings.HasPrefix(home, "/Users/") {
		homes += "|" + regexp.QuoteMeta(strings.TrimPrefix(filepath.Clean(home), "/"))
	}
	r.home = regexp.MustCompile("(^|[^\\w./~-])/(?:" + homes + ")" + pathTail)
	return r
}

var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseEnv reads the KEY=value lines a shell would source. Quotes
// around a value are stripped; an unquoted value ends at whitespace.
func parseEnv(b []byte) []secret {
	var out []secret
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok || !envKeyRE.MatchString(k) {
			continue
		}
		if n := len(v); n >= 2 && (v[0] == '"' || v[0] == '\'') && v[n-1] == v[0] {
			v = v[1 : n-1]
		} else if i := strings.IndexAny(v, " \t"); i >= 0 {
			v = v[:i]
		}
		if len(v) >= MinSecretLen {
			out = append(out, secret{k, v})
		}
	}
	return out
}

// String returns s with every secret replaced by [redacted], or
// [redacted:NAME] when the value came from secrets.env.
func (r *Redactor) String(s string) string {
	if r == nil {
		r = &Redactor{}
	}
	for _, sec := range r.secrets {
		s = strings.ReplaceAll(s, sec.value, "[redacted:"+sec.name+"]")
	}
	s = pemRE.ReplaceAllString(s, Mark)
	s = tokenRE.ReplaceAllString(s, Mark)
	if r.home != nil {
		s = r.home.ReplaceAllString(s, "${1}"+Mark)
	}
	return s
}

// JSON redacts every string inside a JSON document. Redacting the
// encoded text instead would miss values whose JSON escaping differs
// from the raw form. Input that is not JSON is redacted as text.
func (r *Redactor) JSON(doc string) string {
	var v any
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return r.String(doc)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r.walk(v)); err != nil {
		return r.String(doc)
	}
	return buf.String()
}

func (r *Redactor) walk(v any) any {
	switch t := v.(type) {
	case string:
		return r.String(t)
	case []any:
		for i := range t {
			t[i] = r.walk(t[i])
		}
	case map[string]any:
		for k := range t {
			t[k] = r.walk(t[k])
		}
	}
	return v
}
