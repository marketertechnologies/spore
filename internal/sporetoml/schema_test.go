package sporetoml

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckFlagsUnknownKeysAndSections(t *testing.T) {
	content := `top = 1
[fleet]
max_worker = 1
max_workers = 2 # fine
role_stall_minuts = 3

[fleett]
max_workers = 0

[lint]
root_line_limt = 5

[coordinator]
model = "x"
agnet = "y"

[matter.linear]
anything = "ok"

[fleet.workers.ratio]
codex = 1

[fleet.reviewers.C]
mode = "fleet"
`
	var got []string
	for _, p := range Check(content) {
		got = append(got, p.String())
	}
	want := []string{
		`line 1: unknown top-level key "top"`,
		`line 3: unknown key "max_worker" in [fleet]`,
		`line 5: unknown key "role_stall_minuts" in [fleet]`,
		`line 7: unknown section [fleett]`,
		`line 11: unknown key "root_line_limt" in [lint]`,
		`line 15: unknown key "agnet" in [coordinator]`,
		`line 23: unknown section [fleet.reviewers.C]`,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d problems, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("problem %d = %q, want prefix %q", i, got[i], want[i])
		}
	}
}

func TestCheckRepoSporeTOMLIsClean(t *testing.T) {
	problems, err := CheckFile(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Errorf("spore.toml: %s", p)
	}
}

func TestWarnUnknown(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "spore.toml"), []byte("[fleet]\nmax_worker = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	WarnUnknown(&buf, "spore lint", dir)
	if got := buf.String(); !strings.HasPrefix(got, `spore lint: warning: spore.toml line 2: unknown key "max_worker" in [fleet]`) {
		t.Errorf("got %q", got)
	}
	buf.Reset()
	WarnUnknown(&buf, "x", t.TempDir())
	if buf.Len() != 0 {
		t.Errorf("missing spore.toml warned: %q", buf.String())
	}
}
