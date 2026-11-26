package events

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	return dir
}

func TestAppendAndRead(t *testing.T) {
	dir := sandbox(t)
	t0 := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	defer SetClock(func() time.Time { return t0 })()

	Record("proj", Event{Kind: TaskStart, Slug: "a"})
	Record("proj", Event{Kind: PROpened, Slug: "a", TS: t0.Add(time.Hour), Fields: map[string]string{"url": "https://github.com/o/r/pull/7", "empty": ""}})

	raw, err := os.ReadFile(filepath.Join(dir, "spore", "proj", FileName))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), "\n"); n != 2 {
		t.Fatalf("want 2 lines, got %d:\n%s", n, raw)
	}
	evs, err := Read("proj", t0.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != PROpened || evs[0].Project != "proj" || evs[0].Fields["url"] == "" {
		t.Fatalf("since filter: %+v", evs)
	}
	if _, ok := evs[0].Fields["empty"]; ok {
		t.Fatalf("empty field kept: %+v", evs[0].Fields)
	}
}

func TestRotateBoundsSize(t *testing.T) {
	sandbox(t)
	old := MaxBytes
	MaxBytes = 400
	defer func() { MaxBytes = old }()

	for i := 0; i < 50; i++ {
		Record("proj", Event{Kind: WorkerSpawn, Slug: "slug", Detail: strings.Repeat("x", 50)})
	}
	path, _ := Path("proj")
	for _, p := range []string{path, path + ".1"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if fi.Size() > MaxBytes {
			t.Fatalf("%s is %d bytes, over %d", p, fi.Size(), MaxBytes)
		}
	}
	if _, err := os.Stat(path + ".2"); err == nil {
		t.Fatal("more than two generations kept")
	}
	evs, err := Read("proj", time.Time{})
	if err != nil || len(evs) == 0 {
		t.Fatalf("read after rotate: %d events, %v", len(evs), err)
	}
}

func TestScrubRedactsSecrets(t *testing.T) {
	sandbox(t)
	cases := []string{
		"push failed: ghp_abcdefghijklmnopqrstuvwxyz0123",
		"GITHUB_TOKEN=github_pat_11AAAAAAAAAAAAAAAAAAAAAA_bbbbbbbbbbbbbbbb",
		"key sk-ant-api03-AAAAAAAAAAAAAAAAAAAA",
		"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload",
		"clone https://x-access-token:s3cr3tvalue@github.com/o/r",
		"LINEAR_API_KEY=lin_api_123456789",
	}
	secrets := []string{"ghp_abcdef", "github_pat_11", "sk-ant-api03", "eyJhbGci", "s3cr3tvalue", "lin_api_123"}
	for i, c := range cases {
		Record("proj", Event{Kind: ReconcileFailed, Detail: c, Fields: map[string]string{"err": c}})
		got := Scrub(c, 300)
		if strings.Contains(got, secrets[i]) {
			t.Errorf("Scrub(%q) = %q, secret kept", c, got)
		}
	}
	path, _ := Path("proj")
	raw, _ := os.ReadFile(path)
	for _, s := range secrets {
		if strings.Contains(string(raw), s) {
			t.Fatalf("ledger holds %q:\n%s", s, raw)
		}
	}
}

func TestScrubFlattensAndCaps(t *testing.T) {
	got := Scrub("line one\nline two\t"+strings.Repeat("y", 400), 50)
	if strings.Contains(got, "\n") || len(got) > 53 || !strings.HasPrefix(got, "line one line two") {
		t.Fatalf("Scrub = %q", got)
	}
}

func TestRecordThrottled(t *testing.T) {
	sandbox(t)
	now := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	defer SetClock(func() time.Time { return now })()
	for i := 0; i < 5; i++ {
		RecordThrottled("proj", "skip-locked", time.Hour, Event{Kind: ReconcileSkipped, Detail: "locked"})
	}
	now = now.Add(2 * time.Hour)
	RecordThrottled("proj", "skip-locked", time.Hour, Event{Kind: ReconcileSkipped, Detail: "locked"})
	evs, _ := Read("proj", time.Time{})
	if len(evs) != 2 {
		t.Fatalf("want 2 throttled events, got %d", len(evs))
	}
}

func TestPathRejectsBadProject(t *testing.T) {
	sandbox(t)
	for _, p := range []string{"", "..", "a/b"} {
		if err := Append(p, Event{Kind: TaskStart}); err == nil {
			t.Errorf("Append(%q) accepted", p)
		}
	}
}

func TestProjectsAndRoot(t *testing.T) {
	sandbox(t)
	Record("b", Event{Kind: TaskStart})
	Record("a", Event{Kind: TaskStart})
	NoteRoot("a", "/srv/a")
	ps, err := Projects()
	if err != nil || strings.Join(ps, ",") != "a,b" {
		t.Fatalf("Projects = %v, %v", ps, err)
	}
	if got := ProjectRoot("a"); got != "/srv/a" {
		t.Fatalf("ProjectRoot = %q", got)
	}
}
