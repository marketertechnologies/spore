package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/versality/spore/internal/events"
	"github.com/versality/spore/internal/ghapp"
	"github.com/versality/spore/internal/secrets"
)

func fakeBin(t *testing.T, name, body string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// The broker hands gh its token in hosts.yml, not GH_TOKEN; hosts= shows
// the file's token.
const fakeGHScript = `hosts=$(sed -n 's/^    oauth_token: "\(.*\)"$/\1/p' "$GH_CONFIG_DIR/hosts.yml" 2>/dev/null)
echo "gh $* token=${GH_TOKEN:-none} hosts=${hosts:-none}"
if [ "$1 $2" = "pr create" ]; then echo https://github.com/acme/proj/pull/9; fi
case "$*" in *body-file*) echo "body=$(cat)";; esac
`

func withStdin(t *testing.T, body string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(body)
	f.Seek(0, 0)
	orig := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = orig; f.Close() })
}

func noBroker(t *testing.T) {
	t.Setenv("SPORE_SECRETS_SOCKET", filepath.Join(t.TempDir(), "absent.sock"))
}

func TestGHPRWithoutBrokerRunsGHAsApp(t *testing.T) {
	ghTestProject(t)
	noBroker(t)
	fakeBin(t, "gh", fakeGHScript)
	withStdin(t, "hello")
	code, out, errOut := captureFn(t, func() int {
		return runGH([]string{"pr", "create", "--draft", "--title", "t", "--body-file", "-"})
	})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"gh pr create --draft --title t --body-file - token=ghs_cli", "body=hello"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout %q lacks %q", out, want)
		}
	}
	code, out, _ = captureFn(t, func() int { return runGH([]string{"api", "repos/acme/proj/pulls/9"}) })
	if code != 0 || !strings.Contains(out, "gh api repos/acme/proj/pulls/9 token=ghs_cli") {
		t.Fatalf("api: exit %d, stdout %q", code, out)
	}
	if code := runGH([]string{"pr"}); code != 2 {
		t.Fatalf("bare pr exit %d, want 2", code)
	}
}

// On a broker host (the NixOS module's config is present) pointing
// SPORE_SECRETS_SOCKET at nothing must not fall back to the App key.
func TestGHRefusesKeyFallbackOnBrokerHost(t *testing.T) {
	ghTestProject(t)
	noBroker(t)
	fakeBin(t, "gh", fakeGHScript)
	marker := filepath.Join(t.TempDir(), "secrets-broker.json")
	if err := os.WriteFile(marker, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := ghapp.BrokerHostMarker
	ghapp.BrokerHostMarker = marker
	t.Cleanup(func() { ghapp.BrokerHostMarker = orig })
	for _, args := range [][]string{
		{"exec", "--", "gh", "auth", "status"},
		{"token"},
		{"pr", "view", "9"},
		{"api", "repos/acme/proj/pulls/9"},
	} {
		code, out, errOut := captureFn(t, func() int { return runGH(args) })
		if code == 0 || strings.Contains(out, "token=") || !strings.Contains(errOut, "secrets broker") {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", args, code, out, errOut)
		}
	}
}

// ghRemoteProject makes the project a git repo with an https origin and
// a git on PATH that logs pushes instead of sending them.
func ghRemoteProject(t *testing.T) (root, log string) {
	t.Helper()
	root = ghTestProject(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"remote", "add", "origin", "https://github.com/acme/proj.git"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "base"},
		{"checkout", "-q", "-b", "wt/x"},
	} {
		if out, err := exec.Command(realGit, append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	log = filepath.Join(t.TempDir(), "push.log")
	fakeBin(t, "git", `for a; do if [ "$a" = push ]; then echo "$* token=$GH_TOKEN" >> `+log+`; exit 0; fi; done
exec `+realGit+` "$@"
`)
	return root, log
}

func TestGHPushWithoutBrokerPushesAsApp(t *testing.T) {
	root, log := ghRemoteProject(t)
	noBroker(t)
	sha := strings.TrimSpace(gitOut(t, root, "rev-parse", "HEAD"))
	if code := runGH([]string{"push", "wt/x"}); code != 0 {
		t.Fatalf("push exit %d", code)
	}
	if code := runGH([]string{"push", "wt/x", "--force-with-lease=" + sha[:8]}); code != 0 {
		t.Fatalf("leased push exit %d", code)
	}
	b, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	want := []string{
		"-C " + root + " push -u origin refs/heads/wt/x:refs/heads/wt/x token=ghs_cli",
		"-C " + root + " push -u --force-with-lease=refs/heads/wt/x:" + sha + " origin refs/heads/wt/x:refs/heads/wt/x token=ghs_cli",
	}
	if len(lines) != 2 || lines[0] != want[0] || lines[1] != want[1] {
		t.Fatalf("pushes:\n%s\nwant:\n%s", b, strings.Join(want, "\n"))
	}
}

func TestGHPushRefusesBareForce(t *testing.T) {
	_, log := ghRemoteProject(t)
	noBroker(t)
	for _, args := range [][]string{
		{"push", "wt/x", "--force"},
		{"push", "-f", "wt/x"},
		{"push", "+wt/x"},
		{"push", "wt/x:main"},
		{"push", "wt/x", "main"},
		{"push"},
	} {
		if code := runGH(args); code == 0 {
			t.Errorf("%q: exit 0", args)
		}
	}
	if b, _ := os.ReadFile(log); len(b) > 0 {
		t.Fatalf("a refused push reached git:\n%s", b)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// startTestBroker runs an in-process secrets broker for the project at
// root whose App tokens mint from the project's github.toml.
func startTestBroker(t *testing.T, root string) (secretsDir string) {
	t.Helper()
	dir := t.TempDir()
	sd := filepath.Join(dir, "secrets")
	name := filepath.Base(root)
	src, err := ghapp.ConfigPath(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sd, name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd, name, "github.toml"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &secrets.Config{
		SecretsDir: sd,
		StateDir:   filepath.Join(dir, "state"),
		Projects:   map[string]secrets.Project{name: {Repo: "github.com/acme/" + name}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	s, err := secrets.NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Log = &bytes.Buffer{}
	sock := filepath.Join(t.TempDir(), "b.sock")
	l, err := secrets.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go s.Serve(l)
	t.Setenv("SPORE_SECRETS_SOCKET", sock)
	return sd
}

func TestGHPRThroughBroker(t *testing.T) {
	root := ghTestProject(t)
	t.Setenv("SPORE_TASK_SLUG", "demo")
	startTestBroker(t, root)
	fakeBin(t, "gh", fakeGHScript)
	withStdin(t, "hello")

	code, out, errOut := captureFn(t, func() int {
		return runGH([]string{"pr", "create", "--draft", "--title", "t", "--body-file", "-"})
	})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"gh pr create --draft --title t --body-file - token=none hosts=ghs_cli", "body=hello", "/pull/9"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout %q lacks %q", out, want)
		}
	}
	evs, err := events.Read(filepath.Base(root), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != events.PROpened || evs[0].Fields["draft"] != "true" {
		t.Fatalf("ledger = %+v", evs)
	}

	code, out, _ = captureFn(t, func() int { return runGH([]string{"pr", "ready", "9"}) })
	if code != 0 || !strings.Contains(out, "gh pr ready 9") {
		t.Fatalf("pr ready: exit %d, stdout %q", code, out)
	}
	code, _, errOut = captureFn(t, func() int { return runGH([]string{"pr", "create", "--title", "t"}) })
	if code != 1 || !strings.Contains(errOut, "must be --draft") {
		t.Fatalf("non-draft create: exit %d, stderr %q", code, errOut)
	}
	code, _, errOut = captureFn(t, func() int { return runGH([]string{"token"}) })
	if code != 1 || !strings.Contains(errOut, "held by the secrets broker") {
		t.Fatalf("token behind the broker: exit %d, stderr %q", code, errOut)
	}
}

func TestGHPRBodyFileReadByClient(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.env")
	if err := os.WriteFile(outside, []byte("TOKEN=x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if real, err := filepath.EvalSymlinks(tmp); err == nil {
		prev := bodyTmpRoot
		bodyTmpRoot = real
		t.Cleanup(func() { bodyTmpRoot = prev })
	}
	root := ghTestProject(t)
	gitOut(t, root, "init", "-q")
	fakeBin(t, "gh", fakeGHScript)
	if err := os.WriteFile(filepath.Join(root, "body.md"), []byte("from file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.md")); err != nil {
		t.Fatal(err)
	}
	tmpBody := filepath.Join(os.TempDir(), "b.md")
	if err := os.WriteFile(tmpBody, []byte("from tmp"), 0o600); err != nil {
		t.Fatal(err)
	}
	check := func(name string) {
		t.Helper()
		for _, tc := range []struct {
			args []string
			want string
		}{
			{[]string{"pr", "create", "--draft", "--title", "t", "--body-file", "body.md"}, "body=from file"},
			{[]string{"pr", "comment", "9", "--body-file=" + tmpBody}, "body=from tmp"},
		} {
			code, out, errOut := captureFn(t, func() int { return runGH(tc.args) })
			if code != 0 || !strings.Contains(out, tc.want) || !strings.Contains(out, "body-file") || strings.Contains(out, "body.md") {
				t.Errorf("%s %q: exit %d, stdout %q, stderr %q", name, tc.args, code, out, errOut)
			}
		}
		for _, p := range []string{outside, "link.md", "missing.md"} {
			code, out, _ := captureFn(t, func() int { return runGH([]string{"pr", "comment", "9", "--body-file", p}) })
			if code == 0 || strings.Contains(out, "TOKEN") {
				t.Errorf("%s: body file %s accepted: %q", name, p, out)
			}
		}
	}
	noBroker(t)
	check("no broker")
	startTestBroker(t, root)
	check("broker")
}

func TestGHUnreadableKeyInSandboxExplains(t *testing.T) {
	ghTestProject(t)
	noBroker(t)
	if code := runGH([]string{"config", "--set", "private_key=" + filepath.Join(t.TempDir(), "gone.pem")}); code != 0 {
		t.Fatalf("config exit %d", code)
	}
	t.Setenv("SANDBOX_RUNTIME", "1")
	code, _, errOut := captureFn(t, func() int { return runGH([]string{"pr", "view", "9"}) })
	if code != 1 || !strings.Contains(errOut, "no redirect, pipe") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

func TestGHRefusesFileReadingFlagsWithoutBroker(t *testing.T) {
	ghTestProject(t)
	noBroker(t)
	log := filepath.Join(t.TempDir(), "gh.log")
	fakeBin(t, "gh", `echo "$*" >> `+log+"\n")
	for _, args := range [][]string{
		{"api", "repos/o/r/issues/1/comments", "-F", "body=@/etc/passwd"},
		{"api", "repos/o/r/issues/1/comments", "--field", "body=@/etc/passwd"},
		{"api", "repos/o/r/issues/1/comments", "--field=body=@/etc/passwd"},
		{"api", "repos/o/r/issues/1/comments", "-Fbody=@/etc/passwd"},
		{"api", "repos/o/r/issues/1/comments", "-F=body=@/etc/passwd"},
		{"api", "repos/o/r/issues/1/comments", "-iF", "body=@/etc/passwd"},
		{"api", "repos/o/r/issues/1/comments", "-iFbody=@/etc/passwd"},
		{"pr", "comment", "9", "-F=/etc/passwd"},
		{"pr", "comment", "9", "-F/etc/passwd"},
	} {
		if code := runGH(args); code == 0 {
			t.Errorf("%q: exit 0", args)
		}
	}
	if b, _ := os.ReadFile(log); len(b) > 0 {
		t.Fatalf("a refused call reached gh:\n%s", b)
	}
}

func TestGHBodyFileIgnoresTMPDIROutsideTmp(t *testing.T) {
	home := t.TempDir()
	secret := filepath.Join(home, "secrets.env")
	if err := os.WriteFile(secret, []byte("TOKEN=x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(home, "/tmp/") {
		t.Skip("test temp dir is not under /tmp")
	}
	if _, err := readBodyFile(secret); err != nil {
		t.Fatalf("file under a /tmp TMPDIR refused: %v", err)
	}
	t.Chdir("/")
	t.Setenv("TMPDIR", "/etc")
	if _, err := readBodyFile("/etc/passwd"); err == nil {
		t.Fatal("TMPDIR=/etc made /etc/passwd a body file")
	}
}

func TestGHPushRefusesOtherProjectRepo(t *testing.T) {
	root := ghTestProject(t)
	startTestBroker(t, root)
	other := filepath.Join(t.TempDir(), "other")
	gitOut(t, t.TempDir(), "init", "-q", "-b", "main", other)
	gitOut(t, other, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "x")
	gitOut(t, other, "checkout", "-q", "-b", "wt/x")
	t.Chdir(other)
	code, _, errOut := captureFn(t, func() int { return runGH([]string{"push", "wt/x"}) })
	if code != 1 || !strings.Contains(errOut, "is not the project") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

const ghCanary = "CANARY-vuNfvrcKGX"

type ghRedactCase struct {
	name   string
	args   []string
	stdin  string
	where  string
	broker bool
}

// ghRedactCases post the canary through every flag form a body or
// title takes; broker marks the ones the broker's allowlist accepts.
var ghRedactCases = []ghRedactCase{
	{"body equals", []string{"pr", "comment", "9", "--body=see " + ghCanary}, "", "--body", true},
	{"body space", []string{"pr", "comment", "9", "--body", "see " + ghCanary}, "", "--body", true},
	{"body short", []string{"pr", "comment", "9", "-b", ghCanary}, "", "--body", false},
	{"review body joined", []string{"pr", "review", "9", "--comment", "-b" + ghCanary}, "", "--body", false},
	{"title equals", []string{"pr", "edit", "9", "--title=" + ghCanary}, "", "--title", true},
	{"title short", []string{"pr", "edit", "9", "-t", ghCanary}, "", "--title", false},
	{"api -f", []string{"api", "repos/acme/proj/issues/9/comments", "-f", "body=" + ghCanary}, "", "field body", true},
	{"api -F", []string{"api", "repos/acme/proj/pulls/9", "-X", "POST", "-F", "title=" + ghCanary}, "", "field title", true},
	{"api --field=", []string{"api", "repos/acme/proj/pulls/9/reviews", "--field=body=" + ghCanary}, "", "field body", true},
	{"api --raw-field", []string{"api", "repos/acme/proj/pulls/9/comments", "--raw-field", "body=" + ghCanary}, "", "field body", true},
	{"api --input -", []string{"api", "repos/acme/proj/pulls/9/reviews", "--input", "-"}, `{"body":"` + ghCanary + `"}`, "stdin", true},
	{"api --input=-", []string{"api", "repos/acme/proj/pulls/9/reviews", "--input=-"}, `{"body":"` + ghCanary + `"}`, "stdin", true},
	{"body-file stdin", []string{"pr", "comment", "9", "--body-file", "-"}, "see " + ghCanary, "stdin", true},
	{"close comment", []string{"pr", "close", "9", "--comment", "see " + ghCanary}, "", "--comment", false},
	{"merge subject", []string{"pr", "merge", "9", "--squash", "--subject=" + ghCanary}, "", "--subject", false},
	{"api grouped short", []string{"api", "repos/acme/proj/issues/9/comments", "-iF", "body=" + ghCanary}, "", "field body", false},
	{"api field before path", []string{"api", "-fbody=" + ghCanary, "repos/acme/proj/issues/9/comments"}, "", "field body", false},
	{"api raw-field= before path", []string{"api", "--raw-field=body=" + ghCanary, "repos/acme/proj/issues/9/comments"}, "", "field body", false},
	{"api -f before path", []string{"api", "-f", "body=" + ghCanary, "repos/acme/proj/issues/9/comments"}, "", "field body", false},
}

// checkGHRedacted runs c against a gh that echoes its args and stdin,
// and checks the canary never reached it and the ledger holds one
// secrets_env redaction for c.
func checkGHRedacted(t *testing.T, root string, c ghRedactCase) {
	t.Helper()
	withStdin(t, c.stdin)
	before := len(ghRedactionEvents(t, root))
	code, out, errOut := captureFn(t, func() int { return runGH(c.args) })
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if strings.Contains(out, ghCanary) || !strings.Contains(out, "[redacted:API_KEY]") {
		t.Fatalf("gh got %q", out)
	}
	hits := ghRedactionEvents(t, root)[before:]
	if len(hits) != 1 {
		t.Fatalf("new redaction events = %+v", hits)
	}
	if raw, _ := json.Marshal(hits[0]); strings.Contains(string(raw), ghCanary) {
		t.Errorf("event carries the secret: %s", raw)
	}
	f := hits[0].Fields
	if f["class"] != "secrets_env" || f["where"] != c.where || f["count"] != "1" || f["via"] != "spore-gh" || hits[0].Slug != "demo" {
		t.Errorf("event = %+v", hits[0])
	}
}

func ghRedactionEvents(t *testing.T, root string) []events.Event {
	t.Helper()
	evs, err := events.Read(filepath.Base(root), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var out []events.Event
	for _, e := range evs {
		if e.Kind == events.Redaction {
			out = append(out, e)
		}
	}
	return out
}

const ghEchoScript = `echo "gh $*"
case "$*" in *body-file*|*input*) echo "stdin=$(cat)";; esac
`

func ghSecretsEnv(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("API_KEY="+ghCanary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGHRedactsEveryFormWithoutBroker(t *testing.T) {
	root := ghTestProject(t)
	noBroker(t)
	t.Setenv("SPORE_TASK_SLUG", "demo")
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	ghSecretsEnv(t, filepath.Join(cfg, "spore"))
	fakeBin(t, "gh", ghEchoScript)
	for _, c := range ghRedactCases {
		t.Run(c.name, func(t *testing.T) { checkGHRedacted(t, root, c) })
	}
}

func TestGHRedactsEveryFormThroughBroker(t *testing.T) {
	root := ghTestProject(t)
	t.Setenv("SPORE_TASK_SLUG", "demo")
	sd := startTestBroker(t, root)
	ghSecretsEnv(t, filepath.Join(sd, filepath.Base(root)))
	fakeBin(t, "gh", ghEchoScript)
	for _, c := range ghRedactCases {
		if c.broker {
			t.Run(c.name, func(t *testing.T) { checkGHRedacted(t, root, c) })
		}
	}
}

type ghEvent struct{ where, class string }

// ghRefAndKeyCases: a token in a ref-like field value and a secret in
// a field key reach neither gh nor the ledger.
var ghRefAndKeyCases = []struct {
	name   string
	args   []string
	secret string
	want   []ghEvent
	broker bool
}{
	{"labels token", []string{"api", "repos/acme/proj/issues/9/labels", "-f", "labels[]=" + ghToken}, ghToken, []ghEvent{{"field labels", "token"}}, true},
	{"graphql name token", []string{"api", "graphql", "-f", "query=query{viewer{login}}", "-f", "name=" + ghToken}, ghToken, []ghEvent{{"field name", "token"}}, false},
	{"secret key", []string{"api", "repos/acme/proj/issues/9/comments", "-f", ghCanary + "=v"}, ghCanary, []ghEvent{{"field [redacted:API_KEY] key", "secrets_env"}}, true},
	{"secret nested key and value", []string{"api", "repos/acme/proj/issues/9/comments", "-f", "body[" + ghCanary + "]=" + ghCanary}, ghCanary, []ghEvent{
		{"field [redacted:API_KEY] key", "secrets_env"}, {"field [redacted:API_KEY]", "secrets_env"},
	}, true},
	{"token key", []string{"api", "repos/acme/proj/issues/9/comments", "-f", ghToken + "=v"}, ghToken, []ghEvent{{"field [redacted] key", "token"}}, true},
}

var ghToken = "ghp_" + strings.Repeat("Xy7", 12)

func checkGHRefAndKey(t *testing.T, root string, args []string, secret string, want []ghEvent) {
	t.Helper()
	withStdin(t, "")
	before := len(ghRedactionEvents(t, root))
	code, out, errOut := captureFn(t, func() int { return runGH(args) })
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if strings.Contains(out, secret) || !strings.Contains(out, "[redacted") {
		t.Fatalf("gh got %q", out)
	}
	evs := ghRedactionEvents(t, root)[before:]
	if len(evs) != len(want) {
		t.Fatalf("new redaction events = %+v, want %+v", evs, want)
	}
	for i, e := range evs {
		if raw, _ := json.Marshal(e); strings.Contains(string(raw), secret) {
			t.Errorf("event carries the secret: %s", raw)
		}
		if e.Fields["where"] != want[i].where || e.Fields["class"] != want[i].class {
			t.Errorf("event %d = %+v, want %+v", i, e.Fields, want[i])
		}
	}
}

func TestGHRedactsRefsAndKeysWithoutBroker(t *testing.T) {
	root := ghTestProject(t)
	noBroker(t)
	t.Setenv("SPORE_TASK_SLUG", "demo")
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	ghSecretsEnv(t, filepath.Join(cfg, "spore"))
	fakeBin(t, "gh", ghEchoScript)
	for _, c := range ghRefAndKeyCases {
		t.Run(c.name, func(t *testing.T) { checkGHRefAndKey(t, root, c.args, c.secret, c.want) })
	}
}

func TestGHRedactsRefsAndKeysThroughBroker(t *testing.T) {
	root := ghTestProject(t)
	t.Setenv("SPORE_TASK_SLUG", "demo")
	sd := startTestBroker(t, root)
	ghSecretsEnv(t, filepath.Join(sd, filepath.Base(root)))
	fakeBin(t, "gh", ghEchoScript)
	for _, c := range ghRefAndKeyCases {
		if c.broker {
			t.Run(c.name, func(t *testing.T) { checkGHRefAndKey(t, root, c.args, c.secret, c.want) })
		}
	}
}
