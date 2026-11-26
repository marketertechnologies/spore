package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoordinatorRespawnShellCarriesModelAndEffort(t *testing.T) {
	root := t.TempDir()
	t.Setenv(CoordinatorAgentEnv, "")
	t.Setenv("SPORE_COORDINATOR_ROLE", "")
	t.Setenv("SPORE_COORDINATOR_MODEL", "")
	t.Setenv("SPORE_COORDINATOR_EFFORT", "")
	toml := "[agents.coordinator]\nmodel = \"model-under-test\"\neffort = \"high\"\n"
	if err := os.WriteFile(filepath.Join(root, "spore.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	got := CoordinatorRespawnShell(root)
	for _, want := range []string{"model-under-test", "high"} {
		if !strings.Contains(got, want) {
			t.Errorf("respawn command %q lacks %q", got, want)
		}
	}
}
