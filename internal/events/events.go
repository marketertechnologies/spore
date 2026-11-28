// Package events is the per-project append-only ledger that `spore
// report` reads: `<state dir>/spore/<project>/events.jsonl`, one JSON
// object per line. Writers are best-effort: a ledger failure never
// fails the caller. The file rotates to events.jsonl.1 past MaxBytes,
// so at most two generations exist.
//
// The package imports nothing from spore so internal/task can write
// to it; callers pass the project name (the main repo's basename,
// task.ProjectName).
package events

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Kinds. Keep in step with docs/report.md.
const (
	TaskStart          = "task.start"
	TaskPause          = "task.pause"
	TaskBlock          = "task.block"
	TaskDone           = "task.done"
	WorkerSpawn        = "worker.spawn"
	WorkerRespawn      = "worker.respawn"
	WorkerCrashLoop    = "worker.crashloop"
	CoordinatorSpawn   = "coordinator.spawn"
	CoordinatorRespawn = "coordinator.respawn"
	CoordinatorCrash   = "coordinator.crashloop"
	RoleRespawn        = "role.respawn"
	RoleCrashLoop      = "role.crashloop"
	RolePhase          = "role.phase"
	RoleVerdict        = "role.verdict"
	Alert              = "alert"
	ReconcileSkipped   = "reconcile.skipped"
	ReconcileFailed    = "reconcile.failed"
	PROpened           = "pr.opened"
	Redaction          = "redaction"
)

// FileName is the ledger's name inside the project state dir.
const FileName = "events.jsonl"

// RootFile holds the project's main repo path, so `spore report
// --all` can read live task state for every project with a ledger.
const RootFile = "project-root"

// MaxBytes is the rotation threshold. A line is a few hundred bytes,
// so one generation holds thousands of events: weeks on a busy host.
var MaxBytes int64 = 1 << 20

const (
	maxDetail = 300
	maxField  = 200
)

// Event is one ledger line. Detail and Fields carry short operational
// text (reasons, phases, URLs); never prompt text or secret values.
// Record scrubs both as a backstop.
type Event struct {
	TS      time.Time         `json:"ts"`
	Kind    string            `json:"kind"`
	Project string            `json:"project,omitempty"`
	Slug    string            `json:"slug,omitempty"`
	Detail  string            `json:"detail,omitempty"`
	Fields  map[string]string `json:"fields,omitempty"`
}

var nowFunc = time.Now

// SetClock swaps the clock for tests; it returns a restore func.
func SetClock(f func() time.Time) func() {
	old := nowFunc
	nowFunc = f
	return func() { nowFunc = old }
}

// BaseDir is "$XDG_STATE_HOME/spore", else "$HOME/.local/state/spore".
// It matches task.StateDirForProject's parent.
func BaseDir() (string, error) {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "spore"), nil
	}
	home := os.Getenv("HOME")
	if home == "" {
		return "", errors.New("events: HOME and XDG_STATE_HOME both unset")
	}
	return filepath.Join(home, ".local", "state", "spore"), nil
}

// Path returns the ledger path for project.
func Path(project string) (string, error) {
	if project == "" || strings.ContainsAny(project, `/\`) || project == "." || project == ".." {
		return "", fmt.Errorf("events: bad project name %q", project)
	}
	base, err := BaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, project, FileName), nil
}

// Record appends e to project's ledger, stamping TS and Project.
// Failures go to stderr; Record never returns an error so a call site
// stays one line.
func Record(project string, e Event) {
	if err := Append(project, e); err != nil {
		fmt.Fprintf(os.Stderr, "events: %v\n", err)
	}
}

// Append is Record with the error returned.
func Append(project string, e Event) error {
	path, err := Path(project)
	if err != nil {
		return err
	}
	if e.Kind == "" {
		return errors.New("events: empty kind")
	}
	if e.TS.IsZero() {
		e.TS = nowFunc()
	}
	e.TS = e.TS.UTC().Truncate(time.Second)
	e.Project = project
	e.Detail = Scrub(e.Detail, maxDetail)
	if len(e.Fields) > 0 {
		clean := make(map[string]string, len(e.Fields))
		for k, v := range e.Fields {
			if v = Scrub(v, maxField); v != "" {
				clean[Scrub(k, 40)] = v
			}
		}
		e.Fields = clean
	}
	if len(e.Fields) == 0 {
		e.Fields = nil
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	unlock, err := lock(path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	if fi, err := os.Stat(path); err == nil && fi.Size()+int64(len(line)) > MaxBytes && fi.Size() > 0 {
		if err := os.Rename(path, path+".1"); err != nil {
			return fmt.Errorf("rotate %s: %w", path, err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// RecordThrottled records e at most once per every for key, so a
// condition that repeats on every pass (a held lock, a disabled fleet)
// leaves one line per window instead of one per minute.
func RecordThrottled(project, key string, every time.Duration, e Event) {
	path, err := Path(project)
	if err != nil {
		fmt.Fprintf(os.Stderr, "events: %v\n", err)
		return
	}
	stamp := filepath.Join(filepath.Dir(path), "events.throttle", safeKey(key))
	now := nowFunc()
	if fi, err := os.Stat(stamp); err == nil && now.Sub(fi.ModTime()) < every {
		return
	}
	if err := os.MkdirAll(filepath.Dir(stamp), 0o755); err == nil {
		if err := os.WriteFile(stamp, nil, 0o644); err == nil {
			_ = os.Chtimes(stamp, now, now)
		}
	}
	Record(project, e)
}

// NoteRoot writes project's main repo path next to its ledger when it
// is missing or differs.
func NoteRoot(project, root string) {
	path, err := Path(project)
	if err != nil || root == "" {
		return
	}
	file := filepath.Join(filepath.Dir(path), RootFile)
	if b, err := os.ReadFile(file); err == nil && strings.TrimSpace(string(b)) == root {
		return
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(file, []byte(root+"\n"), 0o644)
}

// ProjectRoot returns the path NoteRoot recorded for project, or "".
func ProjectRoot(project string) string {
	path, err := Path(project)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(path), RootFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Projects lists the project names that have a ledger, sorted.
func Projects() ([]string, error) {
	base, err := BaseDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(base, e.Name(), FileName)); err == nil {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// Read returns project's events at or after since, oldest first,
// across both generations. Lines that fail to parse are skipped.
func Read(project string, since time.Time) ([]Event, error) {
	path, err := Path(project)
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, p := range []string{path + ".1", path} {
		evs, err := readFile(p, since)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS.Before(out[j].TS) })
	return out, nil
}

func readFile(path string, since time.Time) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Kind == "" {
			continue
		}
		if !e.TS.Before(since) {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

func lock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

var unsafeKey = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safeKey(k string) string {
	k = unsafeKey.ReplaceAllString(k, "_")
	if k == "" || k == "." || k == ".." {
		return "_"
	}
	return k
}

// secretPatterns match token shapes that must never reach the ledger
// even if a caller passes an error message that embeds one.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(gh[pousr]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{20,})`),
	regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{10,}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`(?i)\b(bearer|token|authorization)[:= ]+[A-Za-z0-9._~+/=-]{12,}`),
	regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|API_KEY|PRIVATE_KEY))=\S+`),
	regexp.MustCompile(`(?i)(https?://)[^/\s:@]+:[^/\s@]+@`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
}

// Scrub flattens s to one line, redacts token shapes and caps it at
// max bytes.
func Scrub(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	for _, re := range secretPatterns {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			if sub := re.FindStringSubmatch(m); len(sub) > 1 && strings.HasPrefix(sub[1], "http") {
				return sub[1] + "[redacted]@"
			}
			if i := strings.IndexAny(m, ":= "); i > 0 && i < len(m)-1 && !strings.HasPrefix(m, "-----") {
				return m[:i+1] + "[redacted]"
			}
			return "[redacted]"
		})
	}
	if len(s) > max {
		cut := max
		for cut > 0 && (s[cut]&0xC0) == 0x80 {
			cut--
		}
		s = s[:cut] + "..."
	}
	return s
}
