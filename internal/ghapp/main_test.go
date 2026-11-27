package ghapp

import (
	"os"
	"testing"
)

// The host may run the secrets broker; tests exercise the direct key
// path, so point the marker away unless a test sets it.
func TestMain(m *testing.M) {
	BrokerHostMarker = "/nonexistent/spore-secrets-broker.json"
	os.Exit(m.Run())
}
