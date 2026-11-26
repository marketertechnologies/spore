package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/versality/spore/internal/events"
	"github.com/versality/spore/internal/ghapp"
	"github.com/versality/spore/internal/secrets"
	"github.com/versality/spore/internal/task"
)

const ghUsage = `spore gh - act on GitHub as the project's GitHub App

Usage:
  spore gh pr <verb> [args...]
  spore gh api <path> [args...]
  spore gh push <branch> [--force-with-lease=<sha>]
  spore gh config [--set key=value ...]
  spore gh token
  spore gh exec -- <cmd> [args...]

Subcommands:
  pr       Run gh pr <verb> as the app (create --draft, view, list,
           comment, edit, ready). Pass a body on stdin with
           --body-file -.
  api      Run gh api <path> as the app.
  push     Push the local branch from the cwd repo (worktrees too) to
           origin as the app and set its upstream. A rewritten branch
           needs --force-with-lease=<sha>, the commit origin's branch
           is at; there is no bare force.
  config   Print the host-side github.toml
           ($XDG_STATE_HOME/spore/<project>/github.toml, 0600). --set
           writes a key (repeatable); an empty value deletes it. Keys:
           pr_flow ("draft" or "off"), app_id, installation_id,
           private_key, operator_login, commit_name, commit_email,
           api_url.
  token    Print an installation token (cached host-side until ~5 min
           before expiry). For humans: run from an agent session, the
           token lands in its transcript. Agents use pr, api and push
           instead, e.g. spore gh push <branch>.
  exec     Run <cmd> with GH_TOKEN set and a git credential helper
           injected, so gh and git push over https act as the app.
           Refuses when the cwd repo's origin is not https.

On a host running the secrets broker, pr, api and push go through it
and its allowlist applies: pr list|view|ready|edit|create --draft|
comment, api on the repo's own pulls and issues or a read-only GraphQL
query, and pushes of branches matching the project's push_branch
(default ^wt/...). token and exec refuse there: the App key is not
readable (docs/secrets.md).

See docs/github-app.md for the one-time app setup.
`

func runGH(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, ghUsage)
		return 2
	}
	var err error
	code := 0
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Print(ghUsage)
		return 0
	case "config":
		err = runGHConfig(args[1:])
	case "token":
		err = runGHToken(args[1:])
	case "exec":
		code, err = runGHExec(args[1:])
	case "pr", "api":
		code, err = runGHPassthrough(args[0], args[1:])
	case "push":
		code, err = runGHPush(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "spore gh: unknown subcommand %q\n\n%s", args[0], ghUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore gh:", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

func ghProjectRoot() (string, error) {
	return task.MainRepoRoot(os.Getenv("SPORE_PROJECT_ROOT"))
}

func runGHConfig(args []string) error {
	var sets []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--set" && i+1 < len(args):
			sets = append(sets, args[i+1])
			i++
		case strings.HasPrefix(a, "--set="):
			sets = append(sets, strings.TrimPrefix(a, "--set="))
		default:
			return fmt.Errorf("usage: spore gh config [--set key=value ...]")
		}
	}
	root, err := ghProjectRoot()
	if err != nil {
		return err
	}
	for _, kv := range sets {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("--set %q: want key=value", kv)
		}
		path, err := ghapp.SetConfigKey(root, strings.TrimSpace(k), strings.TrimSpace(v))
		if err != nil {
			return err
		}
		fmt.Printf("set %s in %s\n", strings.TrimSpace(k), path)
	}
	if len(sets) > 0 {
		return nil
	}
	path, err := ghapp.ConfigPath(root)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Printf("# %s (absent: pr_flow off)\n", path)
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := ghapp.ParseConfig(string(b)); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	fmt.Printf("# %s\n%s", path, b)
	return nil
}

// errBrokered refuses token and exec on a host whose App key is held
// by the secrets broker: the role loop pushes through the broker, and
// agents never push by hand.
var errBrokered = errors.New("the GitHub App key is held by the secrets broker on this host; the role loop pushes and opens PRs through it, and agents do not push by hand (docs/secrets.md)")

func runGHToken(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("usage: spore gh token")
	}
	if secrets.Available() {
		return errBrokered
	}
	root, err := ghProjectRoot()
	if err != nil {
		return err
	}
	tok, err := ghapp.ProjectToken(context.Background(), root)
	if err != nil {
		return err
	}
	fmt.Println(tok.Token)
	return nil
}

func runGHExec(args []string) (int, error) {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return 0, fmt.Errorf("usage: spore gh exec -- <cmd> [args...]")
	}
	if secrets.Available() {
		return 0, errBrokered
	}
	if out, err := exec.Command("git", "config", "--get", "remote.origin.url").Output(); err == nil {
		if err := ghapp.CheckHTTPSRemote(strings.TrimSpace(string(out))); err != nil {
			return 0, err
		}
	}
	root, err := ghProjectRoot()
	if err != nil {
		return 0, err
	}
	tok, err := ghapp.ProjectToken(context.Background(), root)
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = ghapp.Env(os.Environ(), tok.Token)
	var out bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if isPRCreate(args) {
		cmd.Stdout = io.MultiWriter(os.Stdout, &out)
	}
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), nil
		}
		return 0, err
	}
	recordPRCreate(root, args, out.String())
	return 0, nil
}

const ghBrokerTimeout = 3 * time.Minute

// runGHPassthrough runs `gh <sub> args...` as the app: through the
// broker when its socket is present, else as exec does.
func runGHPassthrough(sub string, args []string) (int, error) {
	if len(args) == 0 {
		return 2, fmt.Errorf("usage: spore gh %s <%s> [args...]", sub, map[string]string{"pr": "verb", "api": "path"}[sub])
	}
	ghArgs := append([]string{sub}, args...)
	if !secrets.Available() {
		return runGHExec(append([]string{"gh"}, ghArgs...))
	}
	stdin := ""
	if readsStdin(ghArgs) {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<20+1))
		if err != nil {
			return 0, err
		}
		if len(b) > 4<<20 {
			return 0, fmt.Errorf("stdin over 4 MiB")
		}
		stdin = string(b)
	}
	req := secrets.Request{}
	secrets.Claim(&req)
	resp, err := secrets.GHRaw(req.Project, stdin, ghArgs, ghBrokerTimeout)
	if err != nil {
		return 0, err
	}
	fmt.Fprint(os.Stdout, resp.Stdout)
	fmt.Fprint(os.Stderr, resp.Stderr)
	if resp.Truncated {
		fmt.Fprintln(os.Stderr, "spore gh: output truncated by the secrets broker")
	}
	if resp.Exit == 0 {
		if root, err := ghProjectRoot(); err == nil {
			recordPRCreate(root, append([]string{"gh"}, ghArgs...), resp.Stdout)
		}
	}
	return resp.Exit, nil
}

// readsStdin reports whether gh args take a body from stdin, the only
// file the broker lets gh read.
func readsStdin(args []string) bool {
	for i, a := range args {
		switch a {
		case "--body-file=-", "--input=-":
			return true
		case "--body-file", "--input", "-F":
			if i+1 < len(args) && args[i+1] == "-" {
				return true
			}
		}
	}
	return false
}

const ghPushUsage = "usage: spore gh push <branch> [--force-with-lease=<sha>]"

func runGHPush(args []string) (int, error) {
	branch, lease := "", ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case strings.HasPrefix(a, "--force-with-lease="):
			if lease = strings.TrimPrefix(a, "--force-with-lease="); lease == "" {
				return 2, fmt.Errorf("--force-with-lease needs =<sha>")
			}
		case a == "--force-with-lease" && i+1 < len(args):
			lease = args[i+1]
			i++
		case strings.HasPrefix(a, "-") || strings.HasPrefix(a, "+"):
			return 2, fmt.Errorf("%s is refused: a rewrite must name the commit it replaces (--force-with-lease=<sha>); %s", a, ghPushUsage)
		case branch == "":
			branch = a
		default:
			return 2, errors.New(ghPushUsage)
		}
	}
	if branch == "" {
		return 2, errors.New(ghPushUsage)
	}
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return 0, fmt.Errorf("not inside a git repo")
	}
	dir := strings.TrimSpace(string(out))
	sha, leaseSHA, err := secrets.ResolvePush(dir, branch, lease)
	if err != nil {
		return 0, err
	}
	ref := "refs/heads/" + branch
	if !secrets.Available() {
		gitArgs := []string{"git", "-C", dir, "push", "-u"}
		if leaseSHA != "" {
			gitArgs = append(gitArgs, "--force-with-lease="+ref+":"+leaseSHA)
		}
		return runGHExec(append(gitArgs, "origin", ref+":"+ref))
	}
	root, err := ghProjectRoot()
	if err != nil {
		return 0, err
	}
	name, err := task.ProjectName(root)
	if err != nil {
		return 0, err
	}
	if err := secrets.Push(name, dir, branch, sha, leaseSHA, ghBrokerTimeout); err != nil {
		return 0, err
	}
	// The broker pushed from its own mirror: record what origin now has
	// so status and the next thin bundle see it, as git push -u would.
	if exec.Command("git", "-C", dir, "update-ref", "refs/remotes/origin/"+branch, sha).Run() == nil {
		_ = exec.Command("git", "-C", dir, "branch", "--set-upstream-to=origin/"+branch, branch).Run()
	}
	fmt.Printf("pushed %s to %s\n", sha[:12], branch)
	return 0, nil
}

// brokerFetch is task.BrokerFetch: the base fetch through the secrets
// broker, for hosts where this process holds no GitHub credential.
func brokerFetch(projectRoot, branch string) error {
	if !secrets.Available() {
		return task.ErrNoFetchAuth
	}
	name, err := task.ProjectName(projectRoot)
	if err != nil {
		return err
	}
	return secrets.Fetch(name, projectRoot, branch, 2*time.Minute)
}

var prURLRE = regexp.MustCompile(`https://[^\s/]+/[^\s/]+/[^\s/]+/pull/[0-9]+`)

// recordPRCreate writes pr.opened when args is a `gh pr create` and its
// output names the new PR.
func recordPRCreate(root string, args []string, stdout string) {
	if !isPRCreate(args) {
		return
	}
	urls := prURLRE.FindAllString(stdout, -1)
	if len(urls) == 0 {
		return
	}
	fields := map[string]string{"url": urls[len(urls)-1]}
	for _, a := range args[3:] {
		if a == "--draft" || a == "-d" {
			fields["draft"] = "true"
		}
	}
	task.RecordEvent(root, events.Event{Kind: events.PROpened, Slug: os.Getenv("SPORE_TASK_SLUG"), Fields: fields})
}

func isPRCreate(args []string) bool {
	return len(args) >= 3 && filepath.Base(args[0]) == "gh" && args[1] == "pr" && args[2] == "create"
}
