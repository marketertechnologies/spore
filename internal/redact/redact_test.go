package redact

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/versality/spore/internal/events"
)

const canary = "CANARY-vuNfvrcKGX"

// newTestRedactor layers a global and a per-project secrets.env under
// a temp XDG_CONFIG_HOME and points HOME at a temp dir.
func newTestRedactor(t *testing.T) (*Redactor, string) {
	t.Helper()
	cfg := t.TempDir()
	// Fixed and outside /home and /Users: a TempDir under /home/runner (CI)
	// would hit the generic home rule instead of the HOME-specific one.
	home := "/srv/spore-redact-test-home"
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", home)
	root := filepath.Join(t.TempDir(), "demo-proj")
	write := func(path, body string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(cfg, "spore", "secrets.env"), "# global\nAPI_KEY="+canary+"\nSHORT=abc1234\nexport SENTRY_AUTH_TOKEN='sntrys_quoted_value'\n")
	write(filepath.Join(cfg, "spore", "demo-proj", "secrets.env"), "DB_PASS=\"pass word with spaces\"\nTRAILING=unquoted-value # comment\n")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return New(root), home
}

func TestStringSecretsEnv(t *testing.T) {
	r, _ := newTestRedactor(t)
	in := "leak " + canary + "-REVSUM, abc1234, sntrys_quoted_value, pass word with spaces, unquoted-value."
	got := r.String(in)
	want := "leak [redacted:API_KEY]-REVSUM, abc1234, [redacted:SENTRY_AUTH_TOKEN], [redacted:DB_PASS], [redacted:TRAILING]."
	if got != want {
		t.Fatalf("String:\n got %q\nwant %q", got, want)
	}
}

func TestStringTokenShapes(t *testing.T) {
	r := New("")
	for _, tok := range []string{
		"ghs_" + strings.Repeat("A1", 18),
		"ghp_" + strings.Repeat("b2", 18),
		"gho_" + strings.Repeat("c3", 18),
		"ghu_" + strings.Repeat("d4", 18),
		"github_pat_11ABCDEFG0_" + strings.Repeat("x", 40),
		"sk-ant-api03-" + strings.Repeat("Zz_-", 10),
	} {
		got := r.String("token: `" + tok + "` here")
		if got != "token: `[redacted]` here" {
			t.Errorf("String(%q) = %q", tok, got)
		}
	}
	for _, keep := range []string{"the ghs_ prefix", "ghp_short", "sk-ant- keys", "nothighs_" + strings.Repeat("A", 20)} {
		if got := r.String(keep); got != keep {
			t.Errorf("String(%q) = %q, want unchanged", keep, got)
		}
	}
}

func TestStringURLCredentials(t *testing.T) {
	r := New("")
	for in, want := range map[string]string{
		"Retrying download gem from https://x:CANARY-opaque-1234@rubygems.pkg.github.com/org/ due to error": "Retrying download gem from https://x:[redacted]@rubygems.pkg.github.com/org/ due to error",
		"https://x:ghp_" + strings.Repeat("Ab1", 12) + "@rubygems.pkg.github.com/":                          "https://x:[redacted]@rubygems.pkg.github.com/",
		"git clone HTTPS://x-access-token:CANARY@github.com/o/r.git":                                        "git clone HTTPS://x-access-token:[redacted]@github.com/o/r.git",
		"https://:CANARY-empty-user@github.com/o/r":                                                         "https://:[redacted]@github.com/o/r",
	} {
		hits := map[string]int{}
		if got := r.scrub(in, hits); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
		if hits[ClassToken] != 1 {
			t.Errorf("%q: token hits = %d, want 1", in, hits[ClassToken])
		}
	}
	for _, keep := range []string{"https://github.com/o/r", "see https://example.com:8443/x", "mail user@example.com"} {
		if got := r.String(keep); got != keep {
			t.Errorf("String(%q) = %q, want unchanged", keep, got)
		}
	}
}

func TestStringPEM(t *testing.T) {
	r := New("")
	full := "key:\n-----BEGIN RSA PRIVATE KEY-----\nMIIEow\nAAAA\n-----END RSA PRIVATE KEY-----\ndone"
	if got := r.String(full); got != "key:\n[redacted]\ndone" {
		t.Errorf("full PEM: %q", got)
	}
	cut := "key:\n-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1r"
	if got := r.String(cut); got != "key:\n[redacted]" {
		t.Errorf("truncated PEM: %q", got)
	}
	pub := "-----BEGIN PUBLIC KEY-----\nMIIB\n-----END PUBLIC KEY-----"
	if got := r.String(pub); got != pub {
		t.Errorf("public key changed: %q", got)
	}
}

func TestStringHomePaths(t *testing.T) {
	r, home := newTestRedactor(t)
	for in, want := range map[string]string{
		"see " + home + "/x/secrets.env now":  "see [redacted] now",
		"(`" + home + "/a.go:12`)":            "(`[redacted]`)",
		"cwd " + home:                         "cwd [redacted]",
		"see /home/alice/proj/x.go":           "see [redacted]",
		"see /Users/bob/x":                    "see [redacted]",
		"see " + home + "x/other":             "see " + home + "x/other",
		"run /usr/bin/go and ~/.config/spore": "run /usr/bin/go and ~/.config/spore",
		"https://example.com/home/alice/x":    "https://example.com/home/alice/x",
		"internal/fleet/rolegithub.go:12":     "internal/fleet/rolegithub.go:12",
	} {
		if got := r.String(in); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJSONRedactsEscapedStrings(t *testing.T) {
	r, _ := newTestRedactor(t)
	doc, err := json.Marshal(map[string]any{
		"event":    "COMMENT",
		"body":     "line one\n-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\nsee /home/alice/x",
		"comments": []any{map[string]any{"path": "a.go", "line": 3, "body": "\"" + canary + "\""}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(r.JSON(string(doc))), &got); err != nil {
		t.Fatal(err)
	}
	if b := got["body"]; b != "line one\n[redacted]\nsee [redacted]" {
		t.Errorf("body = %q", b)
	}
	c := got["comments"].([]any)[0].(map[string]any)
	if c["body"] != "\"[redacted:API_KEY]\"" || c["path"] != "a.go" || c["line"] != float64(3) {
		t.Errorf("comment = %v", c)
	}
	if got := r.JSON("not json " + canary); got != "not json [redacted:API_KEY]" {
		t.Errorf("non-JSON fallback = %q", got)
	}
}

func TestMissingSecretsFiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := New(t.TempDir()).String("plain text"); got != "plain text" {
		t.Fatalf("got %q", got)
	}
}

func TestAbsPathRE(t *testing.T) {
	for in, want := range map[string]bool{
		"see /home/u/x":                 true,
		"key at ~/.config/spore/a.pem":  true,
		"(`/etc/nixos/flake.nix`)":      true,
		"/srv/a/b at the start":         true,
		"Make the widget spin.":         false,
		"see https://github.com/a/b":    false,
		"read and/or write the a/b key": false,
	} {
		if got := AbsPathRE.MatchString(in); got != want {
			t.Errorf("AbsPathRE(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestGHCallEveryFlagForm: the canary is redacted in every spelling of
// every flag that carries posted text, and the hit names the place.
func TestGHCallEveryFlagForm(t *testing.T) {
	r, _ := newTestRedactor(t)
	c := canary
	red := "[redacted:API_KEY]"
	for _, tc := range []struct {
		name  string
		args  []string
		want  []string
		where string
	}{
		{"body space", []string{"pr", "comment", "7", "--body", "x " + c}, []string{"pr", "comment", "7", "--body", "x " + red}, "--body"},
		{"body equals", []string{"pr", "comment", "7", "--body=x " + c}, []string{"pr", "comment", "7", "--body=x " + red}, "--body"},
		{"body short", []string{"pr", "comment", "7", "-b", c}, []string{"pr", "comment", "7", "-b", red}, "--body"},
		{"body short joined", []string{"pr", "review", "7", "-b" + c}, []string{"pr", "review", "7", "-b" + red}, "--body"},
		{"body short equals", []string{"pr", "review", "7", "-b=" + c}, []string{"pr", "review", "7", "-b=" + red}, "--body"},
		{"title space", []string{"pr", "create", "--draft", "--title", c}, []string{"pr", "create", "--draft", "--title", red}, "--title"},
		{"title equals", []string{"pr", "edit", "7", "--title=" + c}, []string{"pr", "edit", "7", "--title=" + red}, "--title"},
		{"title short", []string{"pr", "create", "-t", c}, []string{"pr", "create", "-t", red}, "--title"},
		{"issue body short", []string{"issue", "comment", "3", "-b", c}, []string{"issue", "comment", "3", "-b", red}, "--body"},
		{"api -f", []string{"api", "repos/o/r/issues/3/comments", "-f", "body=" + c}, []string{"api", "repos/o/r/issues/3/comments", "-f", "body=" + red}, "field body"},
		{"api -F", []string{"api", "repos/o/r/pulls/3", "-F", "title=" + c}, []string{"api", "repos/o/r/pulls/3", "-F", "title=" + red}, "field title"},
		{"api -f joined", []string{"api", "repos/o/r/pulls/3", "-fbody=" + c}, []string{"api", "repos/o/r/pulls/3", "-fbody=" + red}, "field body"},
		{"api --field", []string{"api", "repos/o/r/pulls/3/reviews", "--field", "body=" + c}, []string{"api", "repos/o/r/pulls/3/reviews", "--field", "body=" + red}, "field body"},
		{"api --field=", []string{"api", "repos/o/r/pulls/3/reviews", "--field=body=" + c}, []string{"api", "repos/o/r/pulls/3/reviews", "--field=body=" + red}, "field body"},
		{"api --raw-field", []string{"api", "repos/o/r/pulls/3/comments", "--raw-field", "body=" + c}, []string{"api", "repos/o/r/pulls/3/comments", "--raw-field", "body=" + red}, "field body"},
		{"api --raw-field=", []string{"api", "repos/o/r/pulls/3/comments", "--raw-field=body=" + c}, []string{"api", "repos/o/r/pulls/3/comments", "--raw-field=body=" + red}, "field body"},
		{"close comment", []string{"pr", "close", "7", "--comment", c}, []string{"pr", "close", "7", "--comment", red}, "--comment"},
		{"close comment equals", []string{"pr", "close", "7", "--comment=" + c}, []string{"pr", "close", "7", "--comment=" + red}, "--comment"},
		{"reopen comment short", []string{"pr", "reopen", "7", "-c", c}, []string{"pr", "reopen", "7", "-c", red}, "--comment"},
		{"issue close comment joined", []string{"issue", "close", "3", "-c" + c}, []string{"issue", "close", "3", "-c" + red}, "--comment"},
		{"merge subject", []string{"pr", "merge", "7", "--squash", "--subject", c}, []string{"pr", "merge", "7", "--squash", "--subject", red}, "--subject"},
		{"merge subject equals", []string{"pr", "merge", "7", "--subject=" + c}, []string{"pr", "merge", "7", "--subject=" + red}, "--subject"},
		{"api grouped space", []string{"api", "repos/o/r/issues/9/comments", "-iF", "body=" + c}, []string{"api", "repos/o/r/issues/9/comments", "-iF", "body=" + red}, "field body"},
		{"api grouped joined", []string{"api", "repos/o/r/issues/9/comments", "-iFbody=" + c}, []string{"api", "repos/o/r/issues/9/comments", "-iFbody=" + red}, "field body"},
		{"api grouped equals", []string{"api", "repos/o/r/issues/9/comments", "-iF=body=" + c}, []string{"api", "repos/o/r/issues/9/comments", "-iF=body=" + red}, "field body"},
		{"pr grouped body", []string{"pr", "comment", "7", "-eb", c}, []string{"pr", "comment", "7", "-eb", red}, "--body"},
		{"api nested key", []string{"api", "repos/o/r/pulls/3/reviews", "-f", "comments[][body]=" + c}, []string{"api", "repos/o/r/pulls/3/reviews", "-f", "comments[][body]=" + red}, "field body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, got, hits := r.GHCall("", tc.args)
			hits = noCmd(hits)
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("args:\n got %q\nwant %q", got, tc.want)
			}
			want := []Hit{{Where: tc.where, Class: ClassSecretsEnv, Count: 1}}
			if len(hits) != 1 || hits[0] != want[0] {
				t.Fatalf("hits = %+v, want %+v", hits, want)
			}
		})
	}
}

// TestGHCallLeavesRefs: refs, logins, repo names, the endpoint and
// flags that are not posted text pass through even when they equal a
// secrets.env value; under pr, -F is --body-file.
func TestGHCallLeavesRefs(t *testing.T) {
	r, _ := newTestRedactor(t)
	c := canary
	for _, args := range [][]string{
		{"pr", "list", "--head", c, "--state", "open"},
		{"pr", "create", "--draft", "--base", c, "--head=" + c, "-F", "-"},
		{"api", "graphql", "-f", "owner=" + c, "-f", "name=" + c, "-F", "number=7"},
		{"api", "repos/o/r/pulls/3/reviews", "-f", "commit_id=" + c, "-f", "event=COMMENT", "-f", "comments[][path]=" + c},
		{"api", "repos/o/r/issues/3/assignees", "-f", "assignees[]=" + c},
		{"api", "repos/o/r/pulls/3", "--jq", c, "-t", c},
		{"pr", "comment", "7", "--", "--body", c},
		{"pr", "edit", "7", "--subject", c},
		{"pr", "review", "7", "--comment", "--approve"},
		{"pr", "view", "7", "-c", c},
	} {
		_, got, hits := r.GHCall("", args)
		if strings.Join(got, "\x00") != strings.Join(args, "\x00") || len(hits) != 0 {
			t.Errorf("GHCall(%q) = %q, hits %+v; want unchanged", args, got, hits)
		}
	}
}

// TestGHCallStdin: stdin is redacted as JSON after `--input -` in
// either spelling (a JSON-escaped secret is still caught) and as text
// otherwise; each class gets its own hit.
func TestGHCallStdin(t *testing.T) {
	r, _ := newTestRedactor(t)
	escaped := "pass word with spaces"
	doc := `{"body":"pass word with spaces and ghs_` + strings.Repeat("A1", 18) + `"}`
	for _, input := range [][]string{{"--input", "-"}, {"--input=-"}} {
		args := append([]string{"api", "repos/o/r/pulls/3/reviews", "--method", "POST"}, input...)
		out, _, hits := r.GHCall(doc, args)
		hits = noCmd(hits)
		if strings.Contains(out, escaped) || strings.Contains(out, "ghs_") || !strings.Contains(out, "[redacted:DB_PASS]") {
			t.Errorf("%q: stdin = %q", input, out)
		}
		want := []Hit{{Where: "stdin", Class: ClassSecretsEnv, Count: 1}, {Where: "stdin", Class: ClassToken, Count: 1}}
		if len(hits) != 2 || hits[0] != want[0] || hits[1] != want[1] {
			t.Errorf("%q: hits = %+v, want %+v", input, hits, want)
		}
	}
	out, _, hits := r.GHCall("see "+canary+" and /home/alice/x", []string{"pr", "comment", "7", "--body-file", "-"})
	if out != "see [redacted:API_KEY] and [redacted]" {
		t.Errorf("text stdin = %q", out)
	}
	if len(hits) != 2 || hits[1].Class != ClassHomePath {
		t.Errorf("text hits = %+v", hits)
	}
}

// TestGHCallRefKeysGetShapes: a ref-like key still passes a colliding
// secrets.env value, but a token or PEM in its value is redacted.
func TestGHCallRefKeysGetShapes(t *testing.T) {
	r, _ := newTestRedactor(t)
	tok := "ghp_" + strings.Repeat("Ab1", 12)
	pem := "-----BEGIN RSA PRIVATE KEY-----\nMIIx\n-----END RSA PRIVATE KEY-----"
	for _, tc := range []struct {
		name  string
		args  []string
		want  []string
		where string
		class string
	}{
		{"labels token", []string{"api", "repos/o/r/issues/1/labels", "-f", "labels[]=" + tok}, []string{"api", "repos/o/r/issues/1/labels", "-f", "labels[]=" + Mark}, "field labels", ClassToken},
		{"graphql name token", []string{"api", "graphql", "-f", "query=q", "-f", "name=" + tok}, []string{"api", "graphql", "-f", "query=q", "-f", "name=" + Mark}, "field name", ClassToken},
		{"head pem", []string{"api", "repos/o/r/pulls", "-f", "head=" + pem}, []string{"api", "repos/o/r/pulls", "-f", "head=" + Mark}, "field head", ClassPEM},
		{"ref secrets.env kept", []string{"api", "repos/o/r/pulls", "-f", "head=" + canary}, []string{"api", "repos/o/r/pulls", "-f", "head=" + canary}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, got, hits := r.GHCall("", tc.args)
			hits = noCmd(hits)
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("args:\n got %q\nwant %q", got, tc.want)
			}
			if tc.where == "" {
				if len(hits) != 0 {
					t.Fatalf("hits = %+v, want none", hits)
				}
				return
			}
			want := Hit{Where: tc.where, Class: tc.class, Count: 1}
			if len(hits) != 1 || hits[0] != want {
				t.Fatalf("hits = %+v, want %+v", hits, want)
			}
		})
	}
}

// TestGHCallFieldKeys: a secret in a field key is redacted before gh
// sees it, and no hit's Where carries it.
func TestGHCallFieldKeys(t *testing.T) {
	r, _ := newTestRedactor(t)
	tok := "ghp_" + strings.Repeat("Ab1", 12)
	red := "[redacted:API_KEY]"
	for _, tc := range []struct {
		name string
		arg  string
		want string
		hits []Hit
	}{
		{"secrets.env key", canary + "=v", red + "=v", []Hit{{Where: "field " + red + " key", Class: ClassSecretsEnv, Count: 1}}},
		{"token key", tok + "=v", Mark + "=v", []Hit{{Where: "field " + Mark + " key", Class: ClassToken, Count: 1}}},
		{"nested key and value", "body[" + canary + "]=" + canary, "body[" + red + "]=" + red, []Hit{
			{Where: "field " + red + " key", Class: ClassSecretsEnv, Count: 1}, {Where: "field " + red, Class: ClassSecretsEnv, Count: 1},
		}},
		{"key without value", canary, red, []Hit{{Where: "field " + red + " key", Class: ClassSecretsEnv, Count: 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"api", "repos/o/r/issues/1/comments", "-f", tc.arg}
			_, got, hits := r.GHCall("", args)
			hits = noCmd(hits)
			if got[3] != tc.want {
				t.Fatalf("field = %q, want %q", got[3], tc.want)
			}
			if len(hits) != len(tc.hits) {
				t.Fatalf("hits = %+v, want %+v", hits, tc.hits)
			}
			for i, h := range hits {
				if h != tc.hits[i] || strings.Contains(h.Where, canary) || strings.Contains(h.Where, tok) {
					t.Errorf("hit %d = %+v, want %+v", i, h, tc.hits[i])
				}
			}
		})
	}
}

func noCmd(hits []Hit) []Hit {
	for i := range hits {
		hits[i].Cmd = ""
	}
	return hits
}

// TestGHCallHitCmd: the hit's Cmd is the verb or path with the query
// dropped and any secret redacted, and never a leading flag.
func TestGHCallHitCmd(t *testing.T) {
	r, _ := newTestRedactor(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"api", "repos/o/r/issues?q=" + canary, "-f", "body=" + canary}, "api repos/o/r/issues"},
		{[]string{"api", "repos/o/r/issues/" + canary + "/comments", "-f", "body=" + canary}, "api repos/o/r/issues/[redacted:API_KEY]/comments"},
		{[]string{"api", "-fbody=" + canary, "repos/o/r/issues/1/comments"}, "api"},
		{[]string{"pr", "comment", "7", "--body", canary}, "pr comment"},
	} {
		_, _, hits := r.GHCall("", tc.args)
		if len(hits) == 0 || hits[0].Cmd != tc.want {
			t.Errorf("GHCall(%q) hits = %+v, want cmd %q", tc.args, hits, tc.want)
		}
	}
}

// TestGHCallFlagParsing: the verb is found past -R, a non-text value
// flag ends a short group, and -t is --template outside create, edit
// and merge.
func TestGHCallFlagParsing(t *testing.T) {
	r, _ := newTestRedactor(t)
	c := canary
	red := "[redacted:API_KEY]"
	for _, tc := range []struct {
		name  string
		args  []string
		want  []string
		where string
	}{
		{"-R before close", []string{"pr", "-R", "o/r", "close", "12", "--comment", c}, []string{"pr", "-R", "o/r", "close", "12", "--comment", red}, "--comment"},
		{"--repo before merge", []string{"pr", "--repo", "o/r", "merge", "12", "-t", c}, []string{"pr", "--repo", "o/r", "merge", "12", "-t", red}, "--subject"},
		{"head value ends group", []string{"pr", "create", "-Hwt/" + c + "-tb"}, []string{"pr", "create", "-Hwt/" + c + "-tb"}, ""},
		{"api header ends group", []string{"api", "repos/o/r/issues/1", "-HIf-" + c}, []string{"api", "repos/o/r/issues/1", "-HIf-" + c}, ""},
		{"view template", []string{"pr", "view", "5", "-t", "{{.body}} " + c}, []string{"pr", "view", "5", "-t", "{{.body}} " + c}, ""},
		{"api template", []string{"api", "repos/o/r/pulls/1", "-t", c}, []string{"api", "repos/o/r/pulls/1", "-t", c}, ""},
		{"text flag after bool", []string{"pr", "comment", "7", "-eb" + c}, []string{"pr", "comment", "7", "-eb" + red}, "--body"},
		{"merge -mb", []string{"pr", "merge", "12", "-mb", c}, []string{"pr", "merge", "12", "-mb", red}, "--body"},
		{"merge -mt", []string{"pr", "merge", "12", "-mt", c}, []string{"pr", "merge", "12", "-mt", red}, "--subject"},
		{"review -ab", []string{"pr", "review", "12", "-ab", c}, []string{"pr", "review", "12", "-ab", red}, "--body"},
		{"review -rb", []string{"pr", "review", "12", "-rb" + c}, []string{"pr", "review", "12", "-rb" + red}, "--body"},
		{"merge -sb", []string{"pr", "merge", "12", "-sb=" + c}, []string{"pr", "merge", "12", "-sb=" + red}, "--body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, got, hits := r.GHCall("", tc.args)
			hits = noCmd(hits)
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("args:\n got %q\nwant %q", got, tc.want)
			}
			if tc.where == "" {
				if len(hits) != 0 {
					t.Fatalf("hits = %+v, want none", hits)
				}
				return
			}
			if len(hits) != 1 || hits[0].Where != tc.where {
				t.Fatalf("hits = %+v, want where %q", hits, tc.where)
			}
		})
	}
}

// TestGHCallJSONKeys: a secret used as a JSON object key under
// --input - is redacted like a field key.
func TestGHCallJSONKeys(t *testing.T) {
	r, _ := newTestRedactor(t)
	out, _, hits := r.GHCall(`{"a":{"`+canary+`":"x"}}`, []string{"api", "repos/o/r/issues/1/comments", "--input", "-"})
	if strings.Contains(out, canary) || !strings.Contains(out, `"[redacted:API_KEY]":"x"`) {
		t.Fatalf("stdin = %q", out)
	}
	if hits = noCmd(hits); len(hits) != 1 || hits[0] != (Hit{Where: "stdin", Class: ClassSecretsEnv, Count: 1}) {
		t.Fatalf("hits = %+v", hits)
	}
}

// TestRecordWithoutCmd: a hit with no Cmd (an older broker) logs only
// the subcommand, never the caller's unredacted path.
func TestRecordWithoutCmd(t *testing.T) {
	root := filepath.Join(t.TempDir(), "rec-proj")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	Record(root, "demo", "spore-gh", []string{"api", "repos/o/r/issues/" + canary}, []Hit{{Where: "--body", Class: ClassToken, Count: 1}})
	evs, err := events.Read(filepath.Base(root), time.Time{})
	if err != nil || len(evs) != 1 {
		t.Fatalf("events = %+v, %v", evs, err)
	}
	if evs[0].Fields["cmd"] != "api" || strings.Contains(evs[0].Detail, canary) {
		t.Fatalf("event = %+v", evs[0])
	}
}

// TestGHCallJSONKeyCollision: keys redacted to the same text keep
// every entry, and an untouched key is never overwritten.
func TestGHCallJSONKeyCollision(t *testing.T) {
	r, _ := newTestRedactor(t)
	doc := `{"files":{"/home/a/x.txt":{"content":"1"},"/home/b/y.txt":{"content":"2"},"[redacted]":{"content":"3"},"k":null}}`
	out, _, _ := r.GHCall(doc, []string{"api", "gists", "--input", "-"})
	var got struct{ Files map[string]any }
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 4 || strings.Contains(out, "/home/") {
		t.Fatalf("files = %v", got.Files)
	}
	if c := got.Files["[redacted]"].(map[string]any)["content"]; c != "3" {
		t.Fatalf("untouched key overwritten: %v", got.Files)
	}
	for _, k := range []string{"[redacted]#2", "[redacted]#3", "k"} {
		if _, ok := got.Files[k]; !ok {
			t.Errorf("missing %q in %v", k, got.Files)
		}
	}
}
