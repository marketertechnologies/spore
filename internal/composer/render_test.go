package composer

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files under testdata/split/want-*")

const splitPool = "testdata/split/rules"

// goldenName flattens an output path so the golden tree never holds a
// real `.claude/rules/` dir or CLAUDE.md that Claude Code or the
// repo's own lints would pick up.
func goldenName(p string) string {
	return strings.TrimPrefix(strings.ReplaceAll(p, "/", "_"), ".") + ".golden"
}

func checkGolden(t *testing.T, consumer, wantDir string) {
	t.Helper()
	r, err := Render(splitPool, filepath.Join(splitPool, "consumers", consumer), Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	files, err := r.Files()
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	dir := filepath.Join("testdata", "split", wantDir)
	if *update {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		for p, c := range files {
			writeFile(t, filepath.Join(dir, goldenName(p)), c)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read golden dir: %v", err)
	}
	var gotNames, wantNames []string
	for p := range files {
		gotNames = append(gotNames, goldenName(p))
	}
	for _, e := range entries {
		wantNames = append(wantNames, e.Name())
	}
	sort.Strings(gotNames)
	sort.Strings(wantNames)
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("output set:\n got %v\nwant %v", gotNames, wantNames)
	}
	for p, c := range files {
		want, err := os.ReadFile(filepath.Join(dir, goldenName(p)))
		if err != nil {
			t.Fatal(err)
		}
		if c != string(want) {
			t.Errorf("%s mismatch:\n got: %q\nwant: %q", p, c, want)
		}
	}
}

func TestRender_SplitGolden(t *testing.T) {
	checkGolden(t, "split.txt", "want-split")
}

func TestRender_ImportGolden(t *testing.T) {
	checkGolden(t, "import.txt", "want-import")
}

func TestRender_ComposeOmitsScopedFragments(t *testing.T) {
	got, err := Compose(splitPool, filepath.Join(splitPool, "consumers", "split.txt"), Options{})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if strings.Contains(got, "GraphQL") || strings.Contains(got, "paths:") {
		t.Fatalf("root body leaks a path-scoped fragment:\n%s", got)
	}
}

func TestRender_ImportNeedsAgentsTarget(t *testing.T) {
	rulesDir, consumersDir := setupPool(t)
	consumer := filepath.Join(consumersDir, "c.txt")
	writeFile(t, consumer, "# target: CLAUDE.md\n# mirror: import\ncore/a\n")
	r, err := Render(rulesDir, consumer, Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if _, err := r.Files(); err == nil || !strings.Contains(err.Error(), "AGENTS.md") {
		t.Fatalf("Files: want missing AGENTS.md target error, got %v", err)
	}
}

func TestRender_BadMirror(t *testing.T) {
	rulesDir, consumersDir := setupPool(t)
	consumer := filepath.Join(consumersDir, "c.txt")
	writeFile(t, consumer, "# mirror: symlink\ncore/a\n")
	if _, err := Render(rulesDir, consumer, Options{}); err == nil {
		t.Fatal("Render: want error for unknown mirror mode")
	}
}

func TestRender_AllScopedIsError(t *testing.T) {
	rulesDir, consumersDir := setupPool(t)
	writeFile(t, filepath.Join(rulesDir, "x.md"), "---\npaths: [\"a/**\"]\n---\nbody\n")
	consumer := filepath.Join(consumersDir, "c.txt")
	writeFile(t, consumer, "x\n")
	if _, err := Render(rulesDir, consumer, Options{}); err == nil {
		t.Fatal("Render: want error when the root file would be empty")
	}
}

func TestParseFrontmatter(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		globs   []string
		body    string
		wantErr string
	}{
		{name: "none", src: "# t\nbody", body: "# t\nbody"},
		{name: "flow", src: "---\npaths: [\"a/**\", 'b/*.rb']\n---\nx", globs: []string{"a/**", "b/*.rb"}, body: "x"},
		{name: "block", src: "---\npaths:\n  - \"a/**\"\n  - b/**\n---\nx", globs: []string{"a/**", "b/**"}, body: "x"},
		{name: "comma string", src: "---\npaths: \"a/**, src/*.{ts,tsx}\"\n---\nx", globs: []string{"a/**", "src/*.{ts,tsx}"}, body: "x"},
		{name: "flow braces", src: "---\npaths: [src/*.{ts,tsx}, lib/**]\n---\nx", globs: []string{"src/*.{ts,tsx}", "lib/**"}, body: "x"},
		{name: "trailing comments", src: "---\npaths: [\"a/**\"] # api\n---\nx", globs: []string{"a/**"}, body: "x"},
		{name: "block comments", src: "---\n# scope\npaths:\n  - \"a/**\" # api\n  - b/#x/**\n---\nx", globs: []string{"a/**", "b/#x/**"}, body: "x"},
		{name: "unknown key", src: "---\npath: [a]\n---\nx", wantErr: "unsupported key"},
		{name: "empty paths", src: "---\npaths: []\n---\nx", wantErr: "empty"},
		{name: "unclosed", src: "---\npaths: [a]\nx", wantErr: "closing"},
		{name: "duplicate", src: "---\npaths: [a]\npaths: [b]\n---\nx", wantErr: "duplicate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			globs, body, err := parseFrontmatter(c.src)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if !reflect.DeepEqual(globs, c.globs) || body != c.body {
				t.Fatalf("got %q / %q, want %q / %q", globs, body, c.globs, c.body)
			}
		})
	}
}

func TestIsGenerated(t *testing.T) {
	cases := map[string]bool{
		"<!-- generated from x.txt -->\n# a\n":                       true,
		"---\npaths:\n  - \"a\"\n---\n\n<!-- generated from x -->\n": true,
		"# hand written\n<!-- generated from x -->\n":                false,
		"---\npaths: [a]\n---\n# hand written\n":                     false,
		"":                                                           false,
	}
	for in, want := range cases {
		if got := IsGenerated(in); got != want {
			t.Errorf("IsGenerated(%q) = %v, want %v", in, got, want)
		}
	}
}
