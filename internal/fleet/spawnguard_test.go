package fleet

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSpawnGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "g.json")
	now := time.Unix(1_800_000_000, 0)
	g := loadGuard(path)
	if g.dead() {
		t.Fatal("a never-spawned agent is not a fast death")
	}
	for range MaxFastDeaths {
		if !g.mayRespawn() {
			t.Fatalf("capped early at %d deaths", g.Deaths)
		}
		g.spawned(now)
		if !g.dead() {
			t.Fatal("death of a pending spawn must count")
		}
	}
	if g.mayRespawn() || !g.capOnce() || g.capOnce() {
		t.Fatalf("want capped with exactly one notice, got %+v", g)
	}
	if err := g.save(); err != nil {
		t.Fatal(err)
	}

	// Started by hand after the cap: judged from first sight, healthy
	// after the window.
	g = loadGuard(path)
	g.alive(now)
	if !g.Pending {
		t.Fatalf("hand-started run not tracked: %+v", g)
	}
	g.alive(now.Add(FastDeathWindow))
	if g.Pending || g.Deaths != 0 || g.Capped || !g.mayRespawn() {
		t.Fatalf("healthy run did not reset: %+v", g)
	}
	if g.dead() {
		t.Fatal("death after a healthy run is not a fast death")
	}
}
