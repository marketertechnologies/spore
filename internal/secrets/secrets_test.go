package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const dbSecret = "postgres://ro:hunter2-very-secret@db.internal/app"

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testConfig(t *testing.T) *Config {
	t.Helper()
	dir := t.TempDir()
	sd := filepath.Join(dir, "secrets")
	writeFile(t, filepath.Join(sd, "secrets.env"), "SHARED_TOKEN=shared-global-value\n")
	writeFile(t, filepath.Join(sd, "alpha", "secrets.env"), "export DATABASE_URL_RO='"+dbSecret+"'\n")
	writeFile(t, filepath.Join(sd, "beta", "secrets.env"), "SENTRY_TOKEN=sntrys_beta_secret_value\n")
	c := &Config{
		SecretsDir: sd,
		StateDir:   filepath.Join(dir, "state"),
		FleetFlag:  filepath.Join(dir, "fleet", "fleet-enabled"),
		Projects: map[string]Project{
			"alpha": {Repo: "github.com/acme/alpha"},
			"beta":  {Repo: "github.com/acme/beta"},
		},
		Profiles: map[string]Profile{
			"alpha-db": {
				Project: "alpha",
				Vars:    map[string]string{"DATABASE_URL": "DATABASE_URL_RO"},
				Commands: map[string]Command{
					"echo": {Argv: []string{"sh", "-c", `printf 'url=%s arg=%s\n' "$DATABASE_URL" "$1"`, "sh"}, Args: []string{`[a-z0-9 ]+`}},
					"env":  {Argv: []string{"env"}},
					"cat":  {Argv: []string{"cat"}, Stdin: true},
				},
			},
			"beta-sentry": {
				Project:  "beta",
				Vars:     map[string]string{"TOKEN": "SENTRY_TOKEN"},
				Commands: map[string]Command{"show": {Argv: []string{"sh", "-c", `echo "$TOKEN"`}}},
			},
		},
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

func startServer(t *testing.T, c *Config) *Server {
	t.Helper()
	s, err := NewServer(c)
	if err != nil {
		t.Fatal(err)
	}
	s.Log = &bytes.Buffer{}
	sock := filepath.Join(t.TempDir(), "b.sock")
	l, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go s.Serve(l)
	t.Setenv("SPORE_SECRETS_SOCKET", sock)
	return s
}

var agent = Caller{UID: 1000, Agent: true, Name: "agent"}
var pilot = Caller{UID: 0, Pilot: true, Name: "root"}

func TestRunInjectsAndRedacts(t *testing.T) {
	c := testConfig(t)
	startServer(t, c)
	resp, _, err := Call(Request{Op: OpRun, Project: "alpha", Profile: "alpha-db", Command: "echo", Args: []string{"hello 1"}}, nil, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resp.Stdout, "hunter2") {
		t.Fatalf("secret leaked: %q", resp.Stdout)
	}
	if want := "url=[redacted:DATABASE_URL_RO] arg=hello 1\n"; resp.Stdout != want {
		t.Fatalf("stdout = %q, want %q", resp.Stdout, want)
	}

	resp, _, err = Call(Request{Op: OpRun, Project: "alpha", Profile: "alpha-db", Command: "env"}, nil, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resp.Stdout, "hunter2") || !strings.Contains(resp.Stdout, "DATABASE_URL=[redacted:DATABASE_URL_RO]") {
		t.Fatalf("env output: %q", resp.Stdout)
	}
	if strings.Contains(resp.Stdout, "SHARED_TOKEN") {
		t.Fatalf("env carries a var outside the profile: %q", resp.Stdout)
	}

	resp, _, err = Call(Request{Op: OpRun, Project: "alpha", Profile: "alpha-db", Command: "cat", Stdin: "x " + dbSecret + " y"}, nil, 10*time.Second)
	if err != nil || strings.Contains(resp.Stdout, "hunter2") {
		t.Fatalf("cat: %q %v", resp.Stdout, err)
	}

	log, _ := os.ReadFile(filepath.Join(c.StateDir, "audit.log"))
	if !bytes.Contains(log, []byte(`"profile":"alpha-db"`)) || bytes.Contains(log, []byte("hunter2")) {
		t.Fatalf("audit log: %s", log)
	}
}

func TestRunRefusals(t *testing.T) {
	c := testConfig(t)
	startServer(t, c)
	cases := []struct {
		name string
		req  Request
		want string
	}{
		{"bad arg", Request{Profile: "alpha-db", Command: "echo", Args: []string{"$(id)"}}, "argument 1 is not allowed"},
		{"extra arg", Request{Profile: "alpha-db", Command: "echo", Args: []string{"a", "b"}}, "exactly 1"},
		{"no args allowed", Request{Profile: "alpha-db", Command: "env", Args: []string{"-0"}}, "takes no arguments"},
		{"unknown command", Request{Profile: "alpha-db", Command: "bash"}, "has no command"},
		{"stdin not allowed", Request{Profile: "alpha-db", Command: "env", Stdin: "x"}, "does not take stdin"},
		{"other project worker", Request{Project: "alpha", Task: "t1", Profile: "beta-sentry", Command: "show"}, "not available"},
		{"unknown profile", Request{Profile: "nope", Command: "x"}, "not available"},
	}
	for _, tc := range cases {
		tc.req.Op = OpRun
		if tc.req.Project == "" {
			tc.req.Project = "alpha"
		}
		_, _, err := Call(tc.req, nil, 10*time.Second)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestScopingAndGrants(t *testing.T) {
	c := testConfig(t)
	s, err := NewServer(c)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	worker := Request{Project: "alpha", Task: "t1"}
	coord := Request{Project: "alpha"}

	if ok, _ := s.allowed(agent, worker, "alpha-db"); !ok {
		t.Fatal("worker denied its own project's profile")
	}
	if ok, _ := s.allowed(agent, worker, "beta-sentry"); ok {
		t.Fatal("worker allowed another project's profile")
	}
	if ok, _ := s.allowed(agent, coord, "beta-sentry"); !ok {
		t.Fatal("coordinator denied a profile")
	}

	if r := s.grant(agent, Request{Project: "alpha", Task: "t1", Profile: "beta-sentry", For: "1h"}); !strings.Contains(r.Error, "refused") {
		t.Fatalf("agent grant: %+v", r)
	}
	if r := s.grant(pilot, Request{Project: "alpha", Task: "t1", Profile: "beta-sentry", For: "30d"}); r.Error == "" {
		t.Fatal("grant over MaxGrant accepted")
	}
	if r := s.grant(pilot, Request{Project: "alpha", Task: "t1", Profile: "beta-sentry", For: "1h"}); r.Error != "" {
		t.Fatal(r.Error)
	}
	if ok, via := s.allowed(agent, worker, "beta-sentry"); !ok || !via {
		t.Fatal("grant not honoured")
	}
	if ok, _ := s.allowed(agent, Request{Project: "alpha", Task: "t2"}, "beta-sentry"); ok {
		t.Fatal("grant leaked to another task")
	}
	now = now.Add(61 * time.Minute)
	if ok, _ := s.allowed(agent, worker, "beta-sentry"); ok {
		t.Fatal("expired grant honoured")
	}

	now = now.Add(time.Minute)
	s.grant(pilot, Request{Project: "alpha", Task: "t1", Profile: "beta-sentry", For: "1h"})
	if r := s.revoke(pilot, Request{Task: "t1"}); r.Stdout != "revoked 1 grant(s)\n" {
		t.Fatalf("revoke: %+v", r)
	}
	if ok, _ := s.allowed(agent, worker, "beta-sentry"); ok {
		t.Fatal("revoked grant honoured")
	}
}

func TestFleetFlag(t *testing.T) {
	c := testConfig(t)
	s, err := NewServer(c)
	if err != nil {
		t.Fatal(err)
	}
	if r := s.fleet(agent, Request{Enable: true}); !strings.Contains(r.Error, "refused") {
		t.Fatalf("agent enable: %+v", r)
	}
	if _, err := os.Stat(c.FleetFlag); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("flag created for agent")
	}
	if r := s.fleet(pilot, Request{Enable: true}); r.Error != "" {
		t.Fatal(r.Error)
	}
	if _, err := os.Stat(c.FleetFlag); err != nil {
		t.Fatal(err)
	}
	if r := s.fleet(agent, Request{}); r.Error != "" {
		t.Fatal(r.Error)
	}
	if _, err := os.Stat(c.FleetFlag); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("agent disable left the flag")
	}
}

func TestConfigValidate(t *testing.T) {
	base := func() *Config {
		return &Config{SecretsDir: "/s", StateDir: "/st", Projects: map[string]Project{"a": {}}}
	}
	bad := map[string]Profile{
		"unknown var":     {Project: "a", Commands: map[string]Command{"x": {Argv: []string{"x", "${NOPE}"}}}},
		"unknown project": {Project: "b", Commands: map[string]Command{"x": {Argv: []string{"x"}}}},
		"var in argv0":    {Project: "a", Vars: map[string]string{"X": "Y"}, Commands: map[string]Command{"x": {Argv: []string{"${X}"}}}},
		"no commands":     {Project: "a"},
		"bad regexp":      {Project: "a", Commands: map[string]Command{"x": {Argv: []string{"x"}, Args: []string{"("}}}},
		"long timeout":    {Project: "a", Commands: map[string]Command{"x": {Argv: []string{"x"}, Timeout: "1h"}}},
	}
	for name, p := range bad {
		c := base()
		c.Profiles = map[string]Profile{"p": p}
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCheckArgsVariadic(t *testing.T) {
	c := &Config{SecretsDir: "/s", StateDir: "/st", Projects: map[string]Project{"a": {}},
		Profiles: map[string]Profile{"p": {Project: "a", Commands: map[string]Command{
			"q": {Argv: []string{"q"}, Args: []string{"-c", `[^\\]*`}, Variadic: true},
		}}}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	cmd := c.Profiles["p"].Commands["q"]
	for _, ok := range [][]string{{"-c"}, {"-c", "select 1"}, {"-c", "a", "b"}} {
		if err := cmd.checkArgs(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range [][]string{{}, {"-f"}, {"-c", `\! env`}} {
		if err := cmd.checkArgs(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCheckGHArgs(t *testing.T) {
	repo := "github.com/acme/alpha"
	ok := [][]string{
		{"pr", "list", "--head", "wt/x", "--state", "open", "--json", "number,url", "--limit", "1"},
		{"pr", "create", "--draft", "--base", "main", "--head", "wt/x", "--title", "t", "--body-file", "-", "--assignee", "op"},
		{"pr", "ready", "7", "--undo"},
		{"pr", "edit", "7", "--title", "t", "--body-file", "-"},
		{"pr", "comment", "7", "--body-file", "-"},
		{"pr", "view", "7", "--json", "isDraft", "--jq", ".isDraft"},
		{"api", "repos/acme/alpha/pulls/7/reviews", "--hostname", "github.com", "--method", "POST", "--input", "-"},
		{"api", "repos/acme/alpha/pulls/7/files", "--hostname", "github.com", "--paginate", "--jq", ".[]"},
		{"api", "graphql", "--hostname", "github.com", "-f", "query=query { viewer { login } }", "-F", "number=7"},
		{"pr", "checks", "7", "--required", "--json", "name,state"},
		{"api", "repos/acme/alpha/commits/9cc70240d3c90179350720a9e322f60d70bf60a8/check-runs", "--jq", ".check_runs[].conclusion"},
		{"api", "repos/acme/alpha/commits/9cc7024/status", "--method", "GET"},
		{"api", "repos/acme/alpha/commits/9cc7024"},
	}
	for _, a := range ok {
		if err := checkGHArgs(a, repo); err != nil {
			t.Errorf("%q: %v", a, err)
		}
	}
	bad := [][]string{
		{"pr", "merge", "7"},
		{"pr", "create", "--base", "main", "--head", "wt/x"},
		{"pr", "list", "--repo", "acme/other"},
		{"pr", "comment", "7", "--body-file", "/etc/shadow"},
		{"api", "repos/acme/other/pulls/7/reviews"},
		{"api", "repos/acme/alpha/contents/x"},
		{"api", "repos/acme/alpha/pulls/7/reviews", "--input", "/var/lib/spore-secrets/alpha/key.pem"},
		{"api", "graphql", "-F", "q=@/var/lib/spore-secrets/secrets.env"},
		{"api", "graphql", "-f", "query=mutation { deleteRef }"},
		{"api", "repos/acme/alpha/pulls/7", "--method", "DELETE"},
		{"api", "repos/acme/alpha/pulls/7", "--hostname", "evil.example"},
		{"api", "repos/acme/alpha/commits/9cc7024/statuses", "--method", "POST"},
		{"api", "repos/acme/alpha/commits/9cc7024/check-runs", "-f", "name=fake"},
		{"api", "repos/acme/alpha/commits/9cc7024/comments"},
		{"api", "repos/acme/other/commits/9cc7024/status"},
		{"auth", "token"},
		{"repo", "clone", "acme/alpha"},
	}
	for _, a := range bad {
		if err := checkGHArgs(a, repo); err == nil {
			t.Errorf("%q accepted", a)
		}
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestPushAndFetchThroughBundles(t *testing.T) {
	c := testConfig(t)
	s := startServer(t, c)
	tmp := t.TempDir()
	origin := filepath.Join(tmp, "origin.git")
	git(t, tmp, "init", "--quiet", "--bare", "-b", "main", origin)
	s.remote = func(string) string { return origin }
	s.mint = func(context.Context, string) (string, error) { return "ghs_testtoken", nil }

	work := filepath.Join(tmp, "work")
	git(t, tmp, "init", "--quiet", "-b", "main", work)
	git(t, work, "commit", "--quiet", "--allow-empty", "-m", "base")
	git(t, work, "remote", "add", "origin", origin)
	git(t, work, "push", "--quiet", "origin", "main")
	git(t, work, "fetch", "--quiet", "origin")
	git(t, work, "checkout", "--quiet", "-b", "wt/feature")
	git(t, work, "commit", "--quiet", "--allow-empty", "-m", "one")
	sha := git(t, work, "rev-parse", "HEAD")

	if err := Push("alpha", work, "wt/feature", sha, "", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := git(t, origin, "rev-parse", "refs/heads/wt/feature"); got != sha {
		t.Fatalf("origin has %s, want %s", got, sha)
	}

	git(t, work, "checkout", "--quiet", "-b", "main-copy", "main")
	err := Push("alpha", work, "main-copy", git(t, work, "rev-parse", "HEAD"), "", 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "push_branch") {
		t.Fatalf("push of non-task branch: %v", err)
	}
	_, _, err = Call(Request{Op: OpPush, Project: "alpha", Branch: "main", SHA: sha, PayloadSize: 1}, strings.NewReader("x"), 10*time.Second)
	if err == nil || !strings.Contains(err.Error(), "push_branch") {
		t.Fatalf("push to main: %v", err)
	}

	git(t, work, "checkout", "--quiet", "wt/feature")
	git(t, work, "commit", "--quiet", "--amend", "--allow-empty", "-m", "rewritten")
	rewritten := git(t, work, "rev-parse", "HEAD")
	if err := Push("alpha", work, "wt/feature", rewritten, "", 30*time.Second); err == nil {
		t.Fatal("non-fast-forward accepted without a lease")
	}
	if err := Push("alpha", work, "wt/feature", rewritten, sha, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := git(t, origin, "rev-parse", "refs/heads/wt/feature"); got != rewritten {
		t.Fatalf("origin has %s after leased push, want %s", got, rewritten)
	}

	// A checked commit behind the branch tip goes up alone.
	git(t, work, "commit", "--quiet", "--allow-empty", "-m", "checked")
	checked := git(t, work, "rev-parse", "HEAD")
	git(t, work, "commit", "--quiet", "--allow-empty", "-m", "unchecked")
	if err := Push("alpha", work, "wt/feature", checked, "", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := git(t, origin, "rev-parse", "refs/heads/wt/feature"); got != checked {
		t.Fatalf("origin has %s, want the checked commit %s", got, checked)
	}
	if err := Push("alpha", work, "wt/feature", strings.Repeat("a", 40), "", 30*time.Second); err == nil {
		t.Fatal("push of a commit outside the bundle accepted")
	}

	// A remote-tracking ref origin no longer has makes the thin bundle
	// depend on a commit the broker cannot fetch: it asks for a full one.
	git(t, work, "commit", "--quiet", "--allow-empty", "-m", "ghost")
	git(t, work, "update-ref", "refs/remotes/origin/ghost", "HEAD")
	git(t, work, "commit", "--quiet", "--allow-empty", "-m", "two")
	two := git(t, work, "rev-parse", "HEAD")
	if _, _, err := Call(Request{Op: OpPush, Project: "alpha", Branch: "wt/feature", SHA: two, PayloadSize: 0}, nil, 10*time.Second); err == nil {
		t.Fatal("push without a bundle accepted")
	}
	thin := filepath.Join(tmp, "thin.bundle")
	git(t, work, "bundle", "create", "--quiet", thin, "refs/heads/wt/feature", "--not", "--remotes=origin")
	b, _ := os.ReadFile(thin)
	_, _, err = Call(Request{Op: OpPush, Project: "alpha", Branch: "wt/feature", SHA: two, PayloadSize: int64(len(b))}, bytes.NewReader(b), 30*time.Second)
	var be *BrokerError
	if !errors.As(err, &be) || !be.NeedFull {
		t.Fatalf("thin bundle with a missing base: %v", err)
	}
	if err := Push("alpha", work, "wt/feature", two, "", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := git(t, origin, "rev-parse", "refs/heads/wt/feature"); got != two {
		t.Fatalf("origin has %s after full-bundle push, want %s", got, two)
	}

	// Fetch: a second clone learns origin/main through the broker.
	other := filepath.Join(tmp, "other")
	git(t, tmp, "init", "--quiet", "-b", "main", other)
	other2 := filepath.Join(tmp, "seed")
	git(t, tmp, "clone", "--quiet", origin, other2)
	git(t, other2, "commit", "--quiet", "--allow-empty", "-m", "main moves")
	git(t, other2, "push", "--quiet", "origin", "main")
	if err := Fetch("alpha", other, "main", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if got, want := git(t, other, "rev-parse", "refs/remotes/origin/main"), git(t, origin, "rev-parse", "main"); got != want {
		t.Fatalf("fetched %s, want %s", got, want)
	}
	if err := Fetch("alpha", other, "main", 30*time.Second); err != nil {
		t.Fatalf("up-to-date fetch: %v", err)
	}
}

func TestPeerCredRefusesStrangers(t *testing.T) {
	c := testConfig(t)
	s, err := NewServer(c)
	if err != nil {
		t.Fatal(err)
	}
	s.agentUID = os.Getuid() + 1
	s.Log = &bytes.Buffer{}
	sock := filepath.Join(t.TempDir(), "b.sock")
	l, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go s.Serve(l)
	t.Setenv("SPORE_SECRETS_SOCKET", sock)
	_, _, err = Call(Request{Op: OpList}, nil, 5*time.Second)
	if os.Getuid() != 0 && (err == nil || !strings.Contains(err.Error(), "neither the agent user nor a pilot")) {
		t.Fatalf("stranger call: %v", err)
	}
}

func TestListShowsNoValues(t *testing.T) {
	c := testConfig(t)
	startServer(t, c)
	resp, _, err := Call(Request{Op: OpList, Project: "alpha", Task: "t1"}, nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var infos []ProfileInfo
	if err := json.Unmarshal(resp.Data, &infos); err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Name != "alpha-db" {
		t.Fatalf("worker list: %+v", infos)
	}
	if bytes.Contains(resp.Data, []byte("hunter2")) {
		t.Fatal("list leaked a value")
	}
}

// bloblessFixture is an origin that serves filtered fetches, and a
// work clone whose origin/main holds two versions of one large file
// (old is the first version's blob), on a local branch wt/edit with a
// third version on top (sha). The broker's mirror is still cold.
type bloblessFixture struct {
	s                   *Server
	c                   *Config
	tmp, origin, work   string
	big, body, old, sha string
}

func newBloblessFixture(t *testing.T) *bloblessFixture {
	t.Helper()
	f := &bloblessFixture{c: testConfig(t), tmp: t.TempDir()}
	f.s = startServer(t, f.c)
	f.origin = filepath.Join(f.tmp, "origin.git")
	servingOrigin(t, f.tmp, f.origin)
	origin := f.origin
	f.s.remote = func(string) string { return origin }
	f.s.mint = func(context.Context, string) (string, error) { return "ghs_testtoken", nil }

	f.work = filepath.Join(f.tmp, "work")
	git(t, f.tmp, "init", "--quiet", "-b", "main", f.work)
	f.big = filepath.Join(f.work, "big.txt")
	f.body = strings.Repeat("line of history that compresses well\n", 4000)
	writeFile(t, f.big, f.body)
	git(t, f.work, "add", "big.txt")
	git(t, f.work, "commit", "--quiet", "-m", "base")
	f.old = git(t, f.work, "rev-parse", "HEAD:big.txt")
	writeFile(t, f.big, f.body+"v2\n")
	git(t, f.work, "commit", "--quiet", "-am", "v2")
	git(t, f.work, "remote", "add", "origin", f.origin)
	git(t, f.work, "push", "--quiet", "origin", "main")
	git(t, f.work, "fetch", "--quiet", "origin")
	git(t, f.work, "checkout", "--quiet", "-b", "wt/edit")
	writeFile(t, f.big, f.body+"v2\nv3\n")
	git(t, f.work, "commit", "--quiet", "-am", "v3")
	f.sha = git(t, f.work, "rev-parse", "HEAD")
	return f
}

// servingOrigin makes a bare repo at dir that, like GitHub, serves
// filtered fetches and lazy fetches of single objects.
func servingOrigin(t *testing.T, tmp, dir string, cloneFrom ...string) {
	t.Helper()
	if len(cloneFrom) > 0 {
		git(t, tmp, "clone", "--quiet", "--bare", cloneFrom[0], dir)
	} else {
		git(t, tmp, "init", "--quiet", "--bare", "-b", "main", dir)
	}
	git(t, dir, "config", "uploadpack.allowFilter", "true")
	git(t, dir, "config", "uploadpack.allowAnySHA1InWant", "true")
}

func (f *bloblessFixture) mirror() string {
	return filepath.Join(f.c.StateDir, "git", "alpha.git")
}

// assertNoBlob fails if the mirror holds blob locally (no lazy fetch).
func (f *bloblessFixture) assertNoBlob(t *testing.T, blob, why string) {
	t.Helper()
	cmd := exec.Command("git", "-C", f.mirror(), "cat-file", "-e", blob)
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1")
	if cmd.Run() == nil {
		t.Fatalf("mirror holds the historical blob %s: %s", blob, why)
	}
}

// A cold mirror fetches origin without blobs and pulls the delta bases
// a thin bundle needs on demand, so a large history never moves.
func TestPushColdMirrorIsBlobless(t *testing.T) {
	f := newBloblessFixture(t)
	if err := Push("alpha", f.work, "wt/edit", f.sha, "", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := git(t, f.origin, "rev-parse", "refs/heads/wt/edit"); got != f.sha {
		t.Fatalf("origin has %s, want %s", got, f.sha)
	}
	f.assertNoBlob(t, f.old, "the cold fetch was not blobless")
}

// The fetch op on a cold mirror also stays blobless: a bundle thin
// against the caller's commit pulls only the new blobs from origin.
func TestFetchColdMirrorIsBlobless(t *testing.T) {
	f := newBloblessFixture(t)
	seed := filepath.Join(f.tmp, "seed")
	git(t, f.tmp, "clone", "--quiet", f.origin, seed)
	writeFile(t, filepath.Join(seed, "big.txt"), f.body+"v2\nupstream\n")
	git(t, seed, "commit", "--quiet", "-am", "main moves")
	git(t, seed, "push", "--quiet", "origin", "main")

	if err := Fetch("alpha", f.work, "main", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if got, want := git(t, f.work, "rev-parse", "refs/remotes/origin/main"), git(t, f.origin, "rev-parse", "main"); got != want {
		t.Fatalf("fetched %s, want %s", got, want)
	}
	git(t, f.work, "cat-file", "-e", "refs/remotes/origin/main:big.txt")
	f.assertNoBlob(t, f.old, "the cold fetch op was not blobless")
}

// A mirror filled by a full fetch before it went blobless keeps its
// objects and takes the next push.
func TestPushConvertsFullMirror(t *testing.T) {
	f := newBloblessFixture(t)
	m := f.mirror()
	git(t, f.tmp, "init", "--quiet", "--bare", m)
	git(t, m, "fetch", "--quiet", "--no-tags", f.origin, "+refs/heads/*:refs/remotes/origin/*")
	if err := Push("alpha", f.work, "wt/edit", f.sha, "", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := git(t, f.origin, "rev-parse", "refs/heads/wt/edit"); got != f.sha {
		t.Fatalf("origin has %s, want %s", got, f.sha)
	}
	if got := git(t, m, "config", "extensions.partialClone"); got != "origin" {
		t.Fatalf("extensions.partialClone = %q, want origin", got)
	}
	cmd := exec.Command("git", "-C", m, "cat-file", "-e", f.old)
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1")
	if err := cmd.Run(); err != nil {
		t.Fatalf("the full mirror lost the blob %s it already held: %v", f.old, err)
	}
	git(t, m, "fsck", "--connectivity-only")
}

// Lazy fetches follow the project's current repo URL: a mirror made
// against an old origin pulls its missing delta bases from the new one.
func TestPushLazyFetchFollowsRepoURL(t *testing.T) {
	f := newBloblessFixture(t)
	if err := Fetch("alpha", f.work, "main", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	git(t, f.mirror(), "rev-parse", "--verify", "refs/remotes/origin/main")
	f.assertNoBlob(t, git(t, f.work, "rev-parse", "main:big.txt"), "the base blob must be lazy-fetched for this test")

	moved := filepath.Join(f.tmp, "moved.git")
	servingOrigin(t, f.tmp, moved, f.origin)
	if err := os.RemoveAll(f.origin); err != nil {
		t.Fatal(err)
	}
	f.s.remote = func(string) string { return moved }
	if err := Push("alpha", f.work, "wt/edit", f.sha, "", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := git(t, moved, "rev-parse", "refs/heads/wt/edit"); got != f.sha {
		t.Fatalf("moved origin has %s, want %s", got, f.sha)
	}
}
