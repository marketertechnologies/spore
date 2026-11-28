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

// urlCredRE is the password of URL userinfo, as bundler and git print
// it (`https://x:<token>@rubygems.pkg.github.com`), whatever its shape.
var urlCredRE = regexp.MustCompile(`(?i)\b(https?://[^\s/:@]*:)[^\s/@]+@`)

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
	// Cmd is the call's verb or API path as the ledger logs it,
	// redacted by the redactor that found the hit, since a caller
	// behind the broker cannot redact it itself.
	Cmd string `json:"cmd,omitempty"`
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
	s = scrubShapes(s, counts)
	if r.home != nil {
		s = replaceRE(s, r.home, ClassHomePath, "${1}"+Mark, counts)
	}
	return s
}

// scrubShapes redacts PEM private keys and token shapes only: no
// secrets.env values and no home paths, so it is safe on refs.
func scrubShapes(s string, counts map[string]int) string {
	s = replaceRE(s, pemRE, ClassPEM, Mark, counts)
	s = replaceRE(s, urlCredRE, ClassToken, "${1}"+Mark+"@", counts)
	return replaceRE(s, tokenRE, ClassToken, Mark, counts)
}

func replaceRE(s string, re *regexp.Regexp, class, repl string, counts map[string]int) string {
	n := len(re.FindAllStringIndex(s, -1))
	if n == 0 {
		return s
	}
	if counts != nil {
		counts[class] += n
	}
	return re.ReplaceAllString(s, repl)
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
		// Keys left as they are go in first; a redacted key that
		// collides with one already there (two home paths both
		// [redacted]) gets a #n suffix instead of dropping an entry.
		out := make(map[string]any, len(t))
		var redacted []string
		for k, x := range t {
			if nk := r.scrub(k, nil); nk != k {
				redacted = append(redacted, k)
				continue
			}
			out[k] = r.walk(x, counts)
		}
		sort.Strings(redacted)
		for _, k := range redacted {
			base := r.scrub(k, counts)
			nk := base
			for n := 2; ; n++ {
				if _, taken := out[nk]; !taken {
					break
				}
				nk = base + "#" + strconv.Itoa(n)
			}
			out[nk] = r.walk(t[k], counts)
		}
		return out
	}
	return v
}

// refFieldKeys are gh api field keys whose values are refs, logins,
// repo names or enums, never prose: a secrets.env value colliding with
// one must not break the call, so their values get only the token and
// PEM patterns.
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
// issue, -b is --body and -t is --title under create and edit (and an
// unknown verb), --subject under pr merge and --template elsewhere;
// -c / --comment is text under close and reopen only (pr review
// --comment is a boolean); under api, -f, -F, --field and --raw-field
// carry key=value fields (under pr, -F is --body-file).
func ghTextFlag(sub, verb, name string) string {
	prIssue := sub == "pr" || sub == "issue"
	switch name {
	case "--title", "--body":
		return name
	case "--subject":
		if sub == "pr" && verb == "merge" {
			return name
		}
	case "--comment", "-c":
		if prIssue && (verb == "close" || verb == "reopen") {
			return "--comment"
		}
	case "-b":
		if prIssue {
			return "--body"
		}
	case "-t":
		switch {
		case !prIssue:
		case sub == "pr" && verb == "merge":
			return "--subject"
		case verb == "create" || verb == "edit" || verb == "":
			return "--title"
		}
	case "-f", "-F", "--field", "--raw-field":
		if sub == "api" {
			return "--field"
		}
	}
	return ""
}

// shortValueFlags are the short flags, per gh subcommand, that take a
// value but carry no posted text. Only letters that take a value under
// every verb are listed (pr -r, -s, -m and -a are booleans under
// review or merge): treating a boolean as value-taking would hide the
// flags after it in its group.
var shortValueFlags = map[string]string{
	"api":   "HXpqt",
	"pr":    "RBHqLAlpSTFt",
	"issue": "RqLAalmpSTFt",
}

// ShortValueFlag walks a short flag group (-iF, -Fk=v, -F=k=v) as
// pflag does and returns the index in a of the first flag that takes a
// value (the rest of a after it, or the next arg, is that value), or
// -1 when the group has none.
func ShortValueFlag(sub, verb, a string) int {
	if !strings.HasPrefix(a, "-") || strings.HasPrefix(a, "--") {
		return -1
	}
	for j := 1; j < len(a) && a[j] != '='; j++ {
		c := a[j : j+1]
		if ghTextFlag(sub, verb, "-"+c) != "" || c == "R" || strings.Contains(shortValueFlags[sub], c) {
			return j
		}
	}
	return -1
}

// ghVerb is the gh verb of args (subcommand first): the first non-flag
// arg after it, skipping the value of -R / --repo.
func ghVerb(args []string) string {
	for i := 1; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--":
			return ""
		case a == "-R" || a == "--repo":
			i++
		case !strings.HasPrefix(a, "-"):
			return a
		}
	}
	return ""
}

// GHCall redacts what a gh call (args without the leading "gh")
// posts: stdin (JSON-aware after `--input -`), the values of --title
// and --body, and the keys and values of gh api fields, in every
// spelling: `--x v`, `--x=v`, `-x v`, `-xv`, `-x=v`. Other args are
// left alone. Values of ref-like keys (refFieldKeys) get only the
// token and PEM patterns, since they carry refs, logins and repo names
// that a secrets.env value could collide with. A field key is
// redacted too; a hit in it is placed at "field <key> key". The hits
// say what was redacted where (the key in Where is redacted), never
// the value.
func (r *Redactor) GHCall(stdin string, args []string) (string, []string, []Hit) {
	args = append([]string(nil), args...)
	sub, verb := "", ghVerb(args)
	if len(args) > 0 {
		sub = args[0]
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
			key := fieldKey(k)
			where := "field " + r.scrub(key, nil)
			counts := map[string]int{}
			k = r.scrub(k, counts)
			note(where+" key", counts)
			if !ok {
				return k
			}
			counts = map[string]int{}
			if refFieldKeys[key] {
				val = scrubShapes(val, counts)
			} else {
				val = r.scrub(val, counts)
			}
			note(where, counts)
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
		// does: its first value-taking flag takes the rest of the
		// group, or the next arg when the group ends with it. A
		// non-text value flag's next arg is not skipped: if the letter
		// set is wrong, that over-redacts instead of hiding a flag.
		j := ShortValueFlag(sub, verb, a)
		if j < 0 {
			continue
		}
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
	}
	counts := map[string]int{}
	if jsonInput {
		stdin = r.json(stdin, counts)
	} else {
		stdin = r.scrub(stdin, counts)
	}
	note("stdin", counts)
	cmd := r.scrub(logCmd(args), nil)
	for i := range hits {
		hits[i].Cmd = cmd
	}
	return stdin, args, hits
}

// logCmd is the gh call's verb or API path as a ledger event names it:
// a flag in second place (gh takes flags before the verb or path, and
// one may carry the secret) is dropped, as is a path's query string.
func logCmd(args []string) string {
	if len(args) < 2 || strings.HasPrefix(args[1], "-") {
		return strings.Join(args[:min(1, len(args))], " ")
	}
	path, _, _ := strings.Cut(args[1], "?")
	return args[0] + " " + path
}

// Record appends one redaction event per hit to the ledger of the
// project at projectRoot. via names the path that posted ("mirror",
// "spore-gh"); the event carries the gh call's verb as the hit's
// redacted Cmd, never its text. A hit without Cmd (from a broker older
// than Cmd) logs only the subcommand: the caller cannot redact a path.
func Record(projectRoot, slug, via string, args []string, hits []Hit) {
	for _, h := range hits {
		cmd := h.Cmd
		if cmd == "" && len(args) > 0 {
			cmd = args[0]
		}
		task.RecordEvent(projectRoot, events.Event{
			Kind: events.Redaction, Slug: slug, Detail: cmd + " " + h.Where,
			Fields: map[string]string{"class": h.Class, "where": h.Where, "count": strconv.Itoa(h.Count), "via": via, "cmd": cmd},
		})
	}
}
