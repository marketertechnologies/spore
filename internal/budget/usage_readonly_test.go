package budget

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A 401 must not refresh or rewrite the login: refresh tokens rotate,
// so a refresh here would race Claude Code's own and log out every
// session on the host. The server answers every non-usage path with a
// failure and records it, so any token call shows up.
func TestRefreshOn401IsReadOnlyAndStale(t *testing.T) {
	dir := t.TempDir()
	stateD := filepath.Join(dir, "state")
	if err := os.MkdirAll(filepath.Join(dir, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	creds := filepath.Join(dir, ".credentials.json")
	writeCredentials(t, creds, "at-fresh", "rt-fresh", time.Now().Add(2*time.Hour).UnixMilli())
	before, err := os.ReadFile(creds)
	if err != nil {
		t.Fatal(err)
	}
	fiBefore, err := os.Stat(creds)
	if err != nil {
		t.Fatal(err)
	}

	var usageCalls, otherCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/usage" {
			atomic.AddInt32(&otherCalls, 1)
			http.Error(w, "unexpected", http.StatusTeapot)
			return
		}
		if atomic.AddInt32(&usageCalls, 1) == 1 {
			_, _ = fmt.Fprintln(w, `{"five_hour":{"utilization":50,"resets_at":"2026-04-26T17:00:00.000000+00:00"},"seven_day":{"utilization":25,"resets_at":"2026-04-26T20:00:00.000000+00:00"}}`)
			return
		}
		http.Error(w, `{"type":"error","error":{"type":"authentication_error"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	swapUsageURL(t, srv.URL+"/usage")

	t.Setenv("AGENT_BUDGET_PROJECTS", filepath.Join(dir, "projects"))
	t.Setenv("AGENT_BUDGET_STATE_DIR", stateD)
	t.Setenv("AGENT_BUDGET_CREDS", creds)
	t.Setenv("AGENT_BUDGET_ACCOUNTS_DIR", filepath.Join(dir, "no-accounts"))
	t.Setenv("AGENT_BUDGET_USAGE_MIN_INTERVAL_SEC", "0")

	if err := Refresh(); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if err := Refresh(); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if got := atomic.LoadInt32(&usageCalls); got != 2 {
		t.Errorf("usage calls: got %d want 2 (no retry after 401)", got)
	}
	if got := atomic.LoadInt32(&otherCalls); got != 0 {
		t.Errorf("non-usage calls: got %d want 0", got)
	}
	after, err := os.ReadFile(creds)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("credentials rewritten on 401:\nbefore %s\nafter  %s", before, after)
	}
	fiAfter, err := os.Stat(creds)
	if err != nil {
		t.Fatal(err)
	}
	if !fiAfter.ModTime().Equal(fiBefore.ModTime()) || !os.SameFile(fiBefore, fiAfter) {
		t.Error("credentials file replaced or touched on 401")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".credentials.") && e.Name() != ".credentials.json" {
			t.Errorf("temp credentials file left behind: %s", e.Name())
		}
	}

	s, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	if s.UsageSnapshot == nil || !s.UsageSnapshot.Stale {
		t.Fatalf("snapshot not marked stale after 401: %+v", s.UsageSnapshot)
	}
	if s.Short.Source != "usage-stale" {
		t.Errorf("short.source: got %q want usage-stale", s.Short.Source)
	}
}

// The account store is held to the same rule: a 401 leaves the file
// alone and the account snapshot stale.
func TestAccountStore401IsReadOnly(t *testing.T) {
	dir := t.TempDir()
	accountsD := filepath.Join(dir, "accounts")
	writeAccountFile(t, accountsD, "primary", "at-dead", "rt-primary")
	path := filepath.Join(accountsD, "primary.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var otherCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/usage" {
			atomic.AddInt32(&otherCalls, 1)
		}
		http.Error(w, "expired", http.StatusUnauthorized)
	}))
	defer srv.Close()
	swapUsageURL(t, srv.URL+"/usage")

	if _, err := fetchUsageForFile(context.Background(), path); err == nil {
		t.Fatal("want error on 401")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("account store file rewritten on 401")
	}
	if otherCalls != 0 {
		t.Errorf("non-usage calls: %d", otherCalls)
	}
}
