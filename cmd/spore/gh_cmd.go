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

	"github.com/versality/spore/internal/events"
	"github.com/versality/spore/internal/ghapp"
	"github.com/versality/spore/internal/task"
)

const ghUsage = `spore gh - act on GitHub as the project's GitHub App

Usage:
  spore gh config [--set key=value ...]
  spore gh token
  spore gh exec -- <cmd> [args...]

Subcommands:
  config   Print the host-side github.toml
           ($XDG_STATE_HOME/spore/<project>/github.toml, 0600). --set
           writes a key (repeatable); an empty value deletes it. Keys:
           pr_flow ("draft" or "off"), app_id, installation_id,
           private_key, operator_login, commit_name, commit_email,
           api_url.
  token    Print an installation token (cached host-side until ~5 min
           before expiry). For humans: run from an agent session, the
           token lands in its transcript. Agents use exec instead,
           e.g. spore gh exec -- git push -u origin <branch>.
  exec     Run <cmd> with GH_TOKEN set and a git credential helper
           injected, so gh and git push over https act as the app.
           Refuses when the cwd repo's origin is not https.

See docs/github-app.md for the one-time app setup.
`

func runGH(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, ghUsage)
		return 2
	}
	var err error
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Print(ghUsage)
		return 0
	case "config":
		err = runGHConfig(args[1:])
	case "token":
		err = runGHToken(args[1:])
	case "exec":
		var code int
		code, err = runGHExec(args[1:])
		if err == nil {
			return code
		}
	default:
		fmt.Fprintf(os.Stderr, "spore gh: unknown subcommand %q\n\n%s", args[0], ghUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore gh:", err)
		return 1
	}
	return 0
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

func runGHToken(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("usage: spore gh token")
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
