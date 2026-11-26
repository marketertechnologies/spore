package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/versality/spore/internal/hooks"
)

func runHooks(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, hooksUsage)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "--help", "help":
		fmt.Print(hooksUsage)
		return 0
	case "install":
		return runHooksInstall(rest)
	case "commit-msg":
		return runHooksCommitMsg(rest)
	case "pretooluse":
		return runHooksPreToolUse()
	case "stop":
		return runHooksStop()
	case "engineer-check":
		return runHooksEngineerCheck()
	case "settings":
		return runHooksSettings(rest)
	case "prune-settings":
		return runHooksPruneSettings(rest)
	case "codex-config":
		return runHooksCodexConfig(rest)
	case "watch-inbox":
		return runHooksWatchInbox(rest)
	case "notify-coordinator":
		return runHooksNotifyCoordinator(rest)
	case "stop-failure":
		return runHooksStopFailure()
	case "codex-session-start":
		return runHooksCodexSessionStart()
	default:
		fmt.Fprintf(os.Stderr, "spore hooks: unknown subcommand %q\n\n%s", sub, hooksUsage)
		return 2
	}
}

func runHooksInstall(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "spore hooks install: takes no args")
		return 2
	}
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore hooks install:", err)
		return 1
	}
	dir, err := hooks.Install(root, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore hooks install:", err)
		return 1
	}
	fmt.Println(dir)
	return 0
}

func runHooksCommitMsg(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "spore hooks commit-msg: usage: commit-msg <file>")
		return 2
	}
	if err := hooks.CommitMsg(args[0]); err != nil {
		fmt.Fprintln(os.Stderr, "spore hooks commit-msg:", err)
		return 1
	}
	return 0
}

func runHooksPreToolUse() int {
	req, err := readHookRequest()
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore hooks pretooluse:", err)
		return 1
	}
	resp := hooks.PreToolUse(req, hooks.DefaultForbidden())
	return writeHookResponse(resp)
}

func runHooksStop() int {
	req, err := readHookRequest()
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore hooks stop:", err)
		return 1
	}
	resp := hooks.Stop(req)
	return writeHookResponse(resp)
}

// runHooksEngineerCheck leaves stdin unread: the hook needs only env,
// and the engineer may run it by hand from a shell with no payload.
func runHooksEngineerCheck() int {
	res, err := hooks.EngineerCheck(hooks.EngineerCheckInputFromEnv())
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore hooks engineer-check:", err)
		return 0
	}
	fmt.Fprint(os.Stderr, res.Stderr)
	return res.Code
}

func readHookRequest() (hooks.Request, error) {
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		return hooks.Request{}, fmt.Errorf("read stdin: %w", err)
	}
	var req hooks.Request
	if err := json.Unmarshal(body, &req); err != nil {
		return hooks.Request{}, fmt.Errorf("unmarshal request: %w", err)
	}
	return req, nil
}

func writeHookResponse(resp hooks.Response) int {
	enc, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore hooks: marshal response:", err)
		return 1
	}
	if _, err := os.Stdout.Write(enc); err != nil {
		fmt.Fprintln(os.Stderr, "spore hooks: write response:", err)
		return 1
	}
	os.Stdout.Write([]byte{'\n'})
	return 0
}

// settingsInput is the JSON schema read from stdin by `spore hooks settings`.
type settingsInput struct {
	Events map[string][]settingsInputBin `json:"events"`
}

type settingsInputBin struct {
	Command     string `json:"command"`
	Matcher     string `json:"matcher,omitempty"`
	Timeout     int    `json:"timeout,omitempty"`
	Async       bool   `json:"async,omitempty"`
	AsyncRewake bool   `json:"asyncRewake,omitempty"`
}

func runHooksSettings(args []string) int {
	fs := flag.NewFlagSet("settings", flag.ContinueOnError)
	managed := fs.Bool("managed", false, "emit the host-wide managed settings drop-in")
	spore := fs.String("spore", "spore", "spore command the managed hooks invoke")
	hooksDir := fs.String("hooks-dir", "/run/current-system/sw/share/spore/hooks", "directory holding block-bg-bash.pl and load-state-md.pl")
	marketplace := fs.String("marketplace", "", "register this directory as the spore plugin marketplace")
	managedFile := fs.String("managed-file", hooks.DefaultManagedSettingsPath, "skip bindings this managed settings file provides")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: spore hooks settings [--managed-file PATH] | --managed [--spore PATH] [--hooks-dir DIR] [--marketplace DIR]")
		return 2
	}
	if *managed {
		out, err := hooks.ManagedSettings(*spore, *hooksDir, *marketplace)
		if err != nil {
			fmt.Fprintln(os.Stderr, "spore hooks settings:", err)
			return 1
		}
		os.Stdout.Write(out)
		return 0
	}

	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore hooks settings:", err)
		return 1
	}
	var input settingsInput
	if err := json.Unmarshal(body, &input); err != nil {
		fmt.Fprintln(os.Stderr, "spore hooks settings: bad input:", err)
		return 1
	}
	events := make(map[string][]hooks.HookBin, len(input.Events))
	for name, bins := range input.Events {
		for _, b := range bins {
			events[name] = append(events[name], hooks.HookBin{
				BinPath:     b.Command,
				Matcher:     b.Matcher,
				Timeout:     b.Timeout,
				Async:       b.Async,
				AsyncRewake: b.AsyncRewake,
			})
		}
	}
	if *managedFile != "" {
		set, ok, err := hooks.LoadManagedSet(*managedFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "spore hooks settings:", err)
			return 1
		}
		if ok {
			var dropped []string
			events, dropped = hooks.FilterManaged(events, set)
			for _, d := range dropped {
				fmt.Fprintf(os.Stderr, "spore hooks settings: %s runs from %s; left out\n", d, *managedFile)
			}
		}
	}
	perms := hooks.DefaultPermissions()
	if root, err := repoRoot(); err == nil {
		if perms, err = hooks.LoadPermissions(root); err != nil {
			fmt.Fprintln(os.Stderr, "spore hooks settings:", err)
			return 1
		}
	}
	out, err := hooks.SettingsWith(events, perms)
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore hooks settings:", err)
		return 1
	}
	os.Stdout.Write(out)
	return 0
}

func runHooksPruneSettings(args []string) int {
	fs := flag.NewFlagSet("prune-settings", flag.ContinueOnError)
	file := fs.String("file", "", "settings.json to prune")
	managedFile := fs.String("managed-file", hooks.DefaultManagedSettingsPath, "managed settings file that provides the hooks")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *file == "" {
		fmt.Fprintln(os.Stderr, "usage: spore hooks prune-settings --file PATH [--managed-file PATH]")
		return 2
	}
	set, ok, err := hooks.LoadManagedSet(*managedFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore hooks prune-settings:", err)
		return 1
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "spore hooks prune-settings: %s not found; nothing provides the hooks, refusing to prune\n", *managedFile)
		return 1
	}
	removed, backup, err := hooks.PruneSettingsFile(*file, set, time.Now())
	if errors.Is(err, hooks.ErrSymlinkSettings) {
		fmt.Printf("%v; left as is\n", err)
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore hooks prune-settings:", err)
		return 1
	}
	if len(removed) == 0 {
		fmt.Printf("%s: nothing to prune\n", *file)
		return 0
	}
	fmt.Printf("%s: backed up to %s\n", *file, backup)
	for _, r := range removed {
		fmt.Printf("%s: removed %s\n", *file, r)
	}
	return 0
}

func runHooksCodexConfig(args []string) int {
	fs := flag.NewFlagSet("codex-config", flag.ContinueOnError)
	spore := fs.String("spore", "spore", "spore command the hooks invoke")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: spore hooks codex-config [--spore PATH]")
		return 2
	}
	out, err := hooks.CodexHooks(hooks.CodexUserEvents(*spore))
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore hooks codex-config:", err)
		return 1
	}
	os.Stdout.Write(out)
	return 0
}

func runHooksWatchInbox(args []string) int {
	if len(args) > 0 && args[0] == "--codex" {
		return runHooksWatchInboxCodex(args[1:])
	}
	if err := hooks.ClearAlerts(); err != nil {
		fmt.Fprintln(os.Stderr, "spore hooks watch-inbox: clear alerts:", err)
	}
	var err error
	switch len(args) {
	case 0:
		inbox := os.Getenv("SPORE_TASK_INBOX")
		if inbox == "" {
			// Host-level Stop hooks fire in every claude session,
			// including non-spore ones. No slug and no inbox env
			// means there is nothing to watch; exit silently so
			// the hook produces no stderr noise.
			return 0
		}
		err = hooks.WatchInboxAt(inbox)
	case 1:
		err = hooks.WatchInbox(args[0])
	default:
		fmt.Fprintln(os.Stderr, "usage: spore hooks watch-inbox [slug]")
		return 2
	}
	if err == hooks.ErrWake {
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore hooks watch-inbox:", err)
		return 1
	}
	return 0
}

func runHooksWatchInboxCodex(args []string) int {
	fs := flag.NewFlagSet("watch-inbox --codex", flag.ContinueOnError)
	pid := fs.Int("pid", 0, "exit when this process is gone")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: spore hooks watch-inbox --codex [--pid N]")
		return 2
	}
	inbox := os.Getenv("SPORE_TASK_INBOX")
	key := hooks.CodexSessionKey()
	if inbox == "" || key == "" {
		fmt.Fprintln(os.Stderr, "spore hooks watch-inbox --codex: needs $SPORE_TASK_INBOX and a session key ($SPORE_COORDINATOR_SESSION or tmux)")
		return 2
	}
	var alive func() bool
	if *pid > 0 {
		alive = func() bool { return syscall.Kill(*pid, 0) == nil }
	}
	// No signal trap: SIGTERM must end the sidecar at once, even while
	// it is parked in the inotify wait. A claim is an atomic rename, so
	// dying mid-loop leaves no partial state.
	if err := hooks.WatchInboxCodex(context.Background(), inbox, key, alive); err != nil {
		fmt.Fprintln(os.Stderr, "spore hooks watch-inbox --codex:", err)
		return 1
	}
	return 0
}

// runHooksCodexSessionStart never blocks the session: a non-spore codex
// session (no inbox, no key) or a bad payload is a silent no-op. Stdout
// carries only the context JSON; Codex fails the hook on any other
// JSON-looking output.
func runHooksCodexSessionStart() int {
	os.Stdout.Write(hooks.CodexStartOutput(hooks.CodexStartContext()))
	req, err := readHookRequest()
	if err != nil {
		return 0
	}
	inbox := os.Getenv("SPORE_TASK_INBOX")
	if inbox == "" {
		return 0
	}
	key := hooks.CodexSessionKey()
	if key == "" {
		return 0
	}
	if err := hooks.RecordCodexThread(inbox, key, req); err != nil {
		fmt.Fprintln(os.Stderr, "spore hooks codex-session-start:", err)
	}
	return 0
}

// runHooksStopFailure always exits 0: Claude Code ignores StopFailure
// output and exit codes, so errors only go to stderr for the debug log.
func runHooksStopFailure() int {
	var in hooks.StopFailureInput
	body, err := io.ReadAll(os.Stdin)
	if err == nil {
		err = json.Unmarshal(body, &in)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore hooks stop-failure: read payload:", err)
	}
	if err := hooks.StopFailure(in); err != nil {
		fmt.Fprintln(os.Stderr, "spore hooks stop-failure:", err)
	}
	return 0
}

func runHooksNotifyCoordinator(args []string) int {
	switch len(args) {
	case 0:
		if err := hooks.NotifyCoordinatorEnv(readHookPayload(os.Stdin, notifyPayloadTimeout)); err != nil {
			fmt.Fprintln(os.Stderr, "spore hooks notify-coordinator:", err)
			return 1
		}
		return 0
	case 1:
		if err := hooks.NotifyCoordinator(args[0]); err != nil {
			fmt.Fprintln(os.Stderr, "spore hooks notify-coordinator:", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintln(os.Stderr, "usage: spore hooks notify-coordinator [project]")
		return 2
	}
}

// notifyPayloadTimeout stays under the hook's 5 s timeout. Claude Code
// closes stdin after the payload; a manual call on a pipe that never
// closes gives up instead of hanging.
const notifyPayloadTimeout = 2 * time.Second

// readHookPayload returns the hook payload on f, or nil when f is a
// terminal or nothing arrives before EOF within timeout.
func readHookPayload(f *os.File, timeout time.Duration) io.Reader {
	if fi, err := f.Stat(); err != nil || fi.Mode()&os.ModeCharDevice != 0 {
		return nil
	}
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(f, 1<<20))
		done <- b
	}()
	select {
	case b := <-done:
		return bytes.NewReader(b)
	case <-time.After(timeout):
		return nil
	}
}

func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	cmd := exec.Command("git", "-c", "safe.directory="+wd, "rev-parse", "--show-toplevel")
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git rev-parse: %w (%s)", err, strings.TrimSpace(errBuf.String()))
	}
	return strings.TrimSpace(out.String()), nil
}
