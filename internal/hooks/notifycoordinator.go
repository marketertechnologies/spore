package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/versality/spore/internal/task"
)

// NotifyCoordinator writes a poke file into the coordinator's project
// inbox at <task.CoordinatorStateDir>/<slug>/inbox/. The poke is a
// JSON file following the tell protocol ({ts, source, body}), written
// atomically via .tmp.
func NotifyCoordinator(slug string) error {
	inbox, err := coordinatorInbox(slug)
	if err != nil {
		return err
	}
	return notifyCoordinatorAt(inbox, "notification", "poke")
}

// NotifyCoordinatorEvent writes a tell envelope with the given source
// and body into the coordinator inbox for project. Unlike
// NotifyCoordinatorEnv it never skips self-pokes: callers that drive a
// transition from inside the coordinator's own hooks still need the
// envelope to land (their own idempotency guard prevents repeats).
func NotifyCoordinatorEvent(project, source, body string) error {
	inbox, err := coordinatorInbox(project)
	if err != nil {
		return err
	}
	return notifyCoordinatorAt(inbox, source, body)
}

// NotifyDedupWindow is how long one worker's notification suppresses
// repeats with the same type and message.
const NotifyDedupWindow = 5 * time.Minute

// NotifyBurstWindow replaces NotifyDedupWindow for blockingTypes. Their
// message names only the tool ("Claude needs your permission to use
// Bash"), so two separate Bash approvals look identical; only a true
// burst (duplicates seconds apart) may collapse.
const NotifyBurstWindow = 30 * time.Second

// blockingTypes are requests the worker waits on until someone answers.
var blockingTypes = map[string]bool{
	"permission_prompt":      true,
	"elicitation_dialog":     true,
	"elicitation_url_dialog": true,
	"agent_needs_input":      true,
}

func dedupWindow(kind string) time.Duration {
	if blockingTypes[kind] {
		return NotifyBurstWindow
	}
	return NotifyDedupWindow
}

// NotificationInput is the part of the Claude Code Notification hook
// stdin payload the hook reads.
type NotificationInput struct {
	NotificationType string `json:"notification_type"`
	Message          string `json:"message"`
}

// infoOnlyTypes are notification_type values that report something
// already finished or the plain idle reminder: nothing for the
// coordinator to act on. Any other type, including ones Claude Code
// adds later, is forwarded.
var infoOnlyTypes = map[string]bool{
	"idle_prompt":             true,
	"auth_success":            true,
	"elicitation_complete":    true,
	"elicitation_response":    true,
	"agent_completed":         true,
	"quota_auto_resume_fired": true,
	"plugin_notification":     true,
}

// idleMessage matches the plain idle reminder text, for Claude Code
// versions whose payload carries no notification_type.
var idleMessage = regexp.MustCompile(`(?i)^claude is waiting for your input\.?$`)

// NotifyCoordinatorEnv is the env-driven entry point for the
// Notification hook, which the managed drop-in installs in every pane
// on the host. It forwards a worker's notification to the coordinator
// inbox of $WT_PROJECT with the worker slug and the message text, and
// drops everything that carries nothing to act on:
//
//   - WT_PROJECT unset (not a spore session);
//   - SPORE_ROLE set: role panes are driven by the role loop, which
//     already reports to the coordinator;
//   - SPORE_TASK_INBOX equal to the coordinator inbox (the coordinator
//     itself);
//   - the plain idle reminder and other info-only types (infoOnlyTypes),
//     or an empty payload;
//   - a repeat of the same type and message from the same worker within
//     dedupWindow (recorded only after the inbox write succeeds).
func NotifyCoordinatorEnv(stdin io.Reader) error {
	project := os.Getenv("WT_PROJECT")
	if project == "" || os.Getenv("SPORE_ROLE") != "" {
		return nil
	}
	inbox, err := coordinatorInbox(project)
	if err != nil {
		return err
	}
	if os.Getenv("SPORE_TASK_INBOX") == inbox {
		return nil
	}
	var in NotificationInput
	if stdin != nil {
		if b, err := io.ReadAll(io.LimitReader(stdin, 1<<20)); err == nil && len(strings.TrimSpace(string(b))) > 0 {
			_ = json.Unmarshal(b, &in)
		}
	}
	in.Message = strings.TrimSpace(in.Message)
	if !needsAttention(in) {
		return nil
	}
	kind := in.NotificationType
	if kind == "" {
		kind = "notification"
	}
	slug := notifySenderSlug()
	stamp := notifyStamp(filepath.Dir(inbox), slug, kind, in.Message)
	now := time.Now()
	if sentRecently(stamp, now, dedupWindow(kind)) {
		return nil
	}
	body := fmt.Sprintf("worker %s: %s", slug, kind)
	if in.Message != "" {
		body += ": " + in.Message
	}
	if err := notifyCoordinatorAt(inbox, "notification", body); err != nil {
		return err
	}
	markSent(stamp, now)
	return nil
}

func needsAttention(in NotificationInput) bool {
	if infoOnlyTypes[in.NotificationType] {
		return false
	}
	if in.NotificationType == "" {
		return in.Message != "" && !idleMessage.MatchString(in.Message)
	}
	return true
}

func notifySenderSlug() string {
	if s := os.Getenv("SPORE_TASK_SLUG"); s != "" {
		return s
	}
	if inbox := os.Getenv("SPORE_TASK_INBOX"); inbox != "" {
		return filepath.Base(filepath.Dir(inbox))
	}
	return "unknown"
}

var unsafeStampChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// notifyStamp names the dedup stamp for one worker, type and message.
// The message is part of the key so an identical burst collapses but a
// second, different prompt (another tool's approval) still gets through.
func notifyStamp(projectState, slug, kind, message string) string {
	sum := sha256.Sum256([]byte(message))
	name := unsafeStampChars.ReplaceAllString(slug+"."+kind, "_") + "." + hex.EncodeToString(sum[:6])
	return filepath.Join(projectState, "notify-stamps", name)
}

// sentRecently reports whether stamp records a send within window of
// now. Two hooks racing inside one window can both
// pass, which is fine for a rate limit that only has to collapse bursts.
func sentRecently(stamp string, now time.Time, window time.Duration) bool {
	fi, err := os.Stat(stamp)
	if err != nil {
		return false
	}
	age := now.Sub(fi.ModTime())
	return age >= 0 && age < window
}

// notifyStampMaxAge is well past the longest dedup window, so pruning
// never drops a stamp that still suppresses anything.
const notifyStampMaxAge = time.Hour

// markSent records a send at now and prunes stamps past
// notifyStampMaxAge. Failures only weaken the dedup, so they are ignored.
func markSent(stamp string, now time.Time) {
	dir := filepath.Dir(stamp)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	if err := os.WriteFile(stamp, nil, 0o600); err == nil {
		_ = os.Chtimes(stamp, now, now)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > notifyStampMaxAge {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func notifyCoordinatorAt(inbox, source, body string) error {
	if err := ensureInbox(inbox); err != nil {
		return fmt.Errorf("notify-coordinator: ensure inbox: %w", err)
	}

	poke := tellEvent{
		Ts:     time.Now().Format("2006-01-02T15:04:05-07:00"),
		Source: source,
		Body:   body,
	}
	b, err := json.Marshal(poke)
	if err != nil {
		return err
	}
	b = append(b, '\n')

	name := fmt.Sprintf("%d-%d-%d.json", time.Now().UnixMilli(), os.Getpid(), notifySeq.Add(1))
	tmp := filepath.Join(inbox, ".tmp", name)
	dst := filepath.Join(inbox, name)

	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("notify-coordinator: write tmp: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("notify-coordinator: rename: %w", err)
	}
	return nil
}

// notifySeq keeps two notices from one process in the same millisecond
// from landing on the same inbox file name.
var notifySeq atomic.Uint64

type tellEvent struct {
	Ts     string `json:"ts"`
	Source string `json:"source"`
	Body   string `json:"body"`
}

func coordinatorInbox(slug string) (string, error) {
	root, err := task.CoordinatorStateDir()
	if err != nil {
		return "", fmt.Errorf("notify-coordinator: %w", err)
	}
	return filepath.Join(root, slug, "inbox"), nil
}
