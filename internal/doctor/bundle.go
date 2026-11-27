package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// bundleTokenRE matches the GitHub credential shapes bundler configs
// carry: classic, fine-grained, OAuth and App installation tokens, and
// the x-access-token user of an App token URL.
var bundleTokenRE = regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{8,}|github_pat_[A-Za-z0-9_]{8,}|x-access-token`)

// bundleTokenLines returns "line N (KEY)" for each line of a bundler
// config holding a GitHub token. The key names the host, never the
// value.
func bundleTokenLines(data []byte) []string {
	var out []string
	for i, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || t[0] == '#' || !bundleTokenRE.MatchString(t) {
			continue
		}
		k, _, ok := strings.Cut(t, ": ")
		k = strings.Trim(strings.TrimSpace(k), `"'`)
		if ok && envKeyRE.MatchString(k) {
			out = append(out, fmt.Sprintf("line %d (%s)", i+1, k))
		} else {
			out = append(out, fmt.Sprintf("line %d", i+1))
		}
	}
	return out
}

var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// checkBundleConfig flags a literal GitHub token in bundler's global
// config or a project .bundle/config, and a global config readable by
// anyone but its owner. The sandbox masks ~/.bundle/config; a project
// .bundle/config sits in the worktree every agent reads, so a token
// there fails. Never print the value.
func checkBundleConfig(env Env) Result {
	const name = "bundle tokens"
	if env.BundleConfig == "" && len(env.ProjectBundle) == 0 {
		return Result{name, Skip, "no HOME"}
	}
	var fails, warns []string
	seen, masked := 0, ""
	globalFI, globalErr := os.Stat(env.BundleConfig)
	files := append([]string{env.BundleConfig}, env.ProjectBundle...)
	for i, f := range files {
		if f == "" {
			continue
		}
		// A project entry naming the global file (doctor run from
		// $HOME, BUNDLE_USER_CONFIG=~/.bundle/config) is graded once,
		// as the global file.
		if i > 0 && env.BundleConfig != "" {
			if filepath.Clean(f) == filepath.Clean(env.BundleConfig) {
				continue
			}
			if fi, err := os.Stat(f); err == nil && globalErr == nil && os.SameFile(fi, globalFI) {
				continue
			}
		}
		// Inside the sandbox the global file is a /dev/null mask (a
		// 0666 character device): grading its mode or content would
		// misreport the real file.
		if fi, err := os.Stat(f); i == 0 && err == nil && !fi.Mode().IsRegular() {
			masked = f + " is masked by the sandbox (not a regular file); run spore doctor from an operator shell to check it"
			continue
		}
		data, err := os.ReadFile(f)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			warns = append(warns, err.Error())
			continue
		}
		seen++
		global := i == 0
		if lines := bundleTokenLines(data); len(lines) > 0 {
			msg := f + " " + strings.Join(lines, ", ") + " holds a literal GitHub token"
			if global {
				warns = append(warns, msg)
			} else {
				fails = append(fails, msg+" readable by every agent in the project")
			}
		}
		if !global {
			continue
		}
		// A symlink (home-manager into the read-only store) cannot be
		// chmodded; its token content was graded above.
		if fi, err := os.Lstat(f); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if fi, err := os.Stat(f); err == nil && fi.Mode().Perm()&0o077 != 0 {
			warns = append(warns, fmt.Sprintf("%s is mode %04o (want 0600: chmod 600)", f, fi.Mode().Perm()))
		}
		if hasACL(f) {
			warns = append(warns, f+" carries an ACL (setfacl -b)")
		}
	}
	const hint = "; supply a read:packages token as BUNDLE_RUBYGEMS__PKG__GITHUB__COM from the environment and drop BUNDLE_GITHUB__COM (see docs/sandbox.md)"
	notes := append(fails, warns...)
	if masked != "" {
		notes = append(notes, masked)
	}
	switch {
	case len(fails) > 0:
		return Result{name, Fail, strings.Join(notes, "; ") + hint}
	case len(warns) > 0:
		return Result{name, Warn, strings.Join(notes, "; ") + hint}
	case masked != "":
		return Result{name, Skip, masked}
	case seen == 0:
		return Result{name, Pass, "no bundler config found"}
	default:
		return Result{name, Pass, fmt.Sprintf("%d bundler config(s), no GitHub token, global one owner-only", seen)}
	}
}
