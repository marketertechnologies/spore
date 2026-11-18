package task

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WorkerConfig captures the [worker] section of a project's spore.toml.
// It governs how the kernel spawns each worker's tmux session.
//
// IsolateNetwork, when true, makes ensureSession launch the worker
// command inside its own private network namespace (pasta userspace
// NAT). N parallel workers can then reuse identical TCP ports, blind to
// each other, while keeping outbound internet + DNS. Coordinators spawn
// via a separate path and are never affected.
type WorkerConfig struct {
	IsolateNetwork bool
}

// LoadWorkerConfig reads [worker] from <projectRoot>/spore.toml. A
// missing file yields a zero WorkerConfig with no error so callers can
// treat absent config as "kernel defaults" (isolation off).
func LoadWorkerConfig(projectRoot string) (WorkerConfig, error) {
	tomlPath := filepath.Join(projectRoot, "spore.toml")
	b, err := os.ReadFile(tomlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return WorkerConfig{}, nil
		}
		return WorkerConfig{}, fmt.Errorf("worker: read %s: %w", tomlPath, err)
	}
	cfg, err := parseWorkerTOML(string(b))
	if err != nil {
		return WorkerConfig{}, fmt.Errorf("worker: parse %s: %w", tomlPath, err)
	}
	return cfg, nil
}

// parseWorkerTOML reads the [worker] section from the same tiny TOML
// subset the rest of the kernel parses: bare or quoted scalars,
// `# comment` lines, blank lines. Sections other than [worker] are
// ignored. Unknown keys and malformed entries inside [worker] are an
// error so typos fail loud rather than silently leaving isolation off.
func parseWorkerTOML(content string) (WorkerConfig, error) {
	var cfg WorkerConfig
	section := ""
	scanner := bufio.NewScanner(strings.NewReader(content))
	for lineNum := 1; scanner.Scan(); lineNum++ {
		line := strings.TrimSpace(stripWorkerComment(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		if section != "worker" {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			return WorkerConfig{}, fmt.Errorf("line %d: malformed entry %q", lineNum, line)
		}
		key := strings.TrimSpace(line[:eq])
		val := stripWorkerQuotes(strings.TrimSpace(line[eq+1:]))
		if key != "isolate_network" {
			return WorkerConfig{}, fmt.Errorf("line %d: unknown key %q in [worker]", lineNum, key)
		}
		b, err := parseWorkerBool(val)
		if err != nil {
			return WorkerConfig{}, fmt.Errorf("line %d: isolate_network: %w", lineNum, err)
		}
		cfg.IsolateNetwork = b
	}
	if err := scanner.Err(); err != nil {
		return WorkerConfig{}, err
	}
	return cfg, nil
}

func parseWorkerBool(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("want boolean, got %q", v)
	}
}

func stripWorkerQuotes(v string) string {
	if len(v) >= 2 {
		first, last := v[0], v[len(v)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

func stripWorkerComment(line string) string {
	inQuote := byte(0)
	for i := 0; i < len(line); i++ {
		ch := line[i]
		switch {
		case inQuote != 0:
			if ch == inQuote {
				inQuote = 0
			}
		case ch == '"' || ch == '\'':
			inQuote = ch
		case ch == '#':
			return line[:i]
		}
	}
	return line
}
