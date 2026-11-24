package lints

import (
	"strings"
	"testing"
)

func runTotalSize(t *testing.T, files map[string]string) []Issue {
	t.Helper()
	issues, err := ClaudeTotalSize{}.Run(newTestRepo(t, files))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return issues
}

func TestClaudeTotalSize_DefaultGraceBand(t *testing.T) {
	under := runTotalSize(t, map[string]string{"CLAUDE.md": repeatLines("x", 200)})
	if len(under) != 0 {
		t.Fatalf("200 lines: want no issue, got %v", under)
	}
	warn := runTotalSize(t, map[string]string{"CLAUDE.md": repeatLines("x", 250)})
	if len(warn) != 1 || !warn[0].Warn || !strings.Contains(warn[0].Message, "root_line_limit") {
		t.Fatalf("250 lines: want one warning, got %+v", warn)
	}
	fail := runTotalSize(t, map[string]string{"CLAUDE.md": repeatLines("x", 401)})
	if len(fail) != 1 || fail[0].Warn {
		t.Fatalf("401 lines: want one error, got %+v", fail)
	}
}

func TestClaudeTotalSize_SporeTomlPin(t *testing.T) {
	pinOld := runTotalSize(t, map[string]string{
		"spore.toml": "[lint]\nroot_line_limit = 400\n",
		"CLAUDE.md":  repeatLines("x", 250),
	})
	if len(pinOld) != 0 {
		t.Fatalf("pinned 400, 250 lines: want no issue, got %+v", pinOld)
	}
	pinStrict := runTotalSize(t, map[string]string{
		"spore.toml": "[lint]\nroot_line_limit = 200 # strict\n",
		"CLAUDE.md":  repeatLines("x", 250),
	})
	if len(pinStrict) != 1 || pinStrict[0].Warn {
		t.Fatalf("pinned 200, 250 lines: want one error, got %+v", pinStrict)
	}
	sub := runTotalSize(t, map[string]string{
		"spore.toml":    "[lint]\nsubdir_line_limit = 3\n",
		"sub/CLAUDE.md": repeatLines("x", 4),
	})
	if len(sub) != 1 || sub[0].Path != "sub/CLAUDE.md" {
		t.Fatalf("subdir pin: got %+v", sub)
	}
}

func TestClaudeTotalSize_SporeTomlBadValue(t *testing.T) {
	root := newTestRepo(t, map[string]string{
		"spore.toml": "[lint]\nroot_line_limit = \"lots\"\n",
		"CLAUDE.md":  "ok\n",
	})
	if _, err := (ClaudeTotalSize{}).Run(root); err == nil || !strings.Contains(err.Error(), "root_line_limit") {
		t.Fatalf("want root_line_limit error, got %v", err)
	}
}
