//go:build linux

package task

import (
	"bytes"
	"encoding/binary"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A watcher claims any *.json it sees at the inbox top level, so the
// final name must never be opened for writing: it has to appear with
// its whole body already in place. inotify reports a write or a
// write-close on a top-level name only if the writer filled it there.
func TestTellNeverWritesInPlace(t *testing.T) {
	dir := t.TempDir()
	if err := TellDir(dir, "x", "warm"); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		t.Skipf("inotify: %v", err)
	}
	defer syscall.Close(fd)
	if _, err := syscall.InotifyAddWatch(fd, dir, syscall.IN_CREATE|syscall.IN_MODIFY|syscall.IN_CLOSE_WRITE); err != nil {
		t.Fatal(err)
	}

	if err := TellDir(dir, "x", "hello"); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 64*1024)
	n, err := syscall.Read(fd, buf)
	if err != nil {
		t.Fatalf("read inotify: %v", err)
	}
	created := false
	for off := 0; off+syscall.SizeofInotifyEvent <= n; {
		var ev syscall.InotifyEvent
		if err := binary.Read(bytes.NewReader(buf[off:off+syscall.SizeofInotifyEvent]), binary.NativeEndian, &ev); err != nil {
			t.Fatal(err)
		}
		nameStart := off + syscall.SizeofInotifyEvent
		name := strings.TrimRight(string(buf[nameStart:nameStart+int(ev.Len)]), "\x00")
		off = nameStart + int(ev.Len)
		if filepath.Ext(name) != ".json" {
			continue
		}
		switch {
		case ev.Mask&syscall.IN_CREATE != 0:
			created = true
		case ev.Mask&(syscall.IN_MODIFY|syscall.IN_CLOSE_WRITE) != 0:
			t.Errorf("message %s written in place (mask %#x); a watcher can claim it half-written", name, ev.Mask)
		}
	}
	if !created {
		t.Error("no message appeared in the inbox")
	}
}
