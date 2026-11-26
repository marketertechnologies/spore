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
	"syscall"
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
           comment, edit, ready, checks). --body-file <path> (api: --input
           <path>) is read here, from the cwd repo or $TMPDIR only,
           and fed to gh on stdin. In an agent pane, run each call
           with no redirect, pipe or cd chain: those keep it in the
           sandbox, where the App key is unreadable.
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
comment|checks, api on the repo's own pulls and issues, a GET of a
commit's CI status (commits/<sha>[/check-runs|check-suites|status|
statuses]) or a read-only GraphQL query, and pushes of branches
matching the project's push_branch (default ^wt/...). token and exec
refuse there: the App key is not readable (docs/secrets.md).

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
// by the secrets broker.
var errBrokered = errors.New("the GitHub App key is held by the secrets broker on this host: push and act on PRs with the pr, api and push subcommands, which go through it (docs/secrets.md)")

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
	return runAsApp(args, os.Stdin)
}

// errSandboxedKey explains the usual cause of an unreadable App key in
// an agent pane: Claude Code takes `spore gh` out of the sandbox only
// when the call has no redirect, pipe or chain.
const errSandboxedKey = "this spore gh call ran inside the Claude Code sandbox, which cannot read the App key: run it as one command with no redirect, pipe or `cd ... &&` (pass a body with --body-file <path>)"

func runAsApp(args []string, stdin io.Reader) (int, error) {
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
		if os.Getenv("SANDBOX_RUNTIME") == "1" {
			return 0, fmt.Errorf("%w\n%s", err, errSandboxedKey)
		}
		return 0, err
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = ghapp.Env(os.Environ(), tok.Token)
	var out bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, os.Stdout, os.Stderr
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
	ghArgs, body, err := inlineBodyFile(append([]string{sub}, args...))
	if err != nil {
		return 2, err
	}
	if !secrets.Available() {
		var in io.Reader = os.Stdin
		if body != nil {
			in = bytes.NewReader(body)
		}
		return runAsApp(append([]string{"gh"}, ghArgs...), in)
	}
	stdin := string(body)
	if body == nil && readsStdin(ghArgs) {
		if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			return 2, fmt.Errorf("--body-file - reads stdin, which is a terminal: pass --body-file <path>")
		}
		b, err := io.ReadAll(io.LimitReader(os.Stdin, maxGHBody+1))
		if err != nil {
			return 0, err
		}
		if len(b) > maxGHBody {
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

const maxGHBody = 4 << 20

// inlineBodyFile reads a `--body-file <path>` (pr -F <path>, api
// --input <path>) here and rewrites it to `-`, returning the contents
// to feed gh on stdin. A redirect would keep the call in the sandbox,
// and the broker lets gh read only stdin. spore gh runs unsandboxed,
// so the path must resolve inside the cwd repo or $TMPDIR.
func inlineBodyFile(args []string) ([]string, []byte, error) {
	out := append([]string(nil), args...)
	var body []byte
	for i := 1; i < len(out); i++ {
		a := out[i]
		if field, ok := fieldValue(out, i); ok {
			if _, v, _ := strings.Cut(field, "="); strings.HasPrefix(v, "@") {
				return nil, nil, fmt.Errorf("%s: a field must not read a file", a)
			}
			continue
		}
		name, path, inline := strings.Cut(a, "=")
		switch {
		case name == "--body-file" || name == "--input":
		case out[0] == "pr" && a == "-F":
		case out[0] == "pr" && strings.HasPrefix(a, "-F"):
			return nil, nil, fmt.Errorf("%s: pass the body file as --body-file <path>", a)
		default:
			continue
		}
		if !inline {
			if i+1 >= len(out) {
				break
			}
			i++
			path = out[i]
		}
		if path == "-" {
			continue
		}
		if body != nil {
			return nil, nil, fmt.Errorf("more than one body file")
		}
		b, err := readBodyFile(path)
		if err != nil {
			return nil, nil, err
		}
		body = b
		if inline {
			out[i] = name + "=-"
		} else {
			out[i] = "-"
		}
	}
	return out, body, nil
}

// fieldValue returns the key=value of a gh api -F/--field at args[i],
// in any spelling; gh reads `@path` values from disk.
func fieldValue(args []string, i int) (string, bool) {
	if args[0] != "api" {
		return "", false
	}
	a := args[i]
	switch {
	case a == "-F" || a == "--field":
		if i+1 < len(args) {
			return args[i+1], true
		}
		return "", true
	case strings.HasPrefix(a, "--field="):
		return strings.TrimPrefix(a, "--field="), true
	case strings.HasPrefix(a, "-F"):
		return a[2:], true
	}
	return "", false
}

// readBodyFile opens the resolved path once with O_NOFOLLOW and checks
// the open file, so nothing can be swapped in between check and read.
func readBodyFile(path string) ([]byte, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("body file: %w", err)
	}
	if real, err = filepath.Abs(real); err != nil {
		return nil, err
	}
	if !insideAny(real, bodyRoots()) {
		return nil, fmt.Errorf("body file %s: must be inside the repo or $TMPDIR", path)
	}
	f, err := os.OpenFile(real, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("body file: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("body file: %w", err)
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxGHBody {
		return nil, fmt.Errorf("body file %s: want a regular file up to 4 MiB", path)
	}
	return io.ReadAll(io.LimitReader(f, maxGHBody))
}

// bodyTmpRoot bounds which $TMPDIR counts as a body root: the caller
// sets TMPDIR, and this process runs unsandboxed. Tests repoint it
// because nix builds run with TMPDIR=/build.
var bodyTmpRoot = "/tmp"

// bodyRoots are the cwd repo and $TMPDIR (only under bodyTmpRoot).
func bodyRoots() []string {
	var roots []string
	if out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		roots = append(roots, strings.TrimSpace(string(out)))
	}
	if tmp, err := filepath.EvalSymlinks(os.TempDir()); err == nil && insideAny(tmp, []string{bodyTmpRoot}) {
		roots = append(roots, tmp)
	}
	return roots
}

func insideAny(path string, roots []string) bool {
	for _, r := range roots {
		rr, err := filepath.EvalSymlinks(r)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(rr, path); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return true
		}
	}
	return false
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
	if dirRoot, err := task.MainRepoRoot(dir); err != nil || !sameDir(dirRoot, root) {
		return 0, fmt.Errorf("the cwd repo %s is not the project %s ($SPORE_PROJECT_ROOT): the broker would push it to that project's repo", dir, root)
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

func sameDir(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
