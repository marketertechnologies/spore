package task

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// LoadAccountTier reads `[fleet] account_tier = "<tier>"` from
// <projectRoot>/spore.toml. The spawn path injects the value as
// SPORE_ACCOUNT_TIER into worker sessions so `spore worker
// token-monitor` resolves the intended per-tier caps. Returns "" when
// the file or key is missing (workers then default to the sub-max
// caps). Lives here rather than internal/fleet because ensureSession
// is the injection point and fleet imports task.
func LoadAccountTier(projectRoot string) string {
	b, err := os.ReadFile(filepath.Join(projectRoot, "spore.toml"))
	if err != nil {
		return ""
	}
	inFleet := false
	scanner := bufio.NewScanner(strings.NewReader(string(b)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inFleet = strings.TrimSpace(line[1:len(line)-1]) == "fleet"
			continue
		}
		if !inFleet {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 || strings.TrimSpace(line[:eq]) != "account_tier" {
			continue
		}
		val := strings.TrimSpace(line[eq+1:])
		if i := strings.IndexByte(val, '#'); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		return strings.Trim(val, `"'`)
	}
	return ""
}
