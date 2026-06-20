package fleet

import (
	"strings"
	"testing"
)

func TestCoordinatorShellCommandSingleExec(t *testing.T) {
	cmd := coordinatorShellCommand("claude", "/r/role.md", false)
	if !strings.Contains(cmd, "exec claude") {
		t.Errorf("single-exec command missing exec: %q", cmd)
	}
	if strings.Contains(cmd, "while true") {
		t.Errorf("single-exec command must not loop: %q", cmd)
	}
}

func TestCoordinatorShellCommandSupervised(t *testing.T) {
	cmd := coordinatorShellCommand("claude", "/r/role.md", true)
	for _, want := range []string{
		"while true",
		`SPORE_ACCOUNT_TIER="${SPORE_ACCOUNT_TIER:-${WT_ACCOUNT_TIER:-}}"`,
		"sleep 1",
		"cat '/r/role.md'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("supervised command missing %q\n  got: %s", want, cmd)
		}
	}
	// The loop must not exec away: exec would replace the supervisor
	// bash and break the respawn on driver exit.
	if strings.Contains(cmd, "exec ") {
		t.Errorf("supervised command must not exec the driver: %q", cmd)
	}
}

func TestCoordinatorSuperviseResolution(t *testing.T) {
	cases := []struct {
		name string
		env  string
		cfg  bool
		want bool
	}{
		{"default off", "", false, false},
		{"cfg on", "", true, true},
		{"env on overrides cfg off", "1", false, true},
		{"env true overrides cfg off", "true", false, true},
		{"env off overrides cfg on", "0", true, false},
		{"env false overrides cfg on", "false", true, false},
		{"env garbage falls back to cfg", "maybe", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SPORE_COORDINATOR_SUPERVISE", tc.env)
			if got := coordinatorSupervise(CoordinatorConfig{Supervise: tc.cfg}); got != tc.want {
				t.Errorf("coordinatorSupervise(env=%q,cfg=%v) = %v, want %v", tc.env, tc.cfg, got, tc.want)
			}
		})
	}
}
