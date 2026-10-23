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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
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

func writeUniqueInboxFile(dir string, stamp int64, body []byte) error {
	for n := 0; n < 1000; n++ {
		name := fmt.Sprintf("%d.json", stamp)
		if n > 0 {
			name = fmt.Sprintf("%d-%d.json", stamp, n)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := f.Write(body); err != nil {
			_ = f.Close()
			return err
		}
		return f.Close()
	}
	return fmt.Errorf("inbox: no free message filename for stamp %d", stamp)
}
