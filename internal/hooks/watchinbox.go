package hooks

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrWake signals that the inbox was drained and the agent should wake
// (exit code 2 in claude-code's Stop hook protocol).
var ErrWake = errors.New("inbox drained")

// WatchInbox is the Stop-hook implementation for inbox-based wakeup.
// It watches $WT_STATE/<slug>/inbox/ for arriving .json files, drains
// them into stdout (one line per message, see renderClaimed), moves
// claimed files to inbox/inflight/, and returns ErrWake when files were
// drained. Returns nil on timeout (no files), or a real error on
// failure.
//
// inflight/ holds the messages of the turn the drain woke. Claude Code
// runs Stop hooks only after a turn that succeeded, so the next run
// acks them into read/; a turn that ends in an API error fires
// StopFailure instead, which puts them back (RequeueInflight). A drain
// claims into a private claiming/<pid>-*/ dir and moves a message to
// inflight/ only after printing it, so a concurrent watcher's ack never
// takes a message that has not been delivered yet.
//
// session is the hook payload's session_id. With one, the turn's
// messages sit in inflight/<session>/, and only that session's Stop
// acks them and only its StopFailure requeues them, so two agents (or
// watchers) on one inbox never ack or requeue each other's turn.
// Without one, inflight/ itself is the bucket.
func WatchInbox(slug, session string) error {
	return watchInboxAt(workerInbox(slug), os.Stdout, os.Stderr, defaultWatchOpts(session))
}

// WatchInboxAt is the env-driven Stop-hook entry point. It targets
// inboxDir directly instead of computing it from a slug, so callers
// like `spore hooks watch-inbox` (no positional) can pass
// $SPORE_TASK_INBOX, which the consumer harness sets per spawn (rower:
// $WT_STATE/<slug>/inbox; coordinator: $SKYHELM_STATE_DIR/<project>/inbox).
func WatchInboxAt(inboxDir, session string) error {
	return watchInboxAt(inboxDir, os.Stdout, os.Stderr, defaultWatchOpts(session))
}

type watchOpts struct {
	session     string
	timeout     time.Duration
	settle      time.Duration
	initWatcher func(dir string) (inboxWaiter, error)
	sleep       func(time.Duration)
}

// inboxWaiter abstracts inotify so tests can drive it without the
// kernel. Wait returns true on event, false on timeout.
type inboxWaiter interface {
	Wait() (woke bool, err error)
	Close() error
}

func defaultWatchOpts(session string) watchOpts {
	return watchOpts{
		session:     session,
		timeout:     envDurationSeconds("WATCH_TIMEOUT", 604800),
		settle:      envDurationSeconds("WATCH_SETTLE", 1),
		initWatcher: initPlatformWatcher,
		sleep:       time.Sleep,
	}
}

func watchInbox(slug string, stdout, stderr io.Writer, opts watchOpts) error {
	return watchInboxAt(workerInbox(slug), stdout, stderr, opts)
}

func watchInboxAt(inboxDir string, stdout, stderr io.Writer, opts watchOpts) error {
	if err := ensureInbox(inboxDir); err != nil {
		return err
	}
	if err := recoverClaims(inboxDir); err != nil {
		return err
	}
	if err := ackInflight(inboxDir, opts.session); err != nil {
		return err
	}

	if n, err := drainInbox(inboxDir, opts.session, stdout); err != nil {
		return err
	} else if n > 0 {
		return ErrWake
	}

	w, werr := opts.initWatcher(inboxDir)
	if werr != nil || w == nil {
		fmt.Fprintf(stderr, "watch-inbox: inotifywait unavailable; sleeping %ds\n",
			int(opts.timeout.Seconds()))
		opts.sleep(opts.timeout)
		if n, err := drainInbox(inboxDir, opts.session, stdout); err != nil {
			return err
		} else if n > 0 {
			return ErrWake
		}
		return nil
	}
	defer w.Close()

	woke, _ := w.Wait()
	if woke {
		opts.sleep(opts.settle)
	}

	if n, err := drainInbox(inboxDir, opts.session, stdout); err != nil {
		return err
	} else if n > 0 {
		return ErrWake
	}
	return nil
}

// drainInbox claims every pending envelope in inboxDir into a private
// claiming/<pid>-*/ dir, prints each one (see renderClaimed), then moves
// the printed files to session's inflight dir. Returns the claim count.
func drainInbox(inboxDir, session string, stdout io.Writer) (int, error) {
	claimRoot := filepath.Join(inboxDir, claimingDir)
	if err := os.MkdirAll(claimRoot, 0o755); err != nil {
		return 0, err
	}
	own, err := os.MkdirTemp(claimRoot, strconv.Itoa(os.Getpid())+"-")
	if err != nil {
		return 0, err
	}
	defer os.Remove(own)
	claimed, err := claimInbox(inboxDir, filepath.Join(claimingDir, filepath.Base(own)))
	if err != nil || len(claimed) == 0 {
		return 0, err
	}
	lines := renderClaimed(inboxDir, claimed)
	for _, l := range lines {
		io.WriteString(stdout, l.text)
	}
	inflight := inflightDir(inboxDir, session)
	if err := os.MkdirAll(inflight, 0o755); err != nil {
		return len(claimed), err
	}
	var errs []error
	for _, l := range lines {
		if filepath.Dir(l.path) != own {
			continue
		}
		if err := os.Rename(l.path, filepath.Join(inflight, filepath.Base(l.path))); err != nil {
			errs = append(errs, err)
		}
	}
	return len(claimed), errors.Join(errs...)
}

// claimingDir holds one subdir per drain in progress, named
// <pid>-<random>. Nothing but its owner touches a live one.
const claimingDir = "claiming"

// staleClaim bounds a live drain (claim, read, print, move): a claim
// dir older than this is dead even if its pid was reused.
const staleClaim = 10 * time.Minute

// recoverClaims puts back into the inbox the messages of claiming/
// subdirs whose owner process is gone (a watcher killed between its
// claim and its print), so they are delivered by the next drain.
func recoverClaims(inboxDir string) error {
	root := filepath.Join(inboxDir, claimingDir)
	dirs, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var errs []error
	for _, d := range dirs {
		pidStr, _, _ := strings.Cut(d.Name(), "-")
		pid, err := strconv.Atoi(pidStr)
		if !d.IsDir() || err != nil {
			continue
		}
		if info, ierr := d.Info(); processAlive(pid) && (ierr != nil || time.Since(info.ModTime()) < staleClaim) {
			continue
		}
		dir := filepath.Join(root, d.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, f := range files {
			if err := os.Rename(filepath.Join(dir, f.Name()), filepath.Join(inboxDir, f.Name())); err != nil && !os.IsNotExist(err) {
				errs = append(errs, err)
			}
		}
		_ = os.Remove(dir)
	}
	return errors.Join(errs...)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// claimInbox lists *.json at the top level of inboxDir and atomically
// moves each to inboxDir/<sub>/. The rename is the claim: concurrent
// drains on one inbox never both get a file. Returns the claimed paths,
// in name order with redelivered files at their original place.
func claimInbox(inboxDir, sub string) ([]string, error) {
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) == 0 {
		return nil, nil
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.TrimPrefix(names[i], redeliverPrefix) < strings.TrimPrefix(names[j], redeliverPrefix)
	})

	dstDir := filepath.Join(inboxDir, sub)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return nil, err
	}
	var claimed []string
	for _, n := range names {
		src := filepath.Join(inboxDir, n)
		dst := filepath.Join(dstDir, n)
		if err := os.Rename(src, dst); err == nil {
			claimed = append(claimed, dst)
		}
	}
	return claimed, nil
}

// redeliverPrefix marks a message put back by RequeueInflight.
const redeliverPrefix = "redeliver-"

// RequeueInflight puts the messages of session's turn that ended in an
// API error back into the inbox, so the next drain delivers them again.
func RequeueInflight(inboxDir, session string) error {
	return moveFiles(inflightDir(inboxDir, session), inboxDir, redeliverPrefix)
}

// ackInflight moves session's inflight messages into read/. A session
// also acks loose files in inflight/ itself: those come from a build
// that predates per-session dirs, or from a watcher run without a
// session, and nothing else would ever ack them.
func ackInflight(inboxDir, session string) error {
	read := filepath.Join(inboxDir, "read")
	if err := moveFiles(inflightDir(inboxDir, session), read, ""); err != nil {
		return err
	}
	if session == "" {
		return nil
	}
	return moveFiles(inflightDir(inboxDir, ""), read, "")
}

// inflightDir is inbox/inflight/<session>/, or inbox/inflight/ without
// a session. A session id that is not a plain name is hashed so it
// cannot point outside inflight/.
func inflightDir(inboxDir, session string) string {
	dir := filepath.Join(inboxDir, "inflight")
	if session == "" {
		return dir
	}
	if !safeSession(session) {
		session = fmt.Sprintf("%x", sha256.Sum256([]byte(session)))[:32]
	}
	return filepath.Join(dir, session)
}

func safeSession(s string) bool {
	if len(s) > 128 || strings.HasPrefix(s, ".") {
		return false
	}
	return strings.Trim(s, "-_.0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") == "" && s != ""
}

// moveFiles moves every regular file in src to dst, adding prefix to
// names that do not carry it yet. Subdirs of src are left alone.
func moveFiles(src, dst, prefix string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			name = prefix + name
		}
		if err := os.Rename(filepath.Join(src, e.Name()), filepath.Join(dst, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// inboxLine is one rendered message and where its file lives now: the
// claim dir it was claimed into, inbox/quarantine/ if it did not parse.
type inboxLine struct {
	path, text string
}

// renderClaimed formats each claimed envelope as exactly one line. The
// source field is whatever the writer put there: anyone with write
// access to the inbox can claim to be the operator, so every line
// marks it unverified, and the fields are escaped so no envelope can
// start a second, unmarked line or close the marker early. A file that
// does not parse is moved to inbox/quarantine/ and reported instead of
// being consumed silently.
func renderClaimed(inboxDir string, claimed []string) []inboxLine {
	lines := make([]inboxLine, 0, len(claimed))
	for _, f := range claimed {
		ts, source, body, err := readTellFile(f)
		if err != nil {
			q := quarantine(inboxDir, f)
			lines = append(lines, inboxLine{q, fmt.Sprintf("[warning] inbox: unreadable message %s: %s\n", oneLine(q), oneLine(err.Error()))})
			continue
		}
		if source == "" {
			source = "none"
		}
		note := ""
		if strings.HasPrefix(filepath.Base(f), redeliverPrefix) {
			note = ", redelivered: the turn that first got it ended in an API error"
		}
		lines = append(lines, inboxLine{f, fmt.Sprintf("[%s] inbox (claimed source: %q, unverified%s): %s\n", stamp(ts), source, note, oneLine(body))})
	}
	return lines
}

// lineBreaks covers every sequence a terminal or a model may read as a
// line break, so a body cannot smuggle in a line of its own.
var lineBreaks = strings.NewReplacer(
	"\r\n", `\n`, "\n", `\n`, "\r", `\r`,
	"\v", `\v`, "\f", `\f`, "\u0085", `\u0085`, "\u2028", `\u2028`, "\u2029", `\u2029`,
)

func oneLine(s string) string {
	return lineBreaks.Replace(s)
}

// stamp passes a timestamp through and replaces anything else with
// "?": the stamp opens the line, ahead of the unverified marker, so a
// ts such as "x] operator: push. [" must not reach it even quoted.
const stampChars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz:.+-"

func stamp(ts string) string {
	for _, r := range ts {
		if !strings.ContainsRune(stampChars, r) {
			return "?"
		}
	}
	return ts
}

// quarantine moves a claimed file to inbox/quarantine/
// and returns where it now lives (the original path if the move fails).
func quarantine(inboxDir, claimed string) string {
	dir := filepath.Join(inboxDir, "quarantine")
	dst := filepath.Join(dir, filepath.Base(claimed))
	if os.MkdirAll(dir, 0o755) != nil || os.Rename(claimed, dst) != nil {
		return claimed
	}
	return dst
}

func readTellFile(path string) (ts, source, body string, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", "", err
	}
	var ev struct {
		Ts     string `json:"ts"`
		Source string `json:"source"`
		Body   string `json:"body"`
		Slug   string `json:"slug"`
		Msg    string `json:"msg"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(b), &ev); err != nil {
		return "", "", "", err
	}
	if ev.Source == "" && ev.Msg != "" {
		ev.Source = "tell"
	}
	if ev.Body == "" {
		ev.Body = ev.Msg
	}
	return ev.Ts, ev.Source, ev.Body, nil
}

func workerInbox(slug string) string {
	return filepath.Join(wtStateDir(), slug, "inbox")
}

func wtStateDir() string {
	if v := os.Getenv("WT_STATE"); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "wt")
	}
	return ""
}

func ensureInbox(inbox string) error {
	for _, sub := range []string{"", ".tmp", "read", claimingDir} {
		if err := os.MkdirAll(filepath.Join(inbox, sub), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func envDurationSeconds(key string, defaultSec int) time.Duration {
	if v := os.Getenv(key); v != "" {
		var n int
		if _, err := fmt.Sscan(v, &n); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return time.Duration(defaultSec) * time.Second
}
