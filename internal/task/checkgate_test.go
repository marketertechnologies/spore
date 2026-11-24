package task

import "testing"

func TestLoadEngineerCheck(t *testing.T) {
	root := t.TempDir()
	if got := LoadEngineerCheck(root); got != (EngineerCheck{TimeoutSeconds: DefaultEngineerCheckTimeoutSeconds}) {
		t.Errorf("missing spore.toml: got %+v", got)
	}
	writeSporeToml(t, root, "[fleet]\ncheck = \"wrong section\"\n[fleet.roles.engineer]\ncheck = \" just check \"\ncheck_timeout_seconds = 300\n")
	if got := LoadEngineerCheck(root); got != (EngineerCheck{Cmd: "just check", TimeoutSeconds: 300}) {
		t.Errorf("got %+v", got)
	}
	writeSporeToml(t, root, "[fleet.roles.engineer]\ncheck = \"x\"\ncheck_timeout_seconds = 0\n")
	if got := LoadEngineerCheck(root).TimeoutSeconds; got != DefaultEngineerCheckTimeoutSeconds {
		t.Errorf("zero timeout: got %d, want default", got)
	}
	if DefaultEngineerCheckTimeoutSeconds >= EngineerCheckHookTimeout {
		t.Errorf("default check timeout %d must stay under the hook timeout %d", DefaultEngineerCheckTimeoutSeconds, EngineerCheckHookTimeout)
	}
}
