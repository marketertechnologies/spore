package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Codex has no asyncRewake: no hook can wake an idle thread. The wake
// path is a sidecar (WatchInboxCodex) that pushes inbox envelopes
// through `codex queue --thread`, which starts a turn on an idle
// thread or queues behind a running one. It needs the thread id, which
// hooks only see as the payload session_id, so a SessionStart hook
// (RecordCodexThread) leaves it next to the inbox.
//
// Records are keyed by the agent session (CodexSessionKey), never by a
// fixed coordinator name, so parallel coordinators sharing one inbox
// dir keep separate thread records.

// CodexThread is the on-disk thread record for one agent session.
type CodexThread struct {
	ThreadID       string `json:"thread_id"`
	Source         string `json:"source,omitempty"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	TS             string `json:"ts"`
}

// ErrNoCodexThread means no SessionStart has recorded a thread id for
// the session yet.
var ErrNoCodexThread = errors.New("no codex thread recorded")

// CodexSessionKey names the agent session a thread record belongs to:
// $SPORE_COORDINATOR_SESSION when set (the launch shim exports it), else
// the tmux session of $TMUX_PANE. Returns "" outside both.
func CodexSessionKey() string {
	if k := os.Getenv("SPORE_COORDINATOR_SESSION"); k != "" {
		return k
	}
	if os.Getenv("TMUX") == "" {
		return ""
	}
	args := []string{"display-message", "-p"}
	if p := os.Getenv("TMUX_PANE"); p != "" {
		args = append(args, "-t", p)
	}
	out, err := exec.Command("tmux", append(args, "#S")...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// CodexThreadFile returns <inbox>/../codex-threads/<key>.json. For a
// coordinator that is <coordinator state dir>/<project>/codex-threads/.
func CodexThreadFile(inbox, key string) string {
	safe := strings.NewReplacer("/", "_", string(filepath.Separator), "_").Replace(key)
	return filepath.Join(filepath.Dir(filepath.Clean(inbox)), "codex-threads", safe+".json")
}

// RecordCodexThread writes req.SessionID as the thread for key. Every
// SessionStart source overwrites it: /clear and fork move the session
// onto a new thread, and the sidecar must follow.
func RecordCodexThread(inbox, key string, req Request) error {
	if req.SessionID == "" {
		return nil
	}
	path := CodexThreadFile(inbox, key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(CodexThread{
		ThreadID:       req.SessionID,
		Source:         req.Source,
		TranscriptPath: req.TranscriptPath,
		TS:             time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".thread-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ReadCodexThread returns the recorded thread id for key.
func ReadCodexThread(inbox, key string) (string, error) {
	b, err := os.ReadFile(CodexThreadFile(inbox, key))
	if os.IsNotExist(err) {
		return "", ErrNoCodexThread
	}
	if err != nil {
		return "", err
	}
	var rec CodexThread
	if err := json.Unmarshal(b, &rec); err != nil {
		return "", fmt.Errorf("codex thread record: %w", err)
	}
	if rec.ThreadID == "" {
		return "", ErrNoCodexThread
	}
	return rec.ThreadID, nil
}

// CodexBin is the codex executable the sidecar runs: $SPORE_CODEX_BIN,
// else "codex" on PATH.
func CodexBin() string {
	if b := os.Getenv("SPORE_CODEX_BIN"); b != "" {
		return b
	}
	return "codex"
}

// maxCodexMessage keeps one `codex queue --message` argv string under
// Linux's 128 KiB per-argument limit (MAX_ARG_STRLEN).
const maxCodexMessage = 96 * 1024

type codexWatchOpts struct {
	poll        time.Duration
	settle      time.Duration
	retry       time.Duration
	initWatcher func(dir string) (inboxWaiter, error)
	sleep       func(time.Duration)
	queue       func(ctx context.Context, thread, msg string) error
	alive       func() bool
	stderr      io.Writer
}

func defaultCodexWatchOpts(alive func() bool) codexWatchOpts {
	poll := envDurationSeconds("WATCH_POLL", 30)
	return codexWatchOpts{
		poll:        poll,
		settle:      envDurationSeconds("WATCH_SETTLE", 1),
		retry:       5 * time.Second,
		initWatcher: func(dir string) (inboxWaiter, error) { return newPlatformWatcher(dir, poll) },
		sleep:       time.Sleep,
		queue:       codexQueue,
		alive:       alive,
		stderr:      os.Stderr,
	}
}

func codexQueue(ctx context.Context, thread, msg string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, CodexBin(), "queue", "--thread", thread, "--message", msg).CombinedOutput()
	if err != nil {
		return fmt.Errorf("codex queue: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// WatchInboxCodex runs until ctx ends or alive (when non-nil) reports
// false, delivering each envelope that lands in inbox to the codex
// thread recorded for key. Envelopes are claimed with the same atomic
// rename as the Claude drain, so a file reaches exactly one drain; a
// failed `codex queue` puts the batch back for the next pass.
func WatchInboxCodex(ctx context.Context, inbox, key string, alive func() bool) error {
	return watchInboxCodex(ctx, inbox, key, defaultCodexWatchOpts(alive))
}

func watchInboxCodex(ctx context.Context, inbox, key string, o codexWatchOpts) error {
	if err := ensureInbox(inbox); err != nil {
		return err
	}
	w, err := o.initWatcher(inbox)
	if err != nil || w == nil {
		fmt.Fprintf(o.stderr, "watch-inbox --codex: inotify unavailable; polling every %s\n", o.poll)
		w = nil
	} else {
		defer w.Close()
	}

	for {
		if ctx.Err() != nil || (o.alive != nil && !o.alive()) {
			return nil
		}
		err := deliverCodex(ctx, inbox, key, o)
		switch {
		case errors.Is(err, ErrNoCodexThread):
			o.sleep(o.retry)
			continue
		case err != nil:
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintln(o.stderr, "watch-inbox --codex:", err)
			o.sleep(o.retry)
			continue
		}
		if w == nil {
			o.sleep(o.poll)
			continue
		}
		if woke, _ := w.Wait(); woke {
			o.sleep(o.settle)
		}
	}
}

// deliverCodex claims pending envelopes and queues them in batches
// that fit one argv string. Nothing is claimed until a thread is known.
func deliverCodex(ctx context.Context, inbox, key string, o codexWatchOpts) error {
	thread, err := ReadCodexThread(inbox, key)
	if err != nil {
		return err
	}
	claimed, err := claimInbox(inbox)
	if err != nil || len(claimed) == 0 {
		return err
	}
	for start := 0; start < len(claimed); {
		end, msg := start, ""
		for end < len(claimed) {
			next := msg + renderClaimed(claimed[end:end+1])
			if end > start && len(next) > maxCodexMessage {
				break
			}
			msg, end = next, end+1
		}
		msg = strings.TrimRight(msg, "\n")
		if len(msg) > maxCodexMessage {
			msg = msg[:maxCodexMessage] + " [truncated; full envelope in inbox/read/]"
		}
		if err := o.queue(ctx, thread, msg); err != nil {
			unclaim(inbox, claimed[start:])
			return err
		}
		start = end
	}
	return nil
}

func unclaim(inbox string, claimed []string) {
	for _, f := range claimed {
		_ = os.Rename(f, filepath.Join(inbox, filepath.Base(f)))
	}
}
