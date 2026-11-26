package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorExitsNonZeroOnFail(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	prev := doctorRun
	t.Cleanup(func() { doctorRun = prev })
	doctorRun = func(name string, args ...string) ([]byte, error) { return nil, errors.New("absent") }

	sw := filepath.Join(dir, "sw")
	if err := os.MkdirAll(filepath.Join(sw, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	args := []string{"--json", "--sw", sw, "--dropin", filepath.Join(dir, "none.json"), "--flake-lock", "", "--repo", dir}
	var b bytes.Buffer
	code, err := runDoctor(args, &b)
	if err != nil || code != 1 {
		t.Fatalf("code = %d, err = %v; want 1 (binary missing)", code, err)
	}
	var out struct {
		Results []struct{ Name, Status, Detail string }
		Fail    int
	}
	if err := json.Unmarshal(b.Bytes(), &out); err != nil {
		t.Fatalf("json: %v\n%s", err, b.String())
	}
	if out.Fail == 0 || out.Results[0].Name != "binary" || out.Results[0].Status != "fail" {
		t.Errorf("json = %+v", out)
	}

	b.Reset()
	if code, _ := runDoctor([]string{"--sw", sw, "--dropin", filepath.Join(dir, "none.json"), "--flake-lock", ""}, &b); code != 1 || !strings.Contains(b.String(), "FAIL  binary:") {
		t.Errorf("text output (code %d):\n%s", code, b.String())
	}
}
