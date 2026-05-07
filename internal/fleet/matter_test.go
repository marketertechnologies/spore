package fleet

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/versality/spore/internal/matter"
)

type recordingMatter struct {
	name     string
	created  int
	updated  int
	syncErr  error
	claimErr error
	calls    atomic.Int32
	claims   struct {
		mu  sync.Mutex
		log []string
	}
}

func (r *recordingMatter) Name() string { return r.name }
func (r *recordingMatter) Sync(ctx context.Context, projectRoot string) (int, int, error) {
	r.calls.Add(1)
	return r.created, r.updated, r.syncErr
}
func (r *recordingMatter) OnClaim(ctx context.Context, slug string, meta map[string]string) error {
	r.claims.mu.Lock()
	r.claims.log = append(r.claims.log, slug)
	r.claims.mu.Unlock()
	return r.claimErr
}
func (r *recordingMatter) claimLog() []string {
	r.claims.mu.Lock()
	defer r.claims.mu.Unlock()
	out := make([]string, len(r.claims.log))
	copy(out, r.claims.log)
	return out
}
func (r *recordingMatter) OnDone(ctx context.Context, slug string, meta map[string]string) error {
	return nil
}

// installMatter swaps the global registry for the duration of the
// test and registers `factory` under `name`. The registry is
// restored on cleanup so other tests aren't polluted.
func installMatter(t *testing.T, name string, factory matter.Factory) {
	t.Helper()
	matter.ResetForTest()
	matter.Register(name, factory)
	t.Cleanup(matter.ResetForTest)
}

func writeSporeTOML(t *testing.T, projectRoot, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(projectRoot, "spore.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// syncMatters is the prelude pass Reconcile runs against the matter
// plugin layer. Testing it in isolation lets us verify the contract
// (created/updated counters, captured errors, no-op when nothing is
// configured) without depending on tmux/git for the full pass.

func TestSyncMattersHappyPath(t *testing.T) {
	dirs := newTestDirs(t)
	rec := &recordingMatter{name: "fake", created: 2, updated: 1}
	installMatter(t, "fake", func(c matter.Config) (matter.Matter, error) { return rec, nil })
	writeSporeTOML(t, dirs.project, "[matter.fake]\nenabled = true\n")

	res := syncMatters(dirs.project)
	if rec.calls.Load() != 1 {
		t.Errorf("Sync calls = %d, want 1", rec.calls.Load())
	}
	if len(res) != 1 {
		t.Fatalf("results = %v", res)
	}
	got := res[0]
	if got.Name != "fake" || got.Created != 2 || got.Updated != 1 || got.Err != nil {
		t.Errorf("result = %+v", got)
	}
}

func TestSyncMattersErrorIsCaptured(t *testing.T) {
	dirs := newTestDirs(t)
	boom := errors.New("upstream down")
	rec := &recordingMatter{name: "fake", syncErr: boom}
	installMatter(t, "fake", func(c matter.Config) (matter.Matter, error) { return rec, nil })
	writeSporeTOML(t, dirs.project, "[matter.fake]\nenabled = true\n")

	res := syncMatters(dirs.project)
	if len(res) != 1 || !errors.Is(res[0].Err, boom) {
		t.Errorf("result = %+v", res)
	}
}

func TestSyncMattersNoConfig(t *testing.T) {
	dirs := newTestDirs(t)
	if got := syncMatters(dirs.project); got != nil {
		t.Errorf("want nil, got %v", got)
	}
}

func TestSyncMattersDisabledSkipped(t *testing.T) {
	dirs := newTestDirs(t)
	rec := &recordingMatter{name: "fake"}
	installMatter(t, "fake", func(c matter.Config) (matter.Matter, error) { return rec, nil })
	writeSporeTOML(t, dirs.project, "[matter.fake]\nenabled = false\n")

	if got := syncMatters(dirs.project); got != nil {
		t.Errorf("disabled matter should produce no result, got %v", got)
	}
	if rec.calls.Load() != 0 {
		t.Errorf("disabled matter Sync should not be called, got %d", rec.calls.Load())
	}
}

func TestSyncMattersUnknownAdapterCapturedAsError(t *testing.T) {
	dirs := newTestDirs(t)
	matter.ResetForTest()
	t.Cleanup(matter.ResetForTest)
	writeSporeTOML(t, dirs.project, "[matter.ghost]\nenabled = true\n")

	res := syncMatters(dirs.project)
	if len(res) != 1 || res[0].Err == nil {
		t.Fatalf("want error result for unknown adapter, got %v", res)
	}
}

// notifyMatterClaim fires OnClaim on the matter named in the
// just-spawned task's frontmatter. The tests below pin the contract
// (matter resolution, disabled skip, error swallow) so the rover
// claim signal stays bound to spawn even after a refactor.

func writeMatterTask(t *testing.T, tasksDir, slug, matterName, matterID string) {
	t.Helper()
	body := "---\nstatus: active\nslug: " + slug + "\ntitle: " + slug
	if matterName != "" {
		body += "\nmatter: " + matterName
	}
	if matterID != "" {
		body += "\nmatter_id: " + matterID
	}
	body += "\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(tasksDir, slug+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNotifyMatterClaimFiresOnClaim(t *testing.T) {
	dirs := newTestDirs(t)
	rec := &recordingMatter{name: "fake"}
	installMatter(t, "fake", func(c matter.Config) (matter.Matter, error) { return rec, nil })
	writeSporeTOML(t, dirs.project, "[matter.fake]\nenabled = true\n")
	writeMatterTask(t, dirs.tasks, "alpha", "fake", "FAKE-12")

	var warn bytes.Buffer
	notifyMatterClaim(dirs.project, dirs.tasks, "alpha", &warn)

	if got := rec.claimLog(); len(got) != 1 || got[0] != "alpha" {
		t.Errorf("OnClaim log = %v, want [alpha]", got)
	}
	if warn.Len() != 0 {
		t.Errorf("happy path should not warn, got %q", warn.String())
	}
}

func TestNotifyMatterClaimSkipsWhenNoMatterMeta(t *testing.T) {
	dirs := newTestDirs(t)
	rec := &recordingMatter{name: "fake"}
	installMatter(t, "fake", func(c matter.Config) (matter.Matter, error) { return rec, nil })
	writeSporeTOML(t, dirs.project, "[matter.fake]\nenabled = true\n")
	writeMatterTask(t, dirs.tasks, "alpha", "", "")

	var warn bytes.Buffer
	notifyMatterClaim(dirs.project, dirs.tasks, "alpha", &warn)

	if got := rec.claimLog(); len(got) != 0 {
		t.Errorf("OnClaim should not fire without matter meta, got %v", got)
	}
}

func TestNotifyMatterClaimSkipsWhenDisabled(t *testing.T) {
	dirs := newTestDirs(t)
	rec := &recordingMatter{name: "fake"}
	installMatter(t, "fake", func(c matter.Config) (matter.Matter, error) { return rec, nil })
	writeSporeTOML(t, dirs.project, "[matter.fake]\nenabled = false\n")
	writeMatterTask(t, dirs.tasks, "alpha", "fake", "FAKE-12")

	var warn bytes.Buffer
	notifyMatterClaim(dirs.project, dirs.tasks, "alpha", &warn)

	if got := rec.claimLog(); len(got) != 0 {
		t.Errorf("OnClaim should not fire when matter disabled, got %v", got)
	}
}

func TestNotifyMatterClaimSwallowsError(t *testing.T) {
	dirs := newTestDirs(t)
	boom := errors.New("upstream rejected")
	rec := &recordingMatter{name: "fake", claimErr: boom}
	installMatter(t, "fake", func(c matter.Config) (matter.Matter, error) { return rec, nil })
	writeSporeTOML(t, dirs.project, "[matter.fake]\nenabled = true\n")
	writeMatterTask(t, dirs.tasks, "alpha", "fake", "FAKE-12")

	var warn bytes.Buffer
	notifyMatterClaim(dirs.project, dirs.tasks, "alpha", &warn)

	if got := rec.claimLog(); len(got) != 1 {
		t.Errorf("OnClaim should have been called once, got %v", got)
	}
	if warn.Len() == 0 {
		t.Errorf("OnClaim error should land on warnOut, got empty buffer")
	}
}
