// Package doctor runs `spore doctor`: read-only drift checks on a
// spore host, one pass/warn/fail/skip line each. See docs/doctor.md.
package doctor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// VerifiedClaudeCode is the Claude Code version spore's hooks, deny
// rules and role loop were last verified against. Bump it only after
// re-running the checks listed in docs/doctor.md ("Claude Code
// version"), and record the run there.
const VerifiedClaudeCode = "2.1.329"

// Status is a check outcome.
type Status string

const (
	Pass Status = "pass"
	Warn Status = "warn"
	Fail Status = "fail"
	Skip Status = "skip"
)

// Result is one check line.
type Result struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
}

// Env holds every host path and probe the checks read, so tests can
// point them at a sandbox.
type Env struct {
	// SW is the system profile holding the deployed spore
	// (/run/current-system/sw).
	SW string
	// DropIn is the managed settings drop-in path.
	DropIn string
	// RepoRoot is a spore checkout whose VERSION to compare; "" skips.
	RepoRoot string
	// FlakeLock is the host flake's lock file; "" or missing skips.
	FlakeLock string
	// ClaudeBin is the Claude Code binary to ask for its version.
	ClaudeBin string
	// ConfigDir is ~/.config/spore; StateBase is ~/.local/state/spore.
	ConfigDir string
	StateBase string
	// Npmrc is ~/.npmrc; "" skips.
	Npmrc string
	// BundleConfig is ~/.bundle/config, masked from sandboxed
	// commands; "" skips. ProjectBundle lists bundler configs agents
	// can read (project .bundle/config, BUNDLE_CONFIG,
	// BUNDLE_USER_CONFIG).
	BundleConfig  string
	ProjectBundle []string
	// SelfVersion is this binary's BuildVersion.
	SelfVersion string

	Run          func(name string, args ...string) ([]byte, error)
	Render       func(spore, hooksDir, marketplace string) ([]byte, error)
	FleetEnabled func() (bool, error)
}

// Check runs every check in order.
func Check(env Env) []Result {
	if env.Run == nil {
		env.Run = func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).Output()
		}
	}
	var out []Result
	out = append(out, checkBinary(env)...)
	out = append(out, checkManagedSettings(env)...)
	out = append(out, checkClaudeCode(env))
	out = append(out, checkFleetUnits(env)...)
	out = append(out, checkSecrets(env))
	out = append(out, checkNpmrc(env))
	out = append(out, checkBundleConfig(env))
	out = append(out, checkKillSwitch(env))
	return out
}

// Counts tallies fails and warns.
func Counts(rs []Result) (fails, warns int) {
	for _, r := range rs {
		switch r.Status {
		case Fail:
			fails++
		case Warn:
			warns++
		}
	}
	return fails, warns
}

func deployedSpore(env Env) string { return filepath.Join(env.SW, "bin", "spore") }

// splitVersion parses BuildVersion output, "0.40.0 (843a4a3...)".
func splitVersion(s string) (version, commit string) {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	version, rest, _ := strings.Cut(s, " ")
	commit = strings.Trim(strings.TrimSpace(rest), "()")
	if commit == "commit unknown" {
		commit = ""
	}
	return version, commit
}

func checkBinary(env Env) []Result {
	bin := deployedSpore(env)
	raw, err := env.Run(bin, "--version")
	if err != nil {
		return []Result{{"binary", Fail, fmt.Sprintf("%s --version: %v", bin, err)}}
	}
	ver, commit := splitVersion(string(raw))
	res := []Result{}
	detail := fmt.Sprintf("deployed %s", strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0]))
	if env.SelfVersion != "" && strings.TrimSpace(string(raw)) != env.SelfVersion {
		res = append(res, Result{"binary", Warn, detail + "; this binary is " + env.SelfVersion})
	} else {
		res = append(res, Result{"binary", Pass, detail})
	}

	if env.RepoRoot == "" {
		res = append(res, Result{"binary vs repo VERSION", Skip, "no spore checkout (pass --repo)"})
	} else if b, err := os.ReadFile(filepath.Join(env.RepoRoot, "VERSION")); err != nil {
		res = append(res, Result{"binary vs repo VERSION", Skip, err.Error()})
	} else {
		want := strings.TrimSpace(string(b))
		switch c := compareVersions(ver, want); {
		case c == 0:
			res = append(res, Result{"binary vs repo VERSION", Pass, want})
		case c < 0:
			res = append(res, Result{"binary vs repo VERSION", Warn, fmt.Sprintf("deployed %s, %s VERSION %s: deploy pending", ver, env.RepoRoot, want)})
		default:
			res = append(res, Result{"binary vs repo VERSION", Warn, fmt.Sprintf("deployed %s is newer than %s VERSION %s", ver, env.RepoRoot, want)})
		}
	}

	rev, err := flakeSporeRev(env.FlakeLock)
	switch {
	case env.FlakeLock == "" || errors.Is(err, fs.ErrNotExist):
		res = append(res, Result{"binary vs flake input", Skip, "no host flake.lock"})
	case err != nil:
		res = append(res, Result{"binary vs flake input", Warn, err.Error()})
	case rev == "":
		res = append(res, Result{"binary vs flake input", Skip, env.FlakeLock + " has no spore input"})
	case commit == "":
		res = append(res, Result{"binary vs flake input", Warn, "deployed binary reports no commit; flake input " + short(rev)})
	case strings.HasPrefix(rev, commit) || strings.HasPrefix(commit, rev):
		res = append(res, Result{"binary vs flake input", Pass, short(rev)})
	default:
		res = append(res, Result{"binary vs flake input", Fail, fmt.Sprintf("deployed %s, flake input %s: the locked spore is not what runs (switch not applied?)", short(commit), short(rev))})
	}
	return res
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// flakeSporeRev returns the locked rev of the flake.lock node that
// points at a repo named spore.
func flakeSporeRev(path string) (string, error) {
	if path == "" {
		return "", fs.ErrNotExist
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var lock struct {
		Nodes map[string]struct {
			Locked struct {
				Repo string `json:"repo"`
				URL  string `json:"url"`
				Rev  string `json:"rev"`
			} `json:"locked"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(b, &lock); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if n, ok := lock.Nodes["spore"]; ok && n.Locked.Rev != "" {
		return n.Locked.Rev, nil
	}
	var keys []string
	for k := range lock.Nodes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		l := lock.Nodes[k].Locked
		if l.Rev != "" && (l.Repo == "spore" || strings.HasSuffix(strings.TrimSuffix(l.URL, ".git"), "/spore")) {
			return l.Rev, nil
		}
	}
	return "", nil
}

// compareVersions compares dotted numeric versions; non-numeric parts
// compare as zero.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func checkManagedSettings(env Env) []Result {
	live, err := os.ReadFile(env.DropIn)
	if errors.Is(err, fs.ErrNotExist) {
		if _, serr := os.Stat(deployedSpore(env)); serr != nil {
			return []Result{{"managed settings", Skip, "no drop-in and no deployed spore (not a spore host)"}}
		}
		return []Result{{"managed settings", Fail, env.DropIn + " missing"}}
	}
	if err != nil {
		return []Result{{"managed settings", Fail, err.Error()}}
	}
	var liveTop map[string]json.RawMessage
	if err := json.Unmarshal(live, &liveTop); err != nil {
		return []Result{{"managed settings", Fail, env.DropIn + " is not valid JSON"}}
	}
	if env.Render == nil {
		return []Result{{"managed settings", Skip, "no renderer"}}
	}
	rendered, err := env.Render(deployedSpore(env), filepath.Join(env.SW, "share", "spore", "hooks"), marketplaceDir(liveTop))
	if err != nil {
		return []Result{{"managed settings", Fail, "render: " + err.Error()}}
	}
	var wantTop map[string]json.RawMessage
	if err := json.Unmarshal(rendered, &wantTop); err != nil {
		return []Result{{"managed settings", Fail, "render: " + err.Error()}}
	}

	var res []Result
	res = append(res, compareSection("managed settings: hooks", liveTop["hooks"], wantTop["hooks"], hooksDiff))
	res = append(res, compareSection("managed settings: deny", subKey(liveTop["permissions"], "deny"), subKey(wantTop["permissions"], "deny"), denyDiff))
	if wantTop["sandbox"] == nil && liveTop["sandbox"] == nil {
		res = append(res, Result{"managed settings: sandbox", Skip, "this binary renders no sandbox block"})
	} else {
		res = append(res, compareSection("managed settings: sandbox", liveTop["sandbox"], wantTop["sandbox"], nil))
	}
	var rest []string
	for k := range union(liveTop, wantTop) {
		switch k {
		case "hooks", "sandbox":
			continue
		case "permissions":
			if !jsonEqual(withoutKey(liveTop[k], "deny"), withoutKey(wantTop[k], "deny")) {
				rest = append(rest, k)
			}
			continue
		}
		if !jsonEqual(liveTop[k], wantTop[k]) {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	if len(rest) == 0 {
		res = append(res, Result{"managed settings: other keys", Pass, "match"})
	} else {
		res = append(res, Result{"managed settings: other keys", Fail, "differ: " + strings.Join(rest, ", ")})
	}
	return res
}

func marketplaceDir(top map[string]json.RawMessage) string {
	var m map[string]struct {
		Source struct {
			Path string `json:"path"`
		} `json:"source"`
	}
	if raw := top["extraKnownMarketplaces"]; raw != nil && json.Unmarshal(raw, &m) == nil {
		return m["spore"].Source.Path
	}
	return ""
}

func compareSection(name string, live, want json.RawMessage, diff func(live, want json.RawMessage) string) Result {
	if jsonEqual(live, want) {
		return Result{name, Pass, "matches what this binary renders"}
	}
	detail := "differs from what this binary renders (stale rebuild?)"
	if diff != nil {
		if d := diff(live, want); d != "" {
			detail += ": " + d
		}
	}
	return Result{name, Fail, detail}
}

func hooksDiff(live, want json.RawMessage) string {
	var l, w map[string]json.RawMessage
	_ = json.Unmarshal(live, &l)
	_ = json.Unmarshal(want, &w)
	var diff []string
	for k := range union(l, w) {
		if !jsonEqual(l[k], w[k]) {
			switch {
			case l[k] == nil:
				diff = append(diff, k+" missing")
			case w[k] == nil:
				diff = append(diff, k+" extra")
			default:
				diff = append(diff, k+" changed")
			}
		}
	}
	sort.Strings(diff)
	return strings.Join(diff, ", ")
}

func denyDiff(live, want json.RawMessage) string {
	var l, w []string
	_ = json.Unmarshal(live, &l)
	_ = json.Unmarshal(want, &w)
	missing, extra := setDiff(w, l), setDiff(l, w)
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("%d missing (%s)", len(missing), firstN(missing, 3)))
	}
	if len(extra) > 0 {
		parts = append(parts, fmt.Sprintf("%d extra (%s)", len(extra), firstN(extra, 3)))
	}
	return strings.Join(parts, ", ")
}

func setDiff(a, b []string) []string {
	in := map[string]bool{}
	for _, x := range b {
		in[x] = true
	}
	var out []string
	for _, x := range a {
		if !in[x] {
			out = append(out, x)
		}
	}
	return out
}

func firstN(xs []string, n int) string {
	if len(xs) > n {
		return strings.Join(xs[:n], ", ") + ", ..."
	}
	return strings.Join(xs, ", ")
}

func subKey(raw json.RawMessage, key string) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m[key]
}

func withoutKey(raw json.RawMessage, key string) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	delete(m, key)
	b, _ := json.Marshal(m)
	return b
}

func union(a, b map[string]json.RawMessage) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

func jsonEqual(a, b json.RawMessage) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(x, y)
}

func checkClaudeCode(env Env) Result {
	bin := env.ClaudeBin
	if bin == "" {
		bin = "claude"
	}
	raw, err := env.Run(bin, "--version")
	if err != nil {
		return Result{"claude code version", Warn, fmt.Sprintf("%s --version: %v", bin, err)}
	}
	ver := strings.Fields(string(raw))
	if len(ver) == 0 {
		return Result{"claude code version", Warn, "empty --version output"}
	}
	switch c := compareVersions(ver[0], VerifiedClaudeCode); {
	case c == 0:
		return Result{"claude code version", Pass, ver[0] + " (verified)"}
	case c > 0:
		return Result{"claude code version", Warn, fmt.Sprintf("%s is newer than the verified %s; re-run the checks in docs/doctor.md", ver[0], VerifiedClaudeCode)}
	default:
		return Result{"claude code version", Warn, fmt.Sprintf("%s is older than the verified %s", ver[0], VerifiedClaudeCode)}
	}
}

type unit struct {
	id, load, active, sub, result string
}

var unitProps = "--property=Id,LoadState,ActiveState,SubState,Result"

func showUnits(env Env, names ...string) (map[string]unit, error) {
	args := append([]string{"--user", "show", unitProps}, names...)
	raw, err := env.Run("systemctl", args...)
	if err != nil {
		return nil, err
	}
	units := map[string]unit{}
	for _, block := range strings.Split(strings.TrimSpace(string(raw)), "\n\n") {
		var u unit
		for _, l := range strings.Split(block, "\n") {
			k, v, _ := strings.Cut(strings.TrimSpace(l), "=")
			switch k {
			case "Id":
				u.id = v
			case "LoadState":
				u.load = v
			case "ActiveState":
				u.active = v
			case "SubState":
				u.sub = v
			case "Result":
				u.result = v
			}
		}
		if u.id != "" {
			units[u.id] = u
		}
	}
	return units, nil
}

// checkFleetUnits finds the fleet's projects from the loaded
// spore-fleet-reconcile-* units (a glob only matches loaded units, and
// an idle oneshot service is not loaded), then asks for each project's
// timer, path units and service by name.
func checkFleetUnits(env Env) []Result {
	const base = "spore-fleet-reconcile-"
	loaded, err := showUnits(env, base+"*")
	if err != nil {
		return []Result{{"fleet units", Warn, "systemctl --user show: " + err.Error()}}
	}
	projects := map[string]bool{}
	for id := range loaded {
		name, kind, ok := strings.Cut(strings.TrimPrefix(id, base), ".")
		if !ok {
			continue
		}
		switch kind {
		case "service", "timer":
			projects[name] = true
		case "path":
			for _, p := range []string{"flag-", "tasks-"} {
				if n, ok := strings.CutPrefix(name, p); ok {
					projects[n] = true
				}
			}
		}
	}
	if len(projects) == 0 {
		return []Result{{"fleet units", Warn, "no " + base + "* user units loaded (fleet module off?)"}}
	}
	var names, want []string
	for n := range projects {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		want = append(want, base+n+".timer", base+"flag-"+n+".path", base+"tasks-"+n+".path", base+n+".service")
	}
	units, err := showUnits(env, want...)
	if err != nil {
		return []Result{{"fleet units", Warn, "systemctl --user show: " + err.Error()}}
	}
	var res []Result
	for _, n := range names {
		var probs []string
		for _, id := range []string{base + n + ".timer", base + "flag-" + n + ".path", base + "tasks-" + n + ".path"} {
			u, ok := units[id]
			switch {
			case !ok || u.load != "loaded":
				probs = append(probs, id+" not loaded")
			case u.active != "active":
				probs = append(probs, fmt.Sprintf("%s %s/%s", id, u.active, u.sub))
			}
		}
		svc := base + n + ".service"
		if u, ok := units[svc]; !ok || u.load != "loaded" {
			probs = append(probs, svc+" not loaded")
		} else if u.active == "failed" || (u.result != "" && u.result != "success") {
			probs = append(probs, fmt.Sprintf("%s last run %s (%s)", svc, u.result, u.active))
		}
		if len(probs) == 0 {
			res = append(res, Result{"fleet units: " + n, Pass, "timer and path units active, last pass ok"})
		} else {
			res = append(res, Result{"fleet units: " + n, Fail, strings.Join(probs, "; ")})
		}
	}
	return res
}

// secretFile reports whether a file under the spore config or state
// dir holds secret material, by name.
func secretFile(name string) bool {
	switch name {
	case "secrets.env", "github-token.json", "github.toml":
		return true
	}
	return strings.HasSuffix(name, ".pem") || strings.HasSuffix(name, ".key")
}

func checkSecrets(env Env) Result {
	var bad []string
	n := 0
	walk := func(root string, depth int) {
		if root == "" {
			return
		}
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if strings.Count(strings.TrimPrefix(p, root), string(filepath.Separator)) >= depth {
					return filepath.SkipDir
				}
				return nil
			}
			if !secretFile(d.Name()) {
				return nil
			}
			info, err := os.Stat(p)
			if err != nil || !info.Mode().IsRegular() {
				return nil
			}
			n++
			if info.Mode().Perm()&0o077 != 0 {
				bad = append(bad, fmt.Sprintf("%s is %04o", p, info.Mode().Perm()))
			}
			return nil
		})
	}
	walk(env.ConfigDir, 3)
	walk(env.StateBase, 2)
	switch {
	case len(bad) > 0:
		return Result{"secrets file modes", Fail, strings.Join(bad, "; ") + " (want 0600: chmod 600)"}
	case n == 0:
		return Result{"secrets file modes", Pass, "no secret files found"}
	default:
		return Result{"secrets file modes", Pass, fmt.Sprintf("%d secret file(s), none readable by group or others", n)}
	}
}

// npmrcAuthKeys are the .npmrc keys whose value is a credential.
var npmrcAuthKeys = []string{"_authToken", "_auth", "_password"}

// npmrcEnvRef is a value npm and yarn expand from the environment
// (`${VAR}`, or npm's optional `${VAR?}`).
var npmrcEnvRef = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\??\}$`)

// npmrcValue unquotes a quoted value and, like npm's ini parser, cuts an
// unquoted one at the first ';' or '#'.
func npmrcValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	if i := strings.IndexAny(v, ";#"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// npmrcLiteralAuth returns the 1-based line numbers of auth keys set to
// a literal value. A value that is entirely a ${VAR} reference is
// expanded by npm and yarn at run time and holds no secret itself.
func npmrcLiteralAuth(data []byte) []int {
	var lines []int
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if j := strings.LastIndex(k, ":"); j >= 0 {
			k = k[j+1:]
		}
		v = npmrcValue(v)
		if v == "" || npmrcEnvRef.MatchString(v) {
			continue
		}
		for _, a := range npmrcAuthKeys {
			if k == a {
				lines = append(lines, i+1)
				break
			}
		}
	}
	return lines
}

// checkNpmrc flags a literal registry token in ~/.npmrc. Since the
// sandbox no longer hides that file (yarn 1 reads .npmrc from every
// ancestor of the cwd and fails on the deny placeholder), any token in
// it is readable by agent commands. Never print the value.
func checkNpmrc(env Env) Result {
	const name = "npmrc tokens"
	if env.Npmrc == "" {
		return Result{name, Skip, "no HOME"}
	}
	data, err := os.ReadFile(env.Npmrc)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Result{name, Pass, env.Npmrc + " absent"}
	case err != nil:
		return Result{name, Warn, err.Error()}
	}
	lines := npmrcLiteralAuth(data)
	if len(lines) == 0 {
		return Result{name, Pass, "no literal auth token in " + env.Npmrc}
	}
	ls := make([]string, len(lines))
	for i, n := range lines {
		ls[i] = strconv.Itoa(n)
	}
	return Result{name, Warn, fmt.Sprintf("%s line %s holds a literal auth token readable by sandboxed agents; use a ${VAR} reference, or a narrowly scoped token in a file named by NPM_CONFIG_USERCONFIG, which sandboxed agents can read too (see docs/sandbox.md)", env.Npmrc, strings.Join(ls, ", "))}
}

func checkKillSwitch(env Env) Result {
	if env.FleetEnabled == nil {
		return Result{"kill switch", Skip, "no probe"}
	}
	on, err := env.FleetEnabled()
	switch {
	case err != nil:
		return Result{"kill switch", Warn, err.Error()}
	case on:
		return Result{"kill switch", Pass, "fleet enabled"}
	default:
		return Result{"kill switch", Warn, "fleet disabled: no workers spawn (spore fleet enable)"}
	}
}
