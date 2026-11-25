// Package secrets is the secrets broker: a service running as a
// separate user that holds project secrets and the GitHub App key,
// runs allowlisted commands with a profile's variables injected, and
// returns only their redacted output. Agents talk to it over a unix
// socket and never see a secret value. See docs/secrets.md.
package secrets

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DefaultConfigPath and DefaultSocketPath are where the NixOS module
// puts the broker config and socket. SPORE_SECRETS_CONFIG and
// SPORE_SECRETS_SOCKET override them.
const (
	DefaultConfigPath = "/etc/spore/secrets-broker.json"
	DefaultSocketPath = "/run/spore-secrets/broker.sock"
)

// DefaultPushBranch is the branch shape the broker pushes when a
// project sets none: the role loop's task branches.
const DefaultPushBranch = `^wt/[A-Za-z0-9._-]+$`

// Config is the broker's view of the host. It names files and
// commands, never secret values, so it may live in /etc or the store.
type Config struct {
	// AgentUser is the account agents run as. Empty means the broker's
	// own account (development and tests).
	AgentUser string `json:"agent_user"`
	// PilotGroup members (and root) may grant profiles and re-enable
	// the fleet. The agent user must not be in it.
	PilotGroup string `json:"pilot_group"`
	// SecretsDir holds secrets.env (global), <project>/secrets.env and
	// <project>/github.toml plus the App key.
	SecretsDir string `json:"secrets_dir"`
	// StateDir holds audit.log, grants.json and the git mirrors.
	StateDir string `json:"state_dir"`
	// FleetFlag is the kill-switch flag the broker creates and removes.
	FleetFlag string             `json:"fleet_flag"`
	Projects  map[string]Project `json:"projects"`
	Profiles  map[string]Profile `json:"profiles"`
}

// Project is one spore project the broker serves.
type Project struct {
	// Repo is HOST/OWNER/REPO; GitHub calls are pinned to it.
	Repo string `json:"repo"`
	// PushBranch is a regexp every pushed branch must match.
	PushBranch string `json:"push_branch"`
}

// Profile is one named capability: some of a project's variables plus
// the commands allowed to see them.
type Profile struct {
	Project     string `json:"project"`
	Description string `json:"description"`
	// Vars maps the name the command sees to the secrets.env key.
	Vars     map[string]string  `json:"vars"`
	Commands map[string]Command `json:"commands"`
}

// Command is one allowed program. The caller names it by its key and
// supplies only the trailing arguments.
type Command struct {
	// Argv is the fixed prefix; ${NAME} expands from the profile Vars.
	// Caller arguments are never expanded.
	Argv []string `json:"argv"`
	// Args are positional regexps, each fully matching one caller
	// argument. With Variadic the last one repeats; without it the
	// count must match exactly. No Args means no caller arguments.
	Args     []string `json:"args"`
	Variadic bool     `json:"variadic"`
	// Stdin lets the caller pipe input (an SQL script, a JSON body).
	Stdin bool `json:"stdin"`
	// Timeout is a Go duration; default 60s, at most 10m.
	Timeout string `json:"timeout"`

	argRE   []*regexp.Regexp
	timeout time.Duration
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var varRE = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ConfigPath is $SPORE_SECRETS_CONFIG or DefaultConfigPath.
func ConfigPath() string {
	if p := os.Getenv("SPORE_SECRETS_CONFIG"); p != "" {
		return p
	}
	return DefaultConfigPath
}

// SocketPath is $SPORE_SECRETS_SOCKET or DefaultSocketPath.
func SocketPath() string {
	if p := os.Getenv("SPORE_SECRETS_SOCKET"); p != "" {
		return p
	}
	return DefaultSocketPath
}

// LoadConfig reads and validates the broker config at path.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("secrets: %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("secrets: %s: %w", path, err)
	}
	return &c, nil
}

// Validate checks names, regexps and ${VAR} references, and compiles
// the argument patterns.
func (c *Config) Validate() error {
	if c.SecretsDir == "" || !filepath.IsAbs(c.SecretsDir) {
		return fmt.Errorf("secrets_dir must be an absolute path")
	}
	if c.StateDir == "" || !filepath.IsAbs(c.StateDir) {
		return fmt.Errorf("state_dir must be an absolute path")
	}
	if c.FleetFlag != "" && !filepath.IsAbs(c.FleetFlag) {
		return fmt.Errorf("fleet_flag must be an absolute path")
	}
	for name, p := range c.Projects {
		if !nameRE.MatchString(name) {
			return fmt.Errorf("project %q: bad name", name)
		}
		if p.Repo != "" && len(strings.Split(p.Repo, "/")) != 3 {
			return fmt.Errorf("project %q: repo %q is not HOST/OWNER/REPO", name, p.Repo)
		}
		if p.PushBranch != "" {
			if _, err := regexp.Compile(p.PushBranch); err != nil {
				return fmt.Errorf("project %q: push_branch: %w", name, err)
			}
		}
	}
	for name, p := range c.Profiles {
		if !nameRE.MatchString(name) {
			return fmt.Errorf("profile %q: bad name", name)
		}
		if _, ok := c.Projects[p.Project]; !ok {
			return fmt.Errorf("profile %q: unknown project %q", name, p.Project)
		}
		if len(p.Commands) == 0 {
			return fmt.Errorf("profile %q: no commands", name)
		}
		for k := range p.Vars {
			if !envNameRE.MatchString(k) {
				return fmt.Errorf("profile %q: bad var name %q", name, k)
			}
		}
		for cname, cmd := range p.Commands {
			if !nameRE.MatchString(cname) {
				return fmt.Errorf("profile %q: bad command name %q", name, cname)
			}
			if len(cmd.Argv) == 0 || cmd.Argv[0] == "" {
				return fmt.Errorf("profile %q command %q: empty argv", name, cname)
			}
			if strings.Contains(cmd.Argv[0], "${") {
				return fmt.Errorf("profile %q command %q: argv[0] must be a literal program", name, cname)
			}
			for _, a := range cmd.Argv {
				for _, m := range varRE.FindAllStringSubmatch(a, -1) {
					if _, ok := p.Vars[m[1]]; !ok {
						return fmt.Errorf("profile %q command %q: ${%s} is not in vars", name, cname, m[1])
					}
				}
			}
			if cmd.Variadic && len(cmd.Args) == 0 {
				return fmt.Errorf("profile %q command %q: variadic needs args", name, cname)
			}
			cmd.argRE = nil
			for _, a := range cmd.Args {
				re, err := regexp.Compile(`^(?:` + a + `)$`)
				if err != nil {
					return fmt.Errorf("profile %q command %q: args: %w", name, cname, err)
				}
				cmd.argRE = append(cmd.argRE, re)
			}
			cmd.timeout = 60 * time.Second
			if cmd.Timeout != "" {
				d, err := time.ParseDuration(cmd.Timeout)
				if err != nil || d <= 0 || d > 10*time.Minute {
					return fmt.Errorf("profile %q command %q: timeout %q: want a duration up to 10m", name, cname, cmd.Timeout)
				}
				cmd.timeout = d
			}
			p.Commands[cname] = cmd
		}
	}
	return nil
}

var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// checkArgs reports whether args satisfy the command's patterns.
func (c Command) checkArgs(args []string) error {
	n := len(c.argRE)
	switch {
	case n == 0 && len(args) > 0:
		return fmt.Errorf("takes no arguments")
	case !c.Variadic && len(args) != n:
		return fmt.Errorf("takes exactly %d argument(s), got %d", n, len(args))
	case c.Variadic && len(args) < n-1:
		return fmt.Errorf("takes at least %d argument(s), got %d", n-1, len(args))
	}
	for i, a := range args {
		re := c.argRE[min(i, n-1)]
		if !re.MatchString(a) {
			return fmt.Errorf("argument %d is not allowed by the profile (want %s)", i+1, c.Args[min(i, n-1)])
		}
	}
	return nil
}

// ProfileNames returns the profile names, sorted.
func (c *Config) ProfileNames() []string {
	out := make([]string, 0, len(c.Profiles))
	for k := range c.Profiles {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// envFiles returns the layered secrets files for project: global
// first, then the project's own, so the project's keys win.
func (c *Config) envFiles(project string) []string {
	return []string{
		filepath.Join(c.SecretsDir, "secrets.env"),
		filepath.Join(c.SecretsDir, project, "secrets.env"),
	}
}

func (c *Config) pushBranchRE(project string) *regexp.Regexp {
	pat := c.Projects[project].PushBranch
	if pat == "" {
		pat = DefaultPushBranch
	}
	return regexp.MustCompile(pat)
}
