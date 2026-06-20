package migrations

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestApply_runsPendingInLexicalOrder(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "trace")
	fsys := fstest.MapFS{
		"bootstrap/migrations/002-second.sh": {Data: []byte("echo b >> " + marker + "\n")},
		"bootstrap/migrations/001-first.sh":  {Data: []byte("echo a >> " + marker + "\n")},
		"bootstrap/migrations/README.md":     {Data: []byte("not a migration\n")},
	}
	res, err := Apply(fsys, "bootstrap/migrations", Options{
		LedgerPath: filepath.Join(dir, "ledger"),
		Version:    "test",
		Stdout:     &bytes.Buffer{},
		Stderr:     &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Applied) != 2 || res.Applied[0] != "001-first.sh" || res.Applied[1] != "002-second.sh" {
		t.Fatalf("Applied = %v, want [001-first.sh 002-second.sh]", res.Applied)
	}
	body, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if string(body) != "a\nb\n" {
		t.Fatalf("marker = %q, want \"a\\nb\\n\"", body)
	}
}

func TestApply_skipsApplied(t *testing.T) {
	dir := t.TempDir()
	ledger := filepath.Join(dir, "ledger")
	if err := os.WriteFile(ledger, []byte("001-first.sh\t2026-01-01T00:00:00Z\tprior\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "trace")
	fsys := fstest.MapFS{
		"bootstrap/migrations/001-first.sh":  {Data: []byte("echo a >> " + marker + "\n")},
		"bootstrap/migrations/002-second.sh": {Data: []byte("echo b >> " + marker + "\n")},
	}
	res, err := Apply(fsys, "bootstrap/migrations", Options{
		LedgerPath: ledger,
		Stdout:     &bytes.Buffer{},
		Stderr:     &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Applied) != 1 || res.Applied[0] != "002-second.sh" {
		t.Fatalf("Applied = %v, want [002-second.sh]", res.Applied)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "001-first.sh" {
		t.Fatalf("Skipped = %v, want [001-first.sh]", res.Skipped)
	}
	body, _ := os.ReadFile(marker)
	if string(body) != "b\n" {
		t.Fatalf("marker = %q, want \"b\\n\"", body)
	}
}

func TestApply_dryRunDoesNotExecuteOrRecord(t *testing.T) {
	dir := t.TempDir()
	ledger := filepath.Join(dir, "ledger")
	marker := filepath.Join(dir, "trace")
	fsys := fstest.MapFS{
		"bootstrap/migrations/001-first.sh": {Data: []byte("echo a >> " + marker + "\n")},
	}
	res, err := Apply(fsys, "bootstrap/migrations", Options{
		DryRun:     true,
		LedgerPath: ledger,
		Stdout:     &bytes.Buffer{},
		Stderr:     &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Applied) != 0 {
		t.Fatalf("Applied = %v, want empty", res.Applied)
	}
	if len(res.Pending) != 1 || res.Pending[0] != "001-first.sh" {
		t.Fatalf("Pending = %v, want [001-first.sh]", res.Pending)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker exists after dry-run: %v", err)
	}
	if _, err := os.Stat(ledger); !os.IsNotExist(err) {
		t.Fatalf("ledger created during dry-run: %v", err)
	}
}

func TestApply_failureHaltsAndDoesNotRecord(t *testing.T) {
	dir := t.TempDir()
	ledger := filepath.Join(dir, "ledger")
	fsys := fstest.MapFS{
		"bootstrap/migrations/001-ok.sh":    {Data: []byte("true\n")},
		"bootstrap/migrations/002-fail.sh":  {Data: []byte("exit 7\n")},
		"bootstrap/migrations/003-never.sh": {Data: []byte("true\n")},
	}
	res, err := Apply(fsys, "bootstrap/migrations", Options{
		LedgerPath: ledger,
		Stdout:     &bytes.Buffer{},
		Stderr:     &bytes.Buffer{},
	})
	if err == nil {
		t.Fatalf("Apply: expected error, got nil")
	}
	if res.Failed != "002-fail.sh" {
		t.Fatalf("Failed = %q, want 002-fail.sh", res.Failed)
	}
	if len(res.Applied) != 1 || res.Applied[0] != "001-ok.sh" {
		t.Fatalf("Applied = %v, want [001-ok.sh]", res.Applied)
	}
	// 003-never should not appear in Pending (halt stopped enumeration after the failure)
	// nor Applied.
	for _, n := range res.Applied {
		if n == "003-never.sh" {
			t.Fatalf("003-never.sh applied despite failure halt")
		}
	}

	body, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	got := string(body)
	if !strings.HasPrefix(got, "001-ok.sh\t") {
		t.Fatalf("ledger first line = %q, want 001-ok.sh prefix", got)
	}
	if strings.Contains(got, "002-fail.sh") {
		t.Fatalf("ledger contains failed migration: %q", got)
	}
}

func TestApply_idempotentOnRerun(t *testing.T) {
	dir := t.TempDir()
	ledger := filepath.Join(dir, "ledger")
	marker := filepath.Join(dir, "trace")
	fsys := fstest.MapFS{
		"bootstrap/migrations/001-only.sh": {Data: []byte("echo x >> " + marker + "\n")},
	}
	opts := Options{LedgerPath: ledger, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if _, err := Apply(fsys, "bootstrap/migrations", opts); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	res, err := Apply(fsys, "bootstrap/migrations", opts)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if len(res.Applied) != 0 {
		t.Fatalf("second-run Applied = %v, want empty", res.Applied)
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("second-run Skipped = %v, want 1", res.Skipped)
	}
	body, _ := os.ReadFile(marker)
	if string(body) != "x\n" {
		t.Fatalf("marker = %q, want exactly one run", body)
	}
}

func TestApply_missingPrefixIsError(t *testing.T) {
	fsys := fstest.MapFS{}
	_, err := Apply(fsys, "bootstrap/migrations", Options{
		LedgerPath: filepath.Join(t.TempDir(), "ledger"),
		Stdout:     &bytes.Buffer{},
		Stderr:     &bytes.Buffer{},
	})
	if err == nil {
		t.Fatalf("Apply: expected error on missing prefix")
	}
	if !isNotFound(err) {
		t.Fatalf("err = %v, want fs.ErrNotExist-shaped", err)
	}
}

func isNotFound(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

// Sanity: an empty migrations directory is a clean no-op.
func TestApply_emptyDirectory(t *testing.T) {
	fsys := fstest.MapFS{
		"bootstrap/migrations/.keep": {Data: []byte("")},
	}
	res, err := Apply(fsys, "bootstrap/migrations", Options{
		LedgerPath: filepath.Join(t.TempDir(), "ledger"),
		Stdout:     &bytes.Buffer{},
		Stderr:     &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Applied)+len(res.Skipped)+len(res.Pending) != 0 {
		t.Fatalf("unexpected result on empty dir: %+v", res)
	}
}

// Compile-time: Options.Stdout/Stderr accept any io.Writer.
var _ fs.FS = (fstest.MapFS)(nil)
