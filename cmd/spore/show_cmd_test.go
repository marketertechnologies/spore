package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/versality/spore/internal/editor"
)

type fakeBackend struct {
	got editor.Location
	err error
}

func (f *fakeBackend) Name() string { return "fake" }
func (f *fakeBackend) Open(l editor.Location) error {
	f.got = l
	return f.err
}

func TestShowResolvesRelativeToCwd(t *testing.T) {
	cwd := t.TempDir()
	os.WriteFile(filepath.Join(cwd, "a.go"), nil, 0o644)
	b := &fakeBackend{}
	var out, errb bytes.Buffer
	if code := show([]string{"a.go:3"}, cwd, b, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if b.got != (editor.Location{Path: filepath.Join(cwd, "a.go"), Line: 3}) {
		t.Fatalf("got %+v", b.got)
	}
}

func TestShowNoEditorExitsZero(t *testing.T) {
	cwd := t.TempDir()
	os.WriteFile(filepath.Join(cwd, "a.go"), nil, 0o644)
	v := &editor.VSCode{Home: t.TempDir(), RuntimeDir: t.TempDir()}
	var out, errb bytes.Buffer
	if code := show([]string{"a.go:1"}, cwd, v, &out, &errb); code != 0 {
		t.Fatalf("exit %d", code)
	}
	msg := errb.String()
	if strings.Count(msg, "\n") != 1 || !strings.Contains(msg, "no editor connected") {
		t.Fatalf("stderr %q", msg)
	}
}

func TestShowMissingFileExitsZero(t *testing.T) {
	b := &fakeBackend{}
	var out, errb bytes.Buffer
	if code := show([]string{"nope.go"}, t.TempDir(), b, &out, &errb); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if b.got.Path != "" || !strings.Contains(errb.String(), "no such file") {
		t.Fatalf("opened %+v, stderr %q", b.got, errb.String())
	}
}

func TestShowUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := show(nil, "/", &fakeBackend{}, &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
}
