package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/versality/spore/internal/ghapp"
)

// The GitHub side: the App key and its tokens stay in the broker. The
// role-loop mirror asks for the few gh calls it makes and for pushes
// of task branches; nothing else reaches GitHub as the App.

var shaRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

const githubTimeout = 2 * time.Minute

func (s *Server) githubConfig(project string) (ghapp.Config, error) {
	dir := filepath.Join(s.cfg.SecretsDir, project)
	b, err := os.ReadFile(filepath.Join(dir, "github.toml"))
	if err != nil {
		return ghapp.Config{}, fmt.Errorf("github.toml for %s: %w", project, err)
	}
	cfg, err := ghapp.ParseConfig(string(b))
	if err != nil {
		return ghapp.Config{}, fmt.Errorf("github.toml for %s: %w", project, err)
	}
	if cfg.PrivateKey != "" && !filepath.IsAbs(cfg.PrivateKey) && !strings.HasPrefix(cfg.PrivateKey, "~/") {
		cfg.PrivateKey = filepath.Join(dir, cfg.PrivateKey)
	}
	if cfg.AppID == 0 || cfg.InstallationID == 0 || cfg.PrivateKey == "" {
		return ghapp.Config{}, fmt.Errorf("github.toml for %s: app_id, installation_id and private_key are required", project)
	}
	return cfg, nil
}

// token returns a cached installation token with at least five minutes
// left, minting a new one when needed. Tokens live only in memory.
func (s *Server) token(ctx context.Context, project string) (string, error) {
	if s.mint != nil {
		return s.mint(ctx, project)
	}
	s.mu.Lock()
	t, ok := s.tokens[project]
	s.mu.Unlock()
	if ok && t.ExpiresAt.After(s.now().Add(5*time.Minute)) {
		return t.Token, nil
	}
	cfg, err := s.githubConfig(project)
	if err != nil {
		return "", err
	}
	t, err = ghapp.Minter{Config: cfg, Now: s.Now}.Mint(ctx)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.tokens[project] = t
	s.mu.Unlock()
	return t.Token, nil
}

// githubProject checks the caller may act on req.Project's GitHub repo
// and returns that repo as HOST/OWNER/REPO.
func (s *Server) githubProject(c Caller, req Request) (string, error) {
	p, ok := s.cfg.Projects[req.Project]
	if !ok {
		return "", fmt.Errorf("unknown project %q", req.Project)
	}
	if p.Repo == "" {
		return "", fmt.Errorf("project %q has no repo configured for GitHub calls", req.Project)
	}
	return p.Repo, nil
}

func (s *Server) githubEnv(ctx context.Context, project, repo, home string) ([]string, error) {
	tok, err := s.token(ctx, project)
	if err != nil {
		return nil, err
	}
	base := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "LANG=C.UTF-8",
		"GH_CONFIG_DIR=" + filepath.Join(home, "gh"), "GH_REPO=" + repo,
		"GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	return ghapp.Env(base, tok), nil
}

// ghCallEnv is the environment for a brokered gh call. The token goes
// in a hosts.yml under home (a 0700 MkdirTemp dir), not in GH_TOKEN, so
// nothing gh evaluates can read it back through the environment.
func (s *Server) ghCallEnv(ctx context.Context, project, repo, home string) ([]string, error) {
	tok, err := s.token(ctx, project)
	if err != nil {
		return nil, err
	}
	host, _, _ := strings.Cut(repo, "/")
	dir := filepath.Join(home, "gh")
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	// version "1" marks the config as migrated; without it gh would try
	// to move the token into a keyring.
	cfg := "version: \"1\"\n"
	if s.ghSocket != "" {
		cfg += "http_unix_socket: " + strconv.Quote(s.ghSocket) + "\n"
	}
	h, t := strconv.Quote(host), strconv.Quote(tok)
	hosts := h + ":\n    users:\n        x-access-token:\n            oauth_token: " + t +
		"\n    oauth_token: " + t + "\n    user: x-access-token\n    git_protocol: https\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(cfg), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte(hosts), 0o600); err != nil {
		return nil, err
	}
	return []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "LANG=C.UTF-8",
		"GH_CONFIG_DIR=" + dir, "GH_REPO=" + repo,
		"GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
	}, nil
}

func (s *Server) gh(c Caller, req Request) Response {
	repo, err := s.githubProject(c, req)
	if err != nil {
		return Response{Error: "refused: " + err.Error()}
	}
	r := s.redactor(req.Project)
	stdin, args, hits := r.GHCall(req.Stdin, req.Args)
	if err := checkGHArgs(args, stdin, repo); err != nil {
		return Response{Error: "refused: gh " + err.Error()}
	}
	home, err := os.MkdirTemp("", "spore-secrets-gh-")
	if err != nil {
		return Response{Error: err.Error()}
	}
	defer os.RemoveAll(home)
	ctx, cancel := context.WithTimeout(context.Background(), githubTimeout)
	defer cancel()
	env, err := s.ghCallEnv(ctx, req.Project, repo, home)
	if err != nil {
		return Response{Error: err.Error()}
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir, cmd.Env, cmd.WaitDelay = home, env, 2*time.Second
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	stdout, stderr := &capBuffer{max: maxOutput}, &capBuffer{max: maxOutput}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	resp := Response{Redactions: hits}
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			resp.Exit = exit.ExitCode()
		} else {
			resp.Exit, resp.Error = 127, err.Error()
		}
	}
	resp.Stdout, resp.Stderr = r.String(stdout.String()), r.String(stderr.String())
	resp.Truncated = stdout.cut || stderr.cut
	return resp
}

// ghFlags are the gh flags the mirror uses, and whether each takes a
// value. Anything else is refused, so a caller cannot point gh at a
// file (`-F x=@path`, `--input path`) or another repo (`--repo`). Never
// add a flag that evaluates a caller expression (`--jq`/`-q`,
// `--template`/`-t`): it runs inside the token-holding gh process.
// Callers filter the JSON themselves.
var ghFlags = map[string]bool{
	"--head": true, "--state": true, "--json": true, "--limit": true,
	"--undo": false, "--add-assignee": true, "--assignee": true, "--title": true,
	"--body": true, "--body-file": true, "--draft": false, "--base": true,
	"--method": true, "-X": true, "--input": true, "--paginate": false,
	"--hostname": true, "-f": true, "--raw-field": true, "-F": true, "--field": true,
	"--required": false,
}

var ghPRVerbs = map[string]bool{"list": true, "view": true, "ready": true, "edit": true, "create": true, "comment": true, "checks": true}

// checkGHArgs allows the shapes the role-loop mirror sends: `gh pr
// list|view|ready|edit|create|comment`, plus `gh pr checks` and
// `gh api` GETs of a commit's CI status for coordinators, `gh api` on
// the repo's own pulls or issues, and `gh api graphql` with one of
// graphqlQueries. stdin is what gh reads for `--input -`.
func checkGHArgs(args []string, stdin, repo string) error {
	if len(args) < 2 {
		return fmt.Errorf("needs a subcommand")
	}
	host, ownerRepo, _ := strings.Cut(repo, "/")
	// CI status of a commit: read-only, so no body and no method but GET.
	commit := false
	switch args[0] {
	case "pr":
		if !ghPRVerbs[args[1]] {
			return fmt.Errorf("pr %s is not allowed", args[1])
		}
	case "api":
		ep := args[1]
		prefix := `^repos/` + regexp.QuoteMeta(ownerRepo)
		commit = regexp.MustCompile(prefix + `/commits/[0-9a-f]{7,40}(/(check-runs|check-suites|status|statuses))?$`).MatchString(ep)
		if ep != "graphql" && !commit && !regexp.MustCompile(prefix+`/(pulls|issues)/[0-9]+(/[a-z_]+)?$`).MatchString(ep) {
			return fmt.Errorf("api %s is not allowed", ep)
		}
	default:
		return fmt.Errorf("%s is not allowed", args[0])
	}
	graphql := args[0] == "api" && args[1] == "graphql"
	fields, input := map[string]string{}, false
	draft := false
	for i := 2; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name, val, inline := strings.Cut(a, "=")
		takes, ok := ghFlags[name]
		if !ok {
			return fmt.Errorf("flag %s is not allowed", name)
		}
		if takes && !inline {
			if i+1 >= len(args) {
				return fmt.Errorf("flag %s needs a value", name)
			}
			i++
			val = args[i]
		}
		switch name {
		case "--draft":
			draft = true
		case "--body-file", "--input":
			if val != "-" {
				return fmt.Errorf("%s must be - (stdin)", name)
			}
			input = input || name == "--input"
		case "-F", "--field":
			// Under pr, -F is --body-file.
			if args[0] == "pr" && val != "-" {
				return fmt.Errorf("pr -F must be - (stdin)")
			}
			if _, v, _ := strings.Cut(val, "="); strings.HasPrefix(v, "@") {
				return fmt.Errorf("%s must not read a file", name)
			}
			if err := addField(fields, graphql, val); err != nil {
				return err
			}
		case "-f", "--raw-field":
			if err := addField(fields, graphql, val); err != nil {
				return err
			}
		case "--hostname":
			if val != host {
				return fmt.Errorf("--hostname %s is not the project's host", val)
			}
		case "--method", "-X":
			if m := strings.ToUpper(val); m != "GET" && (m != "POST" || commit) {
				return fmt.Errorf("--method %s is not allowed", val)
			}
		}
		if commit && (name == "-f" || name == "--raw-field" || name == "-F" || name == "--field" || name == "--input") {
			return fmt.Errorf("api on commits is read-only: %s is not allowed", name)
		}
	}
	if args[0] == "pr" && args[1] == "create" && !draft {
		return fmt.Errorf("pr create must be --draft")
	}
	if !graphql {
		return nil
	}
	query, hasQuery := fields["query"]
	delete(fields, "query")
	if input {
		if hasQuery || len(fields) > 0 {
			return fmt.Errorf("graphql takes --input - or fields, not both")
		}
		var err error
		if query, fields, err = graphqlBody(stdin); err != nil {
			return err
		}
	} else if !hasQuery {
		return fmt.Errorf("graphql needs a query")
	}
	return checkGraphQL(query, fields, ownerRepo)
}

// addField records a graphql call's key=value field. A repeated key is
// refused rather than guessing which value gh sends.
func addField(fields map[string]string, graphql bool, kv string) error {
	if !graphql {
		return nil
	}
	k, v, ok := strings.Cut(kv, "=")
	if !ok {
		return fmt.Errorf("graphql field %q needs key=value", kv)
	}
	if _, dup := fields[k]; dup {
		return fmt.Errorf("graphql field %q is repeated", k)
	}
	fields[k] = v
	return nil
}

func (s *Server) projectGit(project string) (string, func(), error) {
	s.mu.Lock()
	m, ok := s.gitMu[project]
	if !ok {
		m = &sync.Mutex{}
		s.gitMu[project] = m
	}
	s.mu.Unlock()
	m.Lock()
	dir := filepath.Join(s.cfg.StateDir, "git", project+".git")
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
			m.Unlock()
			return "", nil, err
		}
		if out, err := exec.Command("git", "init", "--quiet", "--bare", dir).CombinedOutput(); err != nil {
			m.Unlock()
			return "", nil, fmt.Errorf("git init: %v: %s", err, out)
		}
	}
	return dir, m.Unlock, nil
}

func (s *Server) remoteURL(repo string) string {
	if s.remote != nil {
		return s.remote(repo)
	}
	return "https://" + repo + ".git"
}

// promisor makes the mirror a blobless partial clone of origin. A cold
// full fetch of a history with tens of GB of blobs outruns
// githubTimeout; commits and trees stay small, and git fetches
// the few blobs a thin bundle or a push needs on demand from origin
// with the App credentials in env. Existing full mirrors keep their
// objects.
func (s *Server) promisor(ctx context.Context, dir string, env []string, repo string) error {
	for _, kv := range [][2]string{
		{"core.repositoryformatversion", "1"},
		{"extensions.partialClone", "origin"},
		{"remote.origin.url", s.remoteURL(repo)},
		{"remote.origin.promisor", "true"},
		{"remote.origin.partialclonefilter", "blob:none"},
	} {
		if _, err := gitIn(ctx, dir, env, "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}

// noLazy is env for a check that reads only commits, which a blobless
// mirror holds once fetched. A commit it lacks must fail fast: a lazy
// fetch sends origin no haves, so it pulls every commit and tree
// behind that commit, ahead of the single batched fetch that follows.
func noLazy(env []string) []string {
	return append(env[:len(env):len(env)], "GIT_NO_LAZY_FETCH=1")
}

func gitIn(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env, cmd.WaitDelay = env, 2*time.Second
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// push takes a bundle of the caller's task branch, checks it holds
// exactly SHA, and pushes SHA to the branch as the App. Only branches
// matching the project's push_branch, never with a bare force: a
// rewrite must name the commit it replaces (Lease).
func (s *Server) push(c Caller, req Request, bundle []byte) Response {
	repo, err := s.githubProject(c, req)
	if err != nil {
		return Response{Error: "refused: " + err.Error()}
	}
	if !s.cfg.pushBranchRE(req.Project).MatchString(req.Branch) {
		return Response{Error: fmt.Sprintf("refused: branch %q does not match the project's push_branch", req.Branch)}
	}
	if !shaRE.MatchString(req.SHA) || (req.Lease != "" && !shaRE.MatchString(req.Lease)) {
		return Response{Error: "refused: sha and lease must be full commit ids"}
	}
	if len(bundle) == 0 {
		return Response{Error: "push needs a bundle"}
	}
	dir, unlock, err := s.projectGit(req.Project)
	if err != nil {
		return Response{Error: err.Error()}
	}
	defer unlock()
	home, err := os.MkdirTemp("", "spore-secrets-push-")
	if err != nil {
		return Response{Error: err.Error()}
	}
	defer os.RemoveAll(home)
	ctx, cancel := context.WithTimeout(context.Background(), githubTimeout)
	defer cancel()
	env, err := s.githubEnv(ctx, req.Project, repo, home)
	if err != nil {
		return Response{Error: err.Error()}
	}
	bpath := filepath.Join(home, "push.bundle")
	if err := os.WriteFile(bpath, bundle, 0o600); err != nil {
		return Response{Error: err.Error()}
	}
	if err := s.promisor(ctx, dir, env, repo); err != nil {
		return Response{Error: err.Error()}
	}
	// Fetch on every push, not only when the bundle fails to verify:
	// git push excludes only origin tips the mirror holds, so a mirror
	// behind a moved base would pack, and lazy-fetch, the whole history.
	if _, err := gitIn(ctx, dir, env, "fetch", "--quiet", "--no-tags", "origin", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return Response{Error: s.redactor(req.Project).String(err.Error())}
	}
	if _, err := gitIn(ctx, dir, noLazy(env), "bundle", "verify", "--quiet", bpath); err != nil {
		return Response{Error: "bundle prerequisites missing", NeedFull: true}
	}
	incoming := "refs/spore/incoming"
	if _, err := gitIn(ctx, dir, env, "fetch", "--quiet", "--no-tags", bpath, "+refs/heads/"+req.Branch+":"+incoming); err != nil {
		return Response{Error: err.Error()}
	}
	got, err := gitIn(ctx, dir, env, "rev-parse", incoming)
	if err != nil {
		return Response{Error: err.Error()}
	}
	// The mirror pushes the commit the check gate cleared, which may
	// trail the branch tip the bundle carries.
	if got != req.SHA {
		if _, err := gitIn(ctx, dir, noLazy(env), "merge-base", "--is-ancestor", req.SHA, incoming); err != nil {
			return Response{Error: fmt.Sprintf("%s is not in the bundle (tip %s)", req.SHA, got)}
		}
	}
	ref := "refs/heads/" + req.Branch
	args := []string{"push", "--quiet", "--no-verify", s.remoteURL(repo), req.SHA + ":" + ref}
	if req.Lease != "" {
		args = append(args, "--force-with-lease="+ref+":"+req.Lease)
	}
	if _, err := gitIn(ctx, dir, env, args...); err != nil {
		return Response{Error: s.redactor(req.Project).String(err.Error())}
	}
	return Response{Stdout: fmt.Sprintf("pushed %s to %s\n", req.SHA[:12], req.Branch)}
}

var branchRE = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// fetch returns a bundle of origin's Branch, thin against Have when
// the broker's mirror holds it. Reading needs no extra scoping.
func (s *Server) fetch(c Caller, req Request) (Response, []byte) {
	repo, err := s.githubProject(c, req)
	if err != nil {
		return Response{Error: "refused: " + err.Error()}, nil
	}
	if !branchRE.MatchString(req.Branch) || strings.Contains(req.Branch, "..") || strings.HasPrefix(req.Branch, "-") {
		return Response{Error: fmt.Sprintf("refused: bad branch %q", req.Branch)}, nil
	}
	dir, unlock, err := s.projectGit(req.Project)
	if err != nil {
		return Response{Error: err.Error()}, nil
	}
	defer unlock()
	home, err := os.MkdirTemp("", "spore-secrets-fetch-")
	if err != nil {
		return Response{Error: err.Error()}, nil
	}
	defer os.RemoveAll(home)
	ctx, cancel := context.WithTimeout(context.Background(), githubTimeout)
	defer cancel()
	env, err := s.githubEnv(ctx, req.Project, repo, home)
	if err != nil {
		return Response{Error: err.Error()}, nil
	}
	if err := s.promisor(ctx, dir, env, repo); err != nil {
		return Response{Error: err.Error()}, nil
	}
	ref := "refs/remotes/origin/" + req.Branch
	if _, err := gitIn(ctx, dir, env, "fetch", "--quiet", "--no-tags", "origin", "+refs/heads/"+req.Branch+":"+ref); err != nil {
		return Response{Error: s.redactor(req.Project).String(err.Error())}, nil
	}
	tip, err := gitIn(ctx, dir, env, "rev-parse", ref)
	if err != nil {
		return Response{Error: err.Error()}, nil
	}
	data, _ := json.Marshal(map[string]string{"sha": tip})
	if req.Have == tip {
		return Response{Data: data}, nil
	}
	args := []string{"bundle", "create", "--quiet", filepath.Join(home, "fetch.bundle"), ref}
	if shaRE.MatchString(req.Have) {
		if _, err := gitIn(ctx, dir, noLazy(env), "merge-base", "--is-ancestor", req.Have, tip); err == nil {
			args = append(args, "^"+req.Have)
		}
	}
	if _, err := gitIn(ctx, dir, env, args...); err != nil {
		return Response{Error: err.Error()}, nil
	}
	b, err := os.ReadFile(filepath.Join(home, "fetch.bundle"))
	if err != nil {
		return Response{Error: err.Error()}, nil
	}
	return Response{Data: data}, b
}
