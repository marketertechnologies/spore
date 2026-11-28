package secrets

import (
	"errors"
	"testing"
	"time"
)

// A package whose TestMain scrubs SPORE_* must still not reach the
// host broker: every test binary that links this package gets an
// absent socket, whatever the host runs.
func TestSocketPathUnderTestNeverHostDefault(t *testing.T) {
	t.Setenv("SPORE_SECRETS_SOCKET", "")
	p := SocketPath()
	if p == DefaultSocketPath {
		t.Fatalf("SocketPath() = host default %s", p)
	}
	if Available() {
		t.Fatalf("broker reported available at %s", p)
	}
	if _, _, err := Call(Request{}, nil, time.Second); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Call err = %v, want ErrUnavailable", err)
	}
}
