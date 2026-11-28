package budget

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCostForUsageOpus(t *testing.T) {
	got, total := costForUsage("claude-opus-4-7", &usageBlock{
		InputTokens:              1_000_000,
		OutputTokens:             1_000_000,
		CacheReadInputTokens:     1_000_000,
		CacheCreationInputTokens: 1_000_000,
	})
	want := 5.0 + 25.0 + 0.50 + 6.25
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("opus cost: got %.6f want %.6f", got, want)
	}
	if total != 4_000_000 {
		t.Errorf("total tokens: got %d want %d", total, 4_000_000)
	}
}

func TestCostForUsageUnknownFallsBackToSonnet(t *testing.T) {
	got, _ := costForUsage("claude-newmodel-zzz", &usageBlock{InputTokens: 1_000_000})
	if math.Abs(got-3.0) > 1e-9 {
		t.Errorf("unknown model fallback: got %.6f want 3.0", got)
	}
}

func TestPricingTableLoaded(t *testing.T) {
	if pricingErr != nil {
		t.Fatalf("pricing.toml parse error: %v", pricingErr)
	}
	if _, ok := pricing["claude-opus-4-7"]; !ok {
		t.Errorf("pricing table missing claude-opus-4-7")
	}
	if fallbackPricing.InputPerMTok != 3.0 {
		t.Errorf("fallback InputPerMTok: got %.4f want 3.0", fallbackPricing.InputPerMTok)
	}
}

func TestAdviceBands(t *testing.T) {
	cases := []struct {
		short, long float64
		want        string
	}{
		{0, 0, "ok"},
		{0.79, 0.79, "ok"},
		{0.80, 0, "tighten"},
		{0.89, 0.79, "tighten"},
		{0.90, 0, "ration"},
		{0, 0.80, "tighten"},
		{0, 0.90, "ration"},
		{1.5, 0.1, "ration"},
	}
	for _, c := range cases {
		if got := adviceFor(c.short, c.long); got != c.want {
			t.Errorf("adviceFor(%.2f, %.2f) = %q, want %q", c.short, c.long, got, c.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0m"},
		{30 * time.Second, "0m"},
		{45 * time.Minute, "45m"},
		{5 * time.Hour, "5h"},
		{1*time.Hour + 47*time.Minute, "1h47m"},
		{4 * 24 * time.Hour, "4d"},
		{(7*24 + 3) * time.Hour, "7d3h"},
	}
	for _, c := range cases {
		if got := formatDuration(c.d); got != c.want {
			t.Errorf("formatDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestRefreshAndAggregate(t *testing.T) {
	dir := t.TempDir()
	projects := filepath.Join(dir, "projects", "myproj")
	if err := os.MkdirAll(projects, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339Nano)
	old := now.Add(-3 * 24 * time.Hour).Format(time.RFC3339Nano)
	tooOld := now.Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano)
	transcript := `{"type":"permission-mode"}
{"type":"assistant","requestId":"req_a","timestamp":"` + recent + `","message":{"id":"msg_a","model":"claude-opus-4-7","usage":{"input_tokens":1000,"output_tokens":1000,"cache_creation_input_tokens":1000,"cache_read_input_tokens":1000}}}
{"type":"assistant","requestId":"req_a","timestamp":"` + recent + `","message":{"id":"msg_a","model":"claude-opus-4-7","usage":{"input_tokens":1000,"output_tokens":1000,"cache_creation_input_tokens":1000,"cache_read_input_tokens":1000}}}
{"type":"assistant","requestId":"req_b","timestamp":"` + old + `","message":{"id":"msg_b","model":"claude-haiku-4-5","usage":{"input_tokens":2000,"output_tokens":2000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}
{"type":"assistant","requestId":"req_c","timestamp":"` + tooOld + `","message":{"id":"msg_c","model":"claude-opus-4-7","usage":{"input_tokens":99999999,"output_tokens":0}}}
`
	if err := os.WriteFile(filepath.Join(projects, "session.jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AGENT_BUDGET_PROJECTS", filepath.Join(dir, "projects"))
	t.Setenv("AGENT_BUDGET_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("AGENT_BUDGET_CREDS", filepath.Join(dir, "no-such-credentials.json"))
	t.Setenv("AGENT_BUDGET_ACCOUNTS_DIR", filepath.Join(dir, "no-accounts"))

	if err := Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	s, err := loadState()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	computeAggregates(s, time.Now().UTC())

	if s.Short.MessageCount != 1 {
		t.Errorf("short msg count: got %d want 1 (only req_a within 5h)", s.Short.MessageCount)
	}
	wantShort := (1000.0*5 + 1000.0*25 + 1000.0*6.25 + 1000.0*0.50) / 1e6
	if math.Abs(s.Short.CostUSD-wantShort) > 1e-9 {
		t.Errorf("short cost: got %.6f want %.6f", s.Short.CostUSD, wantShort)
	}

	if s.Long.MessageCount != 2 {
		t.Errorf("long msg count: got %d want 2 (req_a + req_b within 7d, req_c excluded)", s.Long.MessageCount)
	}
	wantLong := wantShort + (2000.0*1+2000.0*5)/1e6
	if math.Abs(s.Long.CostUSD-wantLong) > 1e-9 {
		t.Errorf("long cost: got %.6f want %.6f", s.Long.CostUSD, wantLong)
	}

	statePath := filepath.Join(dir, "state", "state.json")
	fi, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("state file mode: got %o want 600", fi.Mode().Perm())
	}

	if err := Refresh(); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	s2, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.Cache) != 1 {
		t.Errorf("expected one cached file, got %d", len(s2.Cache))
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENT_BUDGET_STATE_DIR", dir)
	now := time.Now().UTC().Truncate(time.Microsecond)
	reset := now.Add(2 * time.Hour)
	rem := int64(8000)
	lim := int64(10000)
	want := &state{
		Mode:      "api",
		UpdatedAt: now,
		Short: windowState{
			DurationSeconds: int(shortWindow.Seconds()),
			Frac:            0.42,
			ResetAt:         &reset,
			Source:          "api-headers",
			TokensRemaining: &rem,
			TokensLimit:     &lim,
			TokensBucket:    "tokens",
		},
		Long: windowState{
			DurationSeconds: int(longWindow.Seconds()),
			CostUSD:         12.50,
			CapUSD:          2000.0,
			Frac:            0.00625,
			MessageCount:    3,
			Source:          "transcript-est",
		},
		Advice: "ok",
		Cache: map[string]*fileEntry{
			"/tmp/session.jsonl": {SizeBytes: 1024, MtimeNs: now.UnixNano(), Messages: nil},
		},
		UsageSnapshot: &usageSnapshot{
			FetchedAt: now,
			Short:     usageWindow{Utilization: 42.0, ResetsAt: reset.Format(time.RFC3339Nano)},
			Long:      usageWindow{Utilization: 12.5, ResetsAt: reset.Format(time.RFC3339Nano)},
		},
	}
	if err := writeState(want); err != nil {
		t.Fatalf("writeState: %v", err)
	}
	got, err := loadState()
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if got.Mode != want.Mode || got.Advice != want.Advice {
		t.Errorf("mode/advice mismatch: got %+v", got)
	}
	if got.Short.Source != "api-headers" || got.Short.TokensBucket != "tokens" {
		t.Errorf("short window mismatch: got %+v", got.Short)
	}
	if got.Short.TokensRemaining == nil || *got.Short.TokensRemaining != 8000 {
		t.Errorf("tokens_remaining round-trip: %+v", got.Short.TokensRemaining)
	}
	if got.UsageSnapshot == nil || got.UsageSnapshot.Short.Utilization != 42.0 {
		t.Errorf("usage_snapshot round-trip: %+v", got.UsageSnapshot)
	}
	if math.Abs(got.Long.CostUSD-12.50) > 1e-9 {
		t.Errorf("long cost round-trip: %.6f want 12.50", got.Long.CostUSD)
	}

	if err := writeState(got); err != nil {
		t.Fatalf("re-writeState: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	got2, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeState(got2); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("state.json bytes drifted across read+write cycle\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}
