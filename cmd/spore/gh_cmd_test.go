package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/versality/spore/internal/events"
	"github.com/versality/spore/internal/ghapp"
)

// ghTestProject points the CLI at a fresh project whose github.toml
// mints tokens from an httptest server, and chdirs into the project.
func ghTestProject(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// A host broker socket would make spore gh exec refuse.
	t.Setenv("SPORE_SECRETS_SOCKET", filepath.Join(t.TempDir(), "no-secrets-broker.sock"))
	root := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPORE_PROJECT_ROOT", root)
	t.Chdir(root)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemPath := filepath.Join(t.TempDir(), "app.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(pemPath, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_cli", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	}))
	t.Cleanup(srv.Close)
	if code := runGH([]string{"config", "--set", "app_id=1", "--set", "installation_id=2",
		"--set=private_key=" + pemPath, "--set", "api_url=" + srv.URL}); code != 0 {
		t.Fatalf("config --set exit %d", code)
	}
	return root
}

func TestGHConfigSetAndPrint(t *testing.T) {
	ghTestProject(t)
	path, err := ghapp.ConfigPath("")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), "app_id = 1\ninstallation_id = 2\nprivate_key = ") {
		t.Fatalf("github.toml = %q", b)
	}
	if code := runGH([]string{"config"}); code != 0 {
		t.Fatalf("config exit %d", code)
	}
	if code := runGH([]string{"config", "--set", "app_id=x"}); code != 1 {
		t.Fatalf("bad --set exit %d", code)
	}
	if code := runGH([]string{"bogus"}); code != 2 {
		t.Fatalf("bogus exit %d", code)
	}
}

func TestGHExecInjectsToken(t *testing.T) {
	ghTestProject(t)
	// The Claude Code sandbox pre-sets GIT_CONFIG_COUNT; Env appends
	// after it, so pin the base for the KEY_1 check below.
	t.Setenv("GIT_CONFIG_COUNT", "0")
	ok := runGH([]string{"exec", "--", "sh", "-c", `test "$GH_TOKEN" = ghs_cli && test "$GIT_CONFIG_KEY_1" = credential.helper`})
	if ok != 0 {
		t.Fatalf("exec exit %d, want 0", ok)
	}
	if code := runGH([]string{"exec", "--", "sh", "-c", "exit 7"}); code != 7 {
		t.Fatalf("exit passthrough = %d, want 7", code)
	}
}

func TestGHExecRefusesSSHRemote(t *testing.T) {
	root := ghTestProject(t)
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@github.com:o/r.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if code := runGH([]string{"exec", "--", "true"}); code != 1 {
		t.Fatalf("exec with ssh origin exit %d, want 1", code)
	}
}

func TestRecordPRCreateWritesLedger(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SPORE_TASK_SLUG", "demo")
	recordPRCreate(root, []string{"gh", "pr", "list"}, "https://github.com/o/r/pull/1\n")
	recordPRCreate(root, []string{"gh", "pr", "create", "--draft", "--title", "x"}, "Creating pull request\nhttps://github.com/o/r/pull/42\n")
	recordPRCreate(root, []string{"gh", "pr", "create"}, "no url here\n")
	evs, err := events.Read(filepath.Base(root), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != events.PROpened || evs[0].Fields["url"] != "https://github.com/o/r/pull/42" || evs[0].Fields["draft"] != "true" || evs[0].Slug != "demo" {
		t.Fatalf("ledger = %+v", evs)
	}
}

func TestGHTokenHelpWarnsAboutTranscripts(t *testing.T) {
	if !strings.Contains(ghUsage, "token lands in its transcript") || !strings.Contains(ghUsage, "spore gh push <branch>") {
		t.Fatalf("spore gh token help lacks the transcript warning:\n%s", ghUsage)
	}
}

// TestPromptsKeepTokensOffCommandLines: recipes, role prompts and the
// App docs that agents follow push and call the API through
// `spore gh exec`, never with a token spliced into the command.
func TestPromptsKeepTokensOffCommandLines(t *testing.T) {
	inline := regexp.MustCompile(`\$\(\s*spore gh token|x-access-token:\$|tok=\$\(|(?m)^\s*spore gh token\s*$`)
	var files []string
	for _, dir := range []string{"../../bootstrap", "../../rules"} {
		err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".md") {
				files = append(files, p)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	files = append(files, "../../docs/github-app.md")
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if m := inline.Find(b); m != nil {
			t.Errorf("%s inlines a token: %q", p, m)
		}
	}
}
