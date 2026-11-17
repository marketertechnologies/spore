// Package agentenv reads a project's agent.env: the per-project
// variables spore seeds into the loop role panes it spawns, so one
// project's agents can run under a different account, budget state
// directory or endpoint than the rest of the host.
//
// The file holds credentials, so nothing here ever puts a value into
// an error, a warning or a log line. Messages name the key and the
// line number only.
package agentenv

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileName is the per-project environment file spore seeds into the
// loop role panes it spawns.
const FileName = "agent.env"

// reservedPrefixes are the variables spore sets itself. A project file
// that reuses one would silently point an agent at another project's
// root, inbox or state, so those entries are dropped rather than
// honoured. Every variable the role-pane spawn passes carries one of
// these prefixes.
var reservedPrefixes = []string{"SPORE_", "WT_"}

// Entry is one variable to seed into a spawned session.
type Entry struct {
	Key   string
	Value string
}

// Path returns the agent.env path for project. The project name comes
// from task.ProjectName, which already resolves a worktree back to its
// main repository, so every worktree of one project shares one file.
func Path(project string) (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "spore", project, FileName), nil
}

// Load reads the agent.env for project. A missing file yields no
// entries and no error: most projects never have one.
func Load(project string) ([]Entry, []string, error) {
	path, err := Path(project)
	if err != nil {
		return nil, nil, err
	}
	return LoadFile(path)
}

// LoadFile reads one agent.env. The second return is operator-facing
// messages to print; they never contain a value. An error means the
// file could not be understood and the caller must not spawn, because
// a half-read credential file runs the agent as the wrong identity
// without anyone noticing.
func LoadFile(path string) ([]Entry, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	defer f.Close()

	var msgs []string
	if info, err := f.Stat(); err == nil {
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			msgs = append(msgs, fmt.Sprintf(
				"%s is mode %04o; it can hold a credential, so restrict it with: chmod 600 %s",
				path, mode, path))
		}
	}

	var entries []Entry
	scanner := bufio.NewScanner(f)
	for n := 1; scanner.Scan(); n++ {
		key, value, ok := parseLine(scanner.Text())
		if !ok {
			return nil, msgs, fmt.Errorf(
				"%s line %d: malformed entry, expected KEY=VALUE, # comment or a blank line",
				path, n)
		}
		if key == "" {
			continue
		}
		if prefix, reserved := reservedPrefix(key); reserved {
			msgs = append(msgs, fmt.Sprintf(
				"%s line %d: key %q is reserved by spore (%s* is spore's own namespace) and was ignored",
				path, n, key, prefix))
			continue
		}
		entries = append(entries, Entry{Key: key, Value: value})
	}
	if err := scanner.Err(); err != nil {
		return nil, msgs, fmt.Errorf("%s: %w", path, err)
	}
	return entries, msgs, nil
}

// parseLine splits one agent.env line. ok is false for a line that is
// not a comment, a blank or a KEY=VALUE entry; key is empty for a
// comment or a blank.
//
// Nothing expands and nothing unescapes: no shell runs, so a value
// reaches the agent byte for byte. A value wrapped in matching single
// or double quotes keeps its interior whitespace; an unquoted one is
// trimmed, because trailing spaces in a pasted token are invisible.
// An unmatched opening quote is an error rather than a literal, since
// a token carrying a stray quote fails to authenticate in a way the
// operator cannot see.
func parseLine(line string) (key, value string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", true
	}
	eq := strings.IndexByte(trimmed, '=')
	if eq <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(trimmed[:eq])
	if !validKey(key) {
		return "", "", false
	}
	// Whitespace either side of an unquoted value is meaningless and
	// invisible: `TOKEN= abcdef` is a space the operator cannot see in
	// a credential they cannot read back. Trimming both sides is the
	// only resolution that cannot produce a wrong value, and the
	// quoted form below stays available for the rare case where the
	// whitespace is deliberate.
	value = strings.TrimSpace(trimmed[eq+1:])
	if len(value) > 0 && (value[0] == '"' || value[0] == '\'') {
		quote := value[0]
		if len(value) < 2 || value[len(value)-1] != quote {
			return "", "", false
		}
		return key, value[1 : len(value)-1], true
	}
	return key, value, true
}

func validKey(key string) bool {
	for i, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return key != ""
}

func reservedPrefix(key string) (string, bool) {
	for _, p := range reservedPrefixes {
		if strings.HasPrefix(key, p) {
			return p, true
		}
	}
	return "", false
}

// TmuxArgs renders entries as `tmux new-session -e` arguments.
func TmuxArgs(entries []Entry) []string {
	args := make([]string, 0, len(entries)*2)
	for _, e := range entries {
		args = append(args, "-e", e.Key+"="+e.Value)
	}
	return args
}
