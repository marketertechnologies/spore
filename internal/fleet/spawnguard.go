package fleet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// FastDeathWindow is how long a spawned agent must be seen alive by a
// pass before its run counts as healthy. A session that disappears
// before any pass saw it alive this long is a fast death. With the
// timer firing about once a minute, a run judged fast may have lasted
// up to one pass interval longer than the window.
const FastDeathWindow = 60 * time.Second

// MaxFastDeaths is how many fast deaths in a row stop the respawns
// of a worker or the coordinator. A healthy run resets the count.
const MaxFastDeaths = 3

// spawnGuard is the per-agent crash-loop record, kept as JSON. Pending
// means the latest spawn has not been judged yet: neither seen alive
// for FastDeathWindow nor seen dead.
type spawnGuard struct {
	path string

	Spawned int64 `json:"spawned"`
	Pending bool  `json:"pending"`
	Deaths  int   `json:"deaths"`
	Capped  bool  `json:"capped"`
}

// loadGuard reads the record at path. A missing or unreadable file is
// a fresh record: losing it re-arms the cap, it never blocks a spawn.
func loadGuard(path string) *spawnGuard {
	g := &spawnGuard{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, g)
	}
	g.path = path
	return g
}

func (g *spawnGuard) save() error {
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(g.path), 0o755); err != nil {
		return err
	}
	tmp := g.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, g.path)
}

func clearGuard(path string) { _ = os.Remove(path) }

// alive records a pass that saw the agent running. Once the pending
// spawn has been up for FastDeathWindow the run is healthy and the
// death count resets. A live agent after a capped streak was started
// outside the reconciler (by hand); its run is judged from now.
func (g *spawnGuard) alive(now time.Time) {
	if !g.Pending && g.Deaths > 0 {
		g.spawned(now)
		return
	}
	if g.Pending && now.Sub(time.Unix(g.Spawned, 0)) >= FastDeathWindow {
		g.Pending = false
		g.Deaths = 0
		g.Capped = false
	}
}

// dead records a pass that found the agent gone and reports whether
// that was a fast death (a pending spawn never judged healthy).
func (g *spawnGuard) dead() bool {
	if !g.Pending {
		return false
	}
	g.Pending = false
	g.Deaths++
	return true
}

func (g *spawnGuard) spawned(now time.Time) {
	g.Spawned = now.Unix()
	g.Pending = true
}

// mayRespawn reports whether the death count is under the cap.
func (g *spawnGuard) mayRespawn() bool { return g.Deaths < MaxFastDeaths }

// capOnce reports true exactly once per capped streak, for the
// one-time coordinator notice.
func (g *spawnGuard) capOnce() bool {
	if g.mayRespawn() || g.Capped {
		return false
	}
	g.Capped = true
	return true
}
