package redact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const canary = "CANARY-vuNfvrcKGX"

// newTestRedactor layers a global and a per-project secrets.env under
// a temp XDG_CONFIG_HOME and points HOME at a temp dir.
func newTestRedactor(t *testing.T) (*Redactor, string) {
	t.Helper()
	cfg := t.TempDir()
	home := t.TempDir()
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
