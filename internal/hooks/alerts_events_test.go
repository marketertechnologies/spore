package hooks

import (
	"strings"
	"testing"
	"time"

	"github.com/versality/spore/internal/events"
)

func TestStopFailure_RecordsLedgerAlertWithoutMessage(t *testing.T) {
	setupAlerts(t, "proj")
	t.Setenv("TMUX_PANE", "%4")
	t.Setenv("SPORE_TASK_SLUG", "demo")
	for _, kind := range []string{"rate_limit", "server_error"} {
		if err := StopFailure(StopFailureInput{Error: kind, LastAssistantMessage: limitBanner}); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := events.Read("proj", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Kind != events.Alert || evs[0].Fields["error"] != "rate_limit" || evs[1].Fields["error"] != "server_error" {
		t.Fatalf("ledger = %+v", evs)
	}
	if evs[0].Slug != "demo" || evs[0].Fields["reset"] == "" {
		t.Errorf("alert event = %+v; want slug and reset time", evs[0])
	}
	for _, e := range evs {
		if strings.Contains(e.Detail, "limit") && len(e.Detail) > len("rate_limit") {
			t.Errorf("assistant message leaked into the ledger: %+v", e)
		}
	}
}
