package fleet

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/versality/spore/internal/ghapp"
)

// The agent-side github.toml on a broker host lacks the App fields;
// with the socket down the mirror names the broker, not the config.
func TestGitHubMirrorConnectTokenOnBrokerHost(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "secrets-broker.json")
	if err := os.WriteFile(marker, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := ghapp.BrokerHostMarker
	ghapp.BrokerHostMarker = marker
	t.Cleanup(func() { ghapp.BrokerHostMarker = orig })
	m := &githubMirror{cfg: ghapp.Config{PRFlow: ghapp.PRFlowDraft, OperatorLogin: "op"}}
	if err := m.connectToken(); !errors.Is(err, ghapp.ErrBrokerHost) {
		t.Fatalf("connectToken: %v, want ErrBrokerHost", err)
	}
}
