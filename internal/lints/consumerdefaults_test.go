package lints

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/versality/spore/internal/composer"
)

func TestConsumerDefaults(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "rules", "consumers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	full := "# target: CLAUDE.md\ncore/x\n" + strings.Join(composer.DefaultCore, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "ok.txt"), []byte(full), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("# target: AGENTS.md\ncore/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"sub.txt": "# target: web/AGENTS.md\ncore/x\n", "bare.txt": "core/x\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	issues, err := ConsumerDefaults{}.Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(issues) != len(composer.DefaultCore) {
		t.Fatalf("issues = %v; want one per default for old.txt", issues)
	}
	for i, is := range issues {
		if is.Path != "rules/consumers/old.txt" {
			t.Errorf("issue path = %q; want rules/consumers/old.txt", is.Path)
		}
		if !strings.Contains(is.Message, composer.DefaultCore[i]) || !strings.Contains(is.Message, "--consumer old --add-defaults") {
			t.Errorf("message %q; want fragment id and fix command", is.Message)
		}
	}
}

func TestConsumerDefaults_NoConsumersDir(t *testing.T) {
	issues, err := ConsumerDefaults{}.Run(t.TempDir())
	if err != nil || len(issues) != 0 {
		t.Fatalf("Run = %v, %v; want nothing", issues, err)
	}
}

func TestConsumerDefaults_Advisory(t *testing.T) {
	if !Advisory(ConsumerDefaults{}) {
		t.Fatal("consumer-defaults must be advisory")
	}
	if Advisory(EmDash{}) {
		t.Fatal("em-dash must not be advisory")
	}
}
