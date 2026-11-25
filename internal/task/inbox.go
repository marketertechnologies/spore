package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Tell appends a JSON message envelope to the per-slug inbox dir under
// StateDir, or to the coordinator inbox (CoordinatorInboxDirForProject)
// for slug "coordinator", which is not a per-project task slug. Filename is "<unix-nanos>.json", with a numeric suffix on
// collision; payload schema is {"slug","ts","msg"}. No agent wake
// side effect: the Stop-hook drain that turns inbox writes into
// resumed turns is downstream consumer territory.
func Tell(slug, msg string) error {
	dir, err := tellInboxDir(slug)
	if err != nil {
		return err
	}
	return TellDir(dir, slug, msg)
}

// TellDir is Tell against an explicit inbox dir, for callers that
// read the inbox from a session env rather than the cwd project.
func TellDir(dir, slug, msg string) error {
	now := time.Now()
	payload := struct {
		Slug string `json:"slug"`
		TS   string `json:"ts"`
		Msg  string `json:"msg"`
	}{Slug: slug, TS: now.UTC().Format(time.RFC3339), Msg: msg}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return writeUniqueInboxFile(dir, now.UnixNano(), b)
}

func tellInboxDir(slug string) (string, error) {
	if slug == "coordinator" {
		return CoordinatorInboxDirForProject("")
	}
	return InboxDir(slug)
}

// writeUniqueInboxFile writes body under dir/.tmp and hard-links it to
// the first free "<stamp>[-n].json" in dir. The link is the publish: a
// watcher never sees the final name before the body is complete, and
// link fails with EEXIST instead of clobbering a colliding message.
func writeUniqueInboxFile(dir string, stamp int64, body []byte) error {
	tmpDir := filepath.Join(dir, ".tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(tmpDir, fmt.Sprintf("%d-*.json", stamp))
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	for n := 0; n < 1000; n++ {
		name := fmt.Sprintf("%d.json", stamp)
		if n > 0 {
			name = fmt.Sprintf("%d-%d.json", stamp, n)
		}
		err := os.Link(tmp.Name(), filepath.Join(dir, name))
		if os.IsExist(err) {
			continue
		}
		return err
	}
	return fmt.Errorf("inbox: no free message filename for stamp %d", stamp)
}
