package lints

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSandboxOff(t *testing.T) {
	for _, tc := range []struct {
		name, toml string
		want       int
		warn       bool
	}{
		{"no spore.toml", "", 0, false},
		{"on", "[sandbox]\nenabled = true\n", 0, false},
		{"off", "[sandbox]\nenabled = false\n", 1, true},
		{"broken", "[sandbox]\nenabled = nope\n", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.toml != "" {
				if err := os.WriteFile(filepath.Join(root, "spore.toml"), []byte(tc.toml), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			issues, err := SandboxOff{}.Run(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(issues) != tc.want {
				t.Fatalf("issues = %v, want %d", issues, tc.want)
			}
			if tc.want == 1 && issues[0].Warn != tc.warn {
				t.Fatalf("warn = %v, want %v", issues[0].Warn, tc.warn)
			}
		})
	}
}
