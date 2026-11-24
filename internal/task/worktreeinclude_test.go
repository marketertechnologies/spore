package task

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// includeRepo is a committed project whose .gitignore ignores
// everything the tests place outside `tracked/`.
func includeRepo(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitRepo(t, root)
	writeFile(t, filepath.Join(root, ".gitignore"), "*\n!.gitignore\n!.worktreeinclude\n!tracked/\n!tracked/**\ntracked/.env\n")
	writeFile(t, filepath.Join(root, "tracked", "keep.txt"), "tracked\n")
	runGit(t, root, "add", ".gitignore", "tracked")
	runGit(t, root, "commit", "-q", "-m", "files")
	return root
}

func runInclude(t *testing.T, root, worktree, include string) string {
	t.Helper()
	writeFile(t, filepath.Join(root, WorktreeIncludeFile), include)
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	copyWorktreeInclude(root, worktree, &warn)
	return warn.String()
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestWorktreeIncludeCopiesMatchingIgnoredFiles(t *testing.T) {
	root := includeRepo(t)
	writeFile(t, filepath.Join(root, ".env"), "TOP\n")
	writeFile(t, filepath.Join(root, "tracked", ".env"), "NESTED\n")
	writeFile(t, filepath.Join(root, "node_modules", "pkg", ".env"), "DEP\n")
	writeFile(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "js\n")
	writeFile(t, filepath.Join(root, "config", "app.json"), "{}\n")
	writeFile(t, filepath.Join(root, "secret.txt"), "no\n")
	writeFile(t, filepath.Join(root, ".env.local"), "LOCAL\n")
	wt := filepath.Join(t.TempDir(), "wt")

	warn := runInclude(t, root, wt, "# env files\n.env*\n!.env.local\n/config/\n")
	if warn != "" {
		t.Errorf("unexpected warning: %q", warn)
	}
	for rel, want := range map[string]bool{
		".env":                  true,
		"tracked/.env":          true,
		"config/app.json":       true,
		".env.local":            false,
		"secret.txt":            false,
		"node_modules/pkg/.env": false,
	} {
		if got := exists(filepath.Join(wt, rel)); got != want {
			t.Errorf("%s copied = %v, want %v", rel, got, want)
		}
	}
}

func TestWorktreeIncludeDirectoryPattern(t *testing.T) {
	root := includeRepo(t)
	writeFile(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "js\n")
	writeFile(t, filepath.Join(root, "build"), "a file, not a dir\n")
	wt := filepath.Join(t.TempDir(), "wt")
	runInclude(t, root, wt, "node_modules/\nbuild/\n")
	if !exists(filepath.Join(wt, "node_modules", "pkg", "index.js")) {
		t.Error("node_modules/ not copied")
	}
	if exists(filepath.Join(wt, "build")) {
		t.Error("dir-only pattern matched a file")
	}
}

func TestWorktreeIncludeNeverOverwrites(t *testing.T) {
	root := includeRepo(t)
	writeFile(t, filepath.Join(root, ".env"), "MAIN\n")
	wt := filepath.Join(t.TempDir(), "wt")
	writeFile(t, filepath.Join(wt, ".env"), "WORKTREE\n")
	runInclude(t, root, wt, ".env\n")
	if b, _ := os.ReadFile(filepath.Join(wt, ".env")); string(b) != "WORKTREE\n" {
		t.Errorf(".env = %q, want the worktree's own copy", b)
	}
}

func TestWorktreeIncludeSymlinks(t *testing.T) {
	// The project root is reached through a symlink so that both the
	// alias and the real path count as "the main checkout".
	real := includeRepo(t)
	root := filepath.Join(t.TempDir(), "alias")
	symlink(t, real, root)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeFile(t, outside, "out\n")
	writeFile(t, filepath.Join(real, "data", "secret"), "s\n")
	writeFile(t, filepath.Join(real, ".env"), "E\n")

	symlink(t, filepath.Join(real, "data", "secret"), filepath.Join(real, "abs-real"))
	symlink(t, filepath.Join(root, "data", "secret"), filepath.Join(real, "abs-alias"))
	symlink(t, filepath.Join(real, "data"), filepath.Join(real, "abs-dir"))
	symlink(t, outside, filepath.Join(real, "abs-outside"))
	symlink(t, ".env", filepath.Join(real, "rel-in"))
	symlink(t, "../outside.txt", filepath.Join(real, "rel-out"))
	symlink(t, "../.env", filepath.Join(real, "cache", "ok"))
	symlink(t, "../../x", filepath.Join(real, "cache", "up"))
	// cache/link -> .. is the project root, so cache/via's `link/..`
	// climbs above it although the lexical path stays inside.
	writeFile(t, filepath.Join(filepath.Dir(real), "x"), "above\n")
	symlink(t, "..", filepath.Join(real, "cache", "link"))
	symlink(t, "link/../x", filepath.Join(real, "cache", "via"))
	symlink(t, "link/../gone/../x", filepath.Join(real, "cache", "dangling"))

	wt := filepath.Join(t.TempDir(), "wt")
	warn := runInclude(t, root, wt, "abs-*\nrel-*\ncache/\n")

	for rel, want := range map[string]bool{
		"abs-real":    false,
		"abs-alias":   false,
		"abs-dir":     false,
		"abs-outside": true,
		"rel-in":      true,
		"rel-out":     false,
		"cache/ok":    true,
		"cache/up":    false,
		"cache/via":   false,
		"cache/link":  true,
	} {
		if got := exists(filepath.Join(wt, rel)); got != want {
			t.Errorf("%s copied = %v, want %v", rel, got, want)
		}
	}
	if target, err := os.Readlink(filepath.Join(wt, "rel-in")); err != nil || target != ".env" {
		t.Errorf("rel-in -> %q (%v), want .env", target, err)
	}
	if n := strings.Count(warn, "skip symlink"); n != 7 {
		t.Errorf("warn has %d skip lines, want 7:\n%s", n, warn)
	}
}

func TestWorktreeIncludeRefusesParentOutsideWorktree(t *testing.T) {
	root := includeRepo(t)
	writeFile(t, filepath.Join(root, "sub", "f"), "f\n")
	wt := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	mainDir := filepath.Join(root, "landing")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "sub", "deeper", "g"), "g\n")
	symlink(t, mainDir, filepath.Join(wt, "sub"))
	var warn bytes.Buffer
	c, err := newIncludeCopier(root, wt, &warn)
	if err != nil {
		t.Fatal(err)
	}
	c.copyEntry(filepath.Join("sub", "f"), fs.FileMode(0))
	if exists(filepath.Join(mainDir, "f")) {
		t.Error("copy wrote through a symlinked parent into the main checkout")
	}
	c.copyEntry(filepath.Join("sub", "deeper", "g"), fs.FileMode(0))
	if exists(filepath.Join(mainDir, "deeper")) {
		t.Error("MkdirAll created a directory in the main checkout before the parent check")
	}
}

func TestIncludeMatch(t *testing.T) {
	pats := parseWorktreeInclude(strings.NewReader("*.key\n/top.txt\nconf/*.json\n!conf/skip.json\n\\#hash\n\\*lit\ntab\t\nspace\\ \ntrail  \n"))
	cases := []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{"a.key", false, true},
		{"deep/b.key", false, true},
		{"top.txt", false, true},
		{"deep/top.txt", false, false},
		{"conf/app.json", false, true},
		{"deep/conf/app.json", false, false},
		{"conf/skip.json", false, false},
		{"#hash", false, true},
		{"*lit", false, true},
		{"alit", false, false},
		{"tab\t", false, true},
		{"tab", false, false},
		{"space ", false, true},
		{"trail", false, true},
	}
	for _, c := range cases {
		if got := includeMatch(pats, c.rel, c.isDir); got != c.want {
			t.Errorf("includeMatch(%q) = %v, want %v", c.rel, got, c.want)
		}
	}
}

// TestWorktreeIncludeNegationInsideCopiedDir keeps last-match-wins
// for entries inside a directory that is copied whole, and skips an
// excluded subdirectory whole, as gitignore does.
func TestWorktreeIncludeNegationInsideCopiedDir(t *testing.T) {
	root := includeRepo(t)
	writeFile(t, filepath.Join(root, "secrets", "dev.key"), "dev\n")
	writeFile(t, filepath.Join(root, "secrets", "prod.key"), "prod\n")
	writeFile(t, filepath.Join(root, "secrets", "old", "a.key"), "a\n")
	writeFile(t, filepath.Join(root, "secrets", "old", "keep.txt"), "k\n")
	writeFile(t, filepath.Join(root, "secrets", "x.log"), "log\n")
	wt := filepath.Join(t.TempDir(), "wt")

	if warn := runInclude(t, root, wt, "secrets/\n!secrets/prod.key\n!secrets/old/\nsecrets/old/keep.txt\n!*.log\n"); warn != "" {
		t.Errorf("unexpected warning: %q", warn)
	}
	for rel, want := range map[string]bool{
		"secrets/dev.key":      true,
		"secrets/prod.key":     false,
		"secrets/old/a.key":    false,
		"secrets/old/keep.txt": false, // gitignore: no re-include under an excluded dir
		"secrets/x.log":        false,
	} {
		if got := exists(filepath.Join(wt, filepath.FromSlash(rel))); got != want {
			t.Errorf("%s copied = %v, want %v", rel, got, want)
		}
	}
}
