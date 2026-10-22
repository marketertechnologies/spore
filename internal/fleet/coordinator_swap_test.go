package fleet

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeSwapTmux struct {
	env   string
	calls [][]string
}

func (f *fakeSwapTmux) run(args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	switch args[0] {
	case "show-environment":
		return []byte(f.env), nil
	case "list-panes":
		return []byte("%7\n%9\n"), nil
	case "display-message":
		return []byte("spore/proj/coordinator\n"), nil
	}
	return nil, nil
}

// mutations drops the read-only probes so tests compare the commands
// that change tmux state.
func (f *fakeSwapTmux) mutations() [][]string {
	var out [][]string
	for _, c := range f.calls {
		if c[0] != "show-environment" && c[0] != "list-panes" && c[0] != "display-message" {
			out = append(out, c)
		}
	}
	return out
}

type swapFixture struct {
	root, inbox, session string
	tmux                 *fakeSwapTmux
	now                  time.Time
	onSleep              func()
}

func newSwapFixture(t *testing.T, toml, sessionEnvExtra string) *swapFixture {
	t.Helper()
	f := &swapFixture{
		root:    t.TempDir(),
		inbox:   filepath.Join(t.TempDir(), "inbox"),
		session: "spore/proj/coordinator",
		now:     time.Unix(1_800_000_000, 0),
	}
	if toml != "" {
		if err := os.WriteFile(filepath.Join(f.root, "spore.toml"), []byte(toml), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.tmux = &fakeSwapTmux{env: "SPORE_PROJECT_ROOT=" + f.root + "\n" +
		"SPORE_TASK_INBOX=" + f.inbox + "\n" +
		"SPORE_COORDINATOR_ROLE=/r/role.md\n" +
		"-SOMETHING_REMOVED\n" + sessionEnvExtra}
	t.Setenv("TMUX", "")
	t.Setenv(CoordinatorAgentEnv, "/usr/local/bin/spore-coordinator-launch")
	prevTmux, prevSleep, prevNow := swapTmux, swapSleep, swapNow
	swapTmux = f.tmux.run
	swapNow = func() time.Time { return f.now }
	swapSleep = func(d time.Duration) {
		f.now = f.now.Add(d)
		if f.onSleep != nil {
			f.onSleep()
		}
	}
	t.Cleanup(func() { swapTmux, swapSleep, swapNow = prevTmux, prevSleep, prevNow })
	return f
}

func (f *swapFixture) readTOML(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.root, "spore.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSwapCoordinatorToCodexClearsModel(t *testing.T) {
	f := newSwapFixture(t,
		"# project config\n[coordinator]\ndriver = \"claude\" # pilot\nmodel = \"opus\"\nbrief = \"role.md\"\n\n[fleet]\nmax_workers = 2\n",
		"SPORE_COORDINATOR_PROVIDER=claude\nSPORE_COORDINATOR_MODEL=opus\n")

	res, err := SwapCoordinator(SwapSpec{Session: f.session, To: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	wantTOML := "# project config\n[coordinator]\ndriver = \"codex\" # pilot\nbrief = \"role.md\"\n\n[fleet]\nmax_workers = 2\n"
	if got := f.readTOML(t); got != wantTOML {
		t.Errorf("spore.toml:\n%q\nwant:\n%q", got, wantTOML)
	}
	if res.Config.Driver != "codex" || res.Config.Model != "" || res.Pane != "%7" {
		t.Errorf("result = %+v", res)
	}
	want := [][]string{
		{"set-environment", "-t", f.session, "SPORE_COORDINATOR_PROVIDER", "codex"},
		{"set-environment", "-t", f.session, "-r", "SPORE_COORDINATOR_MODEL"},
		{"respawn-pane", "-k", "-t", "%7", "-c", f.root,
			coordinatorShellCommand("/usr/local/bin/spore-coordinator-launch", "/r/role.md")},
	}
	if got := f.tmux.mutations(); !reflect.DeepEqual(got, want) {
		t.Errorf("tmux sequence:\n%q\nwant:\n%q", got, want)
	}
	if f.tmux.calls[0][0] != "show-environment" || f.tmux.calls[0][2] != f.session {
		t.Errorf("first call should read the target session env: %q", f.tmux.calls[0])
	}
}

func TestSwapCoordinatorModelRules(t *testing.T) {
	cases := []struct {
		name, toml, env, to, model string
		modelSet                   bool
		wantModel                  string
	}{
		{"explicit model", "", "", "codex", "gpt-5.5", true, "gpt-5.5"},
		{"same driver keeps model", "[coordinator]\ndriver = \"codex\"\nmodel = \"gpt-5.5\"\n", "", "codex", "", false, "gpt-5.5"},
		{"session env beats toml", "[coordinator]\ndriver = \"claude\"\nmodel = \"opus\"\n", "SPORE_COORDINATOR_PROVIDER=codex\nSPORE_COORDINATOR_MODEL=gpt-5.5\n", "codex", "", false, "gpt-5.5"},
		{"default driver is claude", "", "", "claude", "", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newSwapFixture(t, c.toml, c.env)
			res, err := SwapCoordinator(SwapSpec{Session: f.session, To: c.to, Model: c.model, ModelSet: c.modelSet})
			if err != nil {
				t.Fatal(err)
			}
			if res.Config.Model != c.wantModel {
				t.Errorf("model = %q, want %q", res.Config.Model, c.wantModel)
			}
			muts := f.tmux.mutations()
			wantEnv := []string{"set-environment", "-t", f.session, "SPORE_COORDINATOR_MODEL", c.wantModel}
			if c.wantModel == "" {
				wantEnv = []string{"set-environment", "-t", f.session, "-r", "SPORE_COORDINATOR_MODEL"}
			}
			if !reflect.DeepEqual(muts[1], wantEnv) {
				t.Errorf("model env = %q, want %q", muts[1], wantEnv)
			}
		})
	}
}

func TestSwapCoordinatorWrapWaitsForStateMD(t *testing.T) {
	f := newSwapFixture(t, "[coordinator]\ndriver = \"claude\"\n", "")
	state := filepath.Join(f.root, "state.md")
	old := f.now.Add(-time.Hour)
	if err := os.WriteFile(state, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(state, old, old); err != nil {
		t.Fatal(err)
	}
	sleeps := 0
	f.onSleep = func() {
		sleeps++
		if sleeps == 3 {
			if err := os.WriteFile(state, []byte("flushed"), 0o644); err != nil {
				t.Fatal(err)
			}
			_ = os.Chtimes(state, f.now, f.now)
		}
	}

	res, err := SwapCoordinator(SwapSpec{
		Session: f.session, To: "codex", Wrap: true,
		WrapTimeout: time.Minute, WrapSettle: 10 * time.Second, Poll: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Wrapped {
		t.Error("Wrapped = false")
	}
	if sleeps < 3+5 {
		t.Errorf("swapped after %d polls; want the 10s settle after the write", sleeps)
	}
	entries, err := os.ReadDir(f.inbox)
	if err != nil || len(entries) != 1 {
		t.Fatalf("inbox entries = %v, err %v", entries, err)
	}
	body, _ := os.ReadFile(filepath.Join(f.inbox, entries[0].Name()))
	if !strings.Contains(string(body), "codex coordinator") || !strings.Contains(string(body), "state.md") {
		t.Errorf("wrap envelope = %s", body)
	}
	if len(f.tmux.mutations()) != 3 {
		t.Errorf("tmux mutations = %q", f.tmux.mutations())
	}
}

func TestSwapCoordinatorWrapTimeout(t *testing.T) {
	const toml = "[coordinator]\ndriver = \"claude\"\n"
	spec := func(f *swapFixture, force bool) SwapSpec {
		return SwapSpec{Session: f.session, To: "codex", Wrap: true, Force: force,
			WrapTimeout: 30 * time.Second, WrapSettle: 10 * time.Second, Poll: 2 * time.Second}
	}

	f := newSwapFixture(t, toml, "")
	if _, err := SwapCoordinator(spec(f, false)); !errors.Is(err, ErrWrapTimeout) {
		t.Fatalf("err = %v, want ErrWrapTimeout", err)
	}
	if got := f.readTOML(t); got != toml {
		t.Errorf("spore.toml changed on abort: %q", got)
	}
	if m := f.tmux.mutations(); len(m) != 0 {
		t.Errorf("tmux mutated on abort: %q", m)
	}

	f = newSwapFixture(t, toml, "")
	res, err := SwapCoordinator(spec(f, true))
	if err != nil {
		t.Fatal(err)
	}
	if res.Wrapped || res.Config.Driver != "codex" || len(f.tmux.mutations()) != 3 {
		t.Errorf("forced swap: res %+v, mutations %q", res, f.tmux.mutations())
	}
}

func TestSwapCoordinatorRejects(t *testing.T) {
	f := newSwapFixture(t, "", "")
	if _, err := SwapCoordinator(SwapSpec{Session: f.session, To: "gemini"}); err == nil {
		t.Error("unknown driver accepted")
	}
	f.tmux.env = "SPORE_TASK_SLUG=coordinator\n"
	if _, err := SwapCoordinator(SwapSpec{Session: f.session, To: "codex"}); err == nil {
		t.Error("session without SPORE_PROJECT_ROOT accepted")
	}
	if len(f.tmux.mutations()) != 0 {
		t.Errorf("tmux mutated: %q", f.tmux.mutations())
	}
}

func TestSwapCoordinatorInsidePaneNeedsNoWrap(t *testing.T) {
	f := newSwapFixture(t, "", "")
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	t.Setenv("TMUX_PANE", "%7")
	_, err := SwapCoordinator(SwapSpec{Session: f.session, To: "codex", Wrap: true, WrapTimeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "--no-wrap") {
		t.Fatalf("err = %v, want a --no-wrap hint", err)
	}
	if _, err := os.Stat(f.inbox); !os.IsNotExist(err) {
		t.Error("wrap request written from inside the pane")
	}
	if _, err := SwapCoordinator(SwapSpec{Session: f.session, To: "codex"}); err != nil {
		t.Fatalf("--no-wrap from inside the pane: %v", err)
	}
}
