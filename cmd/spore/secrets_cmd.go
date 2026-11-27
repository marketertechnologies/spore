package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/versality/spore/internal/secrets"
)

const secretsUsage = `spore secrets - use project secrets without reading them

Usage:
  spore secrets list [--json]
  spore secrets run [--stdin] <profile> -- <command> [args...]
  spore secrets grant <task-slug> <profile> --for <duration> [--project <name>]
  spore secrets revoke <task-slug> [<profile>] [--project <name>]
  spore secrets grants
  spore secrets serve [--config <path>] [--socket <path>]
  spore secrets check [--config <path>]

Subcommands:
  list     Profiles this caller may use, with their commands. Never
           prints a value.
  run      Run one of a profile's allowed commands in the broker with
           the profile's variables injected. Only redacted output comes
           back. --stdin forwards this process's stdin (up to 4 MiB).
  grant    Give a worker one more profile for a limited time (at most
           7 days). Root or the pilot group only: sudo spore secrets
           grant ...
  revoke   Drop a task's grants (one profile, or all).
  grants   List live grants.
  serve    Run the broker (the NixOS module's spore-secrets.service).
  check    Validate a broker config the way serve does at start (names,
           regexps, ${VAR} references, known projects) without
           serving. Exit 1 on the first error.

The broker listens on $SPORE_SECRETS_SOCKET (default
/run/spore-secrets/broker.sock). See docs/secrets.md.
`

func runSecrets(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, secretsUsage)
		return 2
	}
	var err error
	code := 0
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Print(secretsUsage)
		return 0
	case "list":
		err = runSecretsList(args[1:])
	case "run":
		code, err = runSecretsRun(args[1:])
	case "grant":
		err = runSecretsGrant(args[1:])
	case "revoke":
		err = runSecretsRevoke(args[1:])
	case "grants":
		err = runSecretsGrants(args[1:])
	case "serve":
		err = runSecretsServe(args[1:])
	case "check":
		err = runSecretsCheck(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "spore secrets: unknown subcommand %q\n\n%s", args[0], secretsUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore secrets:", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

const secretsCallTimeout = 11 * time.Minute

func secretsCall(req secrets.Request) (secrets.Response, error) {
	secrets.Claim(&req)
	resp, _, err := secrets.Call(req, nil, secretsCallTimeout)
	if errors.Is(err, secrets.ErrUnavailable) {
		return resp, fmt.Errorf("no broker at %s (is spore-secrets.socket enabled on this host?)", secrets.SocketPath())
	}
	return resp, err
}

func runSecretsList(args []string) error {
	fs := flag.NewFlagSet("secrets list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resp, err := secretsCall(secrets.Request{Op: secrets.OpList})
	if err != nil {
		return err
	}
	if *asJSON {
		fmt.Println(string(resp.Data))
		return nil
	}
	var infos []secrets.ProfileInfo
	if err := json.Unmarshal(resp.Data, &infos); err != nil {
		return err
	}
	if len(infos) == 0 {
		fmt.Println("no profiles available to this caller")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PROFILE\tPROJECT\tCOMMANDS\tDESCRIPTION")
	for _, p := range infos {
		desc := p.Description
		if p.Granted {
			desc = "(granted) " + desc
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Name, p.Project, strings.Join(p.Commands, ","), desc)
	}
	return tw.Flush()
}

func runSecretsRun(args []string) (int, error) {
	useStdin := false
	if len(args) > 0 && args[0] == "--stdin" {
		useStdin, args = true, args[1:]
	}
	if len(args) < 3 || args[1] != "--" {
		return 2, fmt.Errorf("usage: spore secrets run [--stdin] <profile> -- <command> [args...]")
	}
	req := secrets.Request{Op: secrets.OpRun, Profile: args[0], Command: args[2], Args: args[3:]}
	if useStdin {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<20+1))
		if err != nil {
			return 1, err
		}
		if len(b) > 4<<20 {
			return 1, fmt.Errorf("stdin over 4 MiB")
		}
		req.Stdin = string(b)
	}
	resp, err := secretsCall(req)
	os.Stdout.WriteString(resp.Stdout)
	os.Stderr.WriteString(resp.Stderr)
	if resp.Truncated {
		fmt.Fprintln(os.Stderr, "spore secrets: output truncated at 4 MiB")
	}
	if err != nil {
		if resp.Exit != 0 {
			return resp.Exit, err
		}
		return 1, err
	}
	return resp.Exit, nil
}

// splitFlags pulls --name value / --name=value pairs out of args so
// flags may follow the positional arguments.
func splitFlags(args []string, names ...string) (map[string]string, []string, error) {
	vals := map[string]string{}
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		matched := false
		for _, n := range names {
			switch {
			case a == "--"+n:
				if i+1 >= len(args) {
					return nil, nil, fmt.Errorf("--%s needs a value", n)
				}
				vals[n], matched = args[i+1], true
				i++
			case strings.HasPrefix(a, "--"+n+"="):
				vals[n], matched = strings.TrimPrefix(a, "--"+n+"="), true
			}
		}
		if !matched {
			if strings.HasPrefix(a, "-") {
				return nil, nil, fmt.Errorf("unknown flag %s", a)
			}
			pos = append(pos, a)
		}
	}
	return vals, pos, nil
}

func runSecretsGrant(args []string) error {
	vals, pos, err := splitFlags(args, "for", "project")
	if err != nil || len(pos) != 2 || vals["for"] == "" {
		return fmt.Errorf("usage: spore secrets grant <task-slug> <profile> --for <duration> [--project <name>]")
	}
	if !grantSlugRE.MatchString(pos[0]) {
		return fmt.Errorf("bad task slug %q", pos[0])
	}
	resp, err := secretsCall(secrets.Request{Op: secrets.OpGrant, Task: pos[0], Profile: pos[1], For: vals["for"], Project: vals["project"]})
	if err != nil {
		return err
	}
	var g secrets.Grant
	if err := json.Unmarshal(resp.Data, &g); err != nil {
		return err
	}
	fmt.Printf("granted %s to %s/%s until %s\n", g.Profile, g.Project, g.Task, g.Expires.Local().Format(time.RFC3339))
	return nil
}

var grantSlugRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func runSecretsRevoke(args []string) error {
	vals, pos, err := splitFlags(args, "project")
	if err != nil || len(pos) < 1 || len(pos) > 2 {
		return fmt.Errorf("usage: spore secrets revoke <task-slug> [<profile>] [--project <name>]")
	}
	req := secrets.Request{Op: secrets.OpRevoke, Task: pos[0], Project: vals["project"]}
	if len(pos) == 2 {
		req.Profile = pos[1]
	}
	resp, err := secretsCall(req)
	if err != nil {
		return err
	}
	fmt.Print(resp.Stdout)
	return nil
}

func runSecretsGrants(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: spore secrets grants")
	}
	resp, err := secretsCall(secrets.Request{Op: secrets.OpGrants})
	if err != nil {
		return err
	}
	var gs []secrets.Grant
	if err := json.Unmarshal(resp.Data, &gs); err != nil {
		return err
	}
	if len(gs) == 0 {
		fmt.Println("no live grants")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PROJECT\tTASK\tPROFILE\tEXPIRES\tBY")
	for _, g := range gs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", g.Project, g.Task, g.Profile, g.Expires.Local().Format(time.RFC3339), g.By)
	}
	return tw.Flush()
}

func runSecretsCheck(args []string) error {
	fs := flag.NewFlagSet("secrets check", flag.ContinueOnError)
	cfgPath := fs.String("config", secrets.ConfigPath(), "broker config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("check: unexpected argument %q", fs.Arg(0))
	}
	cfg, err := secrets.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	fmt.Printf("ok: %s: %d project(s), %d profile(s)\n", *cfgPath, len(cfg.Projects), len(cfg.Profiles))
	return nil
}

func runSecretsServe(args []string) error {
	fs := flag.NewFlagSet("secrets serve", flag.ContinueOnError)
	cfgPath := fs.String("config", secrets.ConfigPath(), "broker config")
	sock := fs.String("socket", secrets.SocketPath(), "socket path when not socket-activated")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := secrets.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	srv, err := secrets.NewServer(cfg)
	if err != nil {
		return err
	}
	l, err := secrets.Listen(*sock)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "spore secrets: serving %d profile(s) on %s\n", len(cfg.Profiles), l.Addr())
	return srv.Serve(l)
}
