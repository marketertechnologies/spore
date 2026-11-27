//go:build linux

package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCheckBundleConfigACL(t *testing.T) {
	env, _ := newEnv(t)
	if err := os.MkdirAll(filepath.Dir(env.BundleConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.BundleConfig, []byte("BUNDLE_JOBS: \"4\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("setfacl", "-b", env.BundleConfig).CombinedOutput(); err != nil {
		t.Skipf("setfacl unavailable: %v %s", err, out)
	}
	if r := find(t, Check(env), "bundle tokens"); r.Status != Pass {
		t.Fatalf("no ACL = %+v, want pass", r)
	}
	uid := "u:" + strconv.Itoa(os.Getuid()) + ":r"
	if out, err := exec.Command("setfacl", "-m", uid, env.BundleConfig).CombinedOutput(); err != nil {
		t.Skipf("setfacl unavailable on this filesystem: %v %s", err, out)
	}
	r := find(t, Check(env), "bundle tokens")
	if r.Status != Warn || !strings.Contains(r.Detail, "carries an ACL") {
		t.Errorf("ACL = %+v, want warn", r)
	}
}
