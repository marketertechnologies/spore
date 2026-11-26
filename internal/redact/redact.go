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
	"strconv"
	"strings"

	"github.com/versality/spore/internal/events"
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
	return NewFromFiles(SecretsFiles(projectRoot))
}

// NewFromFiles loads the secrets in files, skipping missing or
// unreadable ones.
func NewFromFiles(files []string) *Redactor {
	r := &Redactor{}
	for _, f := range files {
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

// ParseEnv returns every KEY=value pair a shell would source, in file
// order. Quotes around a value are stripped; an unquoted value ends at
// whitespace.
func ParseEnv(b []byte) [][2]string {
	var out [][2]string
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
		out = append(out, [2]string{k, v})
	}
	return out
}

func parseEnv(b []byte) []secret {
	var out []secret
	for _, kv := range ParseEnv(b) {
		if len(kv[1]) >= MinSecretLen {
			out = append(out, secret{kv[0], kv[1]})
		}
	}
	return out
}

// Pattern classes a redaction is counted under. A Hit names its
// class, never the matched value.
const (
	ClassSecretsEnv = "secrets_env"
	ClassToken      = "token"
	ClassPEM        = "pem"
	ClassHomePath   = "home_path"
)

var classes = []string{ClassSecretsEnv, ClassPEM, ClassToken, ClassHomePath}

// Hit counts the redactions of one pattern class in one place of a
// gh call: Where is a flag (--body, --title), a field (field body) or
// stdin.
type Hit struct {
	Where string `json:"where"`
	Class string `json:"class"`
	Count int    `json:"count"`
}

// String returns s with every secret replaced by [redacted], or
// [redacted:NAME] when the value came from secrets.env.
func (r *Redactor) String(s string) string {
	return r.scrub(s, nil)
}

// scrub is String, adding the matches per class to counts when it is
// non-nil.
func (r *Redactor) scrub(s string, counts map[string]int) string {
	if r == nil {
		r = &Redactor{}
	}
	add := func(class string, n int) {
		if counts != nil && n > 0 {
			counts[class] += n
		}
	}
	for _, sec := range r.secrets {
		if n := strings.Count(s, sec.value); n > 0 {
			add(ClassSecretsEnv, n)
			s = strings.ReplaceAll(s, sec.value, "[redacted:"+sec.name+"]")
		}
	}
	replace := func(re *regexp.Regexp, class, repl string) {
		if n := len(re.FindAllStringIndex(s, -1)); n > 0 {
			add(class, n)
			s = re.ReplaceAllString(s, repl)
		}
	}
	replace(pemRE, ClassPEM, Mark)
	replace(tokenRE, ClassToken, Mark)
	if r.home != nil {
		replace(r.home, ClassHomePath, "${1}"+Mark)
	}
	return s
}

// JSON redacts every string inside a JSON document. Redacting the
// encoded text instead would miss values whose JSON escaping differs
// from the raw form. Input that is not JSON is redacted as text.
func (r *Redactor) JSON(doc string) string {
	return r.json(doc, nil)
}

func (r *Redactor) json(doc string, counts map[string]int) string {
	var v any
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return r.scrub(doc, counts)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r.walk(v, counts)); err != nil {
		return r.scrub(doc, counts)
	}
	return buf.String()
}

func (r *Redactor) walk(v any, counts map[string]int) any {
	switch t := v.(type) {
	case string:
		return r.scrub(t, counts)
	case []any:
		for i := range t {
			t[i] = r.walk(t[i], counts)
		}
	case map[string]any:
		for k := range t {
			t[k] = r.walk(t[k], counts)
		}
	}
	return v
}

// refFieldKeys are gh api field keys whose values are refs, logins,
// repo names or enums, never prose: a secrets.env value colliding with
// one must not break the call.
var refFieldKeys = map[string]bool{
	"head": true, "base": true, "ref": true, "sha": true, "commit_id": true,
	"owner": true, "repo": true, "name": true, "number": true, "path": true,
	"assignees": true, "reviewers": true, "team_reviewers": true, "labels": true,
	"state": true, "event": true, "side": true, "start_side": true,
}

// fieldKey is the last key segment of a gh api field: comments[][body]
// is body, assignees[] is assignees.
func fieldKey(k string) string {
	k = strings.TrimSuffix(k, "[]")
	if strings.HasSuffix(k, "]") {
		if i := strings.LastIndex(k, "["); i >= 0 {
			return k[i+1 : len(k)-1]
		}
	}
	return k
}

// ghTextFlag maps a gh flag spelling to the canonical name of a flag
// whose value is posted text, or "" when it is not one. Under pr and
// issue, -t and -b are --title and --body (-t is --subject under pr
// merge), and -c / --comment is text under close and reopen only (pr
// review --comment is a boolean); under api, -f, -F, --field and
// --raw-field carry key=value fields (under pr, -F is --body-file).
func ghTextFlag(sub, verb, name string) string {
	switch name {
	case "--title", "--body":
		return name
	case "--subject":
		if sub == "pr" && verb == "merge" {
			return name
		}
	case "--comment", "-c":
		if (sub == "pr" || sub == "issue") && (verb == "close" || verb == "reopen") {
			return "--comment"
		}
	case "-t", "-b":
		if sub == "pr" || sub == "issue" {
			return map[string]string{"-t": "--title", "-b": "--body"}[name]
		}
	case "-f", "-F", "--field", "--raw-field":
		if sub == "api" {
			return "--field"
		}
	}
	return ""
}

// GHCall redacts what a gh call (args without the leading "gh")
// posts: stdin (JSON-aware after `--input -`), the values of --title
// and --body, and the values of gh api fields, in every spelling:
// `--x v`, `--x=v`, `-x v`, `-xv`, `-x=v`. Other args are left alone,
// as are ref-like field keys (refFieldKeys), since they carry refs,
// logins and repo names that a secrets.env value could collide with.
// The hits say what was redacted where, never the value.
func (r *Redactor) GHCall(stdin string, args []string) (string, []string, []Hit) {
	args = append([]string(nil), args...)
	sub, verb := "", ""
	if len(args) > 0 {
		sub = args[0]
	}
	if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
		verb = args[1]
	}
	var hits []Hit
	note := func(where string, counts map[string]int) {
		for _, c := range classes {
			if n := counts[c]; n > 0 {
				hits = append(hits, Hit{Where: where, Class: c, Count: n})
			}
		}
	}
	text := func(flag, v string) string {
		if flag == "--field" {
			k, val, ok := strings.Cut(v, "=")
			if !ok || refFieldKeys[fieldKey(k)] {
				return v
			}
			counts := map[string]int{}
			val = r.scrub(val, counts)
			note("field "+fieldKey(k), counts)
			return k + "=" + val
		}
		counts := map[string]int{}
		v = r.scrub(v, counts)
		note(flag, counts)
		return v
	}
	jsonInput := false
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if !strings.HasPrefix(a, "-") {
			continue
		}
		if strings.HasPrefix(a, "--") {
			name, val, inline := strings.Cut(a, "=")
			if name == "--input" {
				if inline {
					jsonInput = val == "-"
				} else if i+1 < len(args) {
					jsonInput = args[i+1] == "-"
				}
				continue
			}
			flag := ghTextFlag(sub, verb, name)
			switch {
			case flag == "":
			case inline:
				args[i] = name + "=" + text(flag, val)
			case i+1 < len(args):
				i++
				args[i] = text(flag, args[i])
			}
			continue
		}
		// A short group (-iF body=x, -iFbody=x) is scanned as pflag
		// does: the first value-taking text flag takes the rest of the
		// group, or the next arg when the group ends with it.
		for j := 1; j < len(a) && a[j] != '='; j++ {
			flag := ghTextFlag(sub, verb, "-"+a[j:j+1])
			if flag == "" {
				continue
			}
			head, rest := a[:j+1], a[j+1:]
			if strings.HasPrefix(rest, "=") {
				head, rest = head+"=", rest[1:]
			}
			switch {
			case rest != "" || strings.HasSuffix(head, "="):
				args[i] = head + text(flag, rest)
			case i+1 < len(args):
				i++
				args[i] = text(flag, args[i])
			}
			break
		}
	}
	counts := map[string]int{}
	if jsonInput {
		stdin = r.json(stdin, counts)
	} else {
		stdin = r.scrub(stdin, counts)
	}
	note("stdin", counts)
	return stdin, args, hits
}

// Record appends one redaction event per hit to the ledger of the
// project at projectRoot. via names the path that posted ("mirror",
// "spore-gh"); the event carries the gh call's verb, never its text.
// gh takes flags before the verb or api path, so a flag in second
// place (which may carry the secret) is not logged as the verb.
func Record(projectRoot, slug, via string, args []string, hits []Hit) {
	cmd := strings.Join(args[:min(2, len(args))], " ")
	if len(args) > 1 && strings.HasPrefix(args[1], "-") {
		cmd = args[0]
	}
	for _, h := range hits {
		task.RecordEvent(projectRoot, events.Event{
			Kind: events.Redaction, Slug: slug, Detail: cmd + " " + h.Where,
			Fields: map[string]string{"class": h.Class, "where": h.Where, "count": strconv.Itoa(h.Count), "via": via, "cmd": cmd},
		})
	}
}
