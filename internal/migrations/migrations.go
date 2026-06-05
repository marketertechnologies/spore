// Package migrations runs idempotent host-state migrations bundled with
// the spore CLI. Each migration is a shell script under
// bootstrap/migrations/, embedded in the binary, executed in lexical
// order. A ledger at $XDG_STATE_HOME/spore/migrations.applied records
// which have run so reruns are no-ops.
//
// Migration authoring contract:
//   - File name: NNN-slug.sh where NNN is a zero-padded 3-digit number.
//   - Must be idempotent: re-running on already-migrated state must
//     succeed without changing anything.
//   - Exit 0 on success, non-zero on failure. Failures halt the run
//     and the ledger is not updated for the failing migration.
//   - Stdin closed. Env inherited from the caller. Working dir is the
//     invoker's $HOME.
//   - Set -e is enabled implicitly via `bash -eu`.
package migrations

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Options controls a single Apply run.
type Options struct {
	// DryRun lists pending migrations without executing them.
	DryRun bool
	// LedgerPath overrides the default
	// $XDG_STATE_HOME/spore/migrations.applied location.
	LedgerPath string
	// Version is recorded in the ledger alongside each applied entry.
	// Pass spore.BuildVersion() from the caller.
	Version string
	// Stdout receives migration stdout. Defaults to os.Stdout.
	Stdout io.Writer
	// Stderr receives migration stderr and engine progress. Defaults
	// to os.Stderr.
	Stderr io.Writer
	// WorkDir overrides the working directory migrations run in.
	// Default: $HOME.
	WorkDir string
}

// Result summarises one Apply run.
type Result struct {
	// Applied lists migrations that ran successfully this call.
	Applied []string
	// Skipped lists migrations whose ledger entry was already present.
	Skipped []string
	// Pending lists migrations that would have run but were not
	// executed (e.g. DryRun, or queued behind a failure).
	Pending []string
	// Failed is the name of the migration that returned non-zero, or
	// empty when all migrations succeeded.
	Failed string
}

// Apply walks fsys under prefix, runs every .sh migration whose name
// is not already in the ledger, and appends successful runs to the
// ledger. Returns a Result describing what happened plus any error
// from migration execution.
func Apply(fsys fs.FS, prefix string, opts Options) (Result, error) {
	var res Result
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	migs, err := discover(fsys, prefix)
	if err != nil {
		return res, fmt.Errorf("discover: %w", err)
	}

	ledgerPath, err := resolveLedgerPath(opts.LedgerPath)
	if err != nil {
		return res, err
	}
	applied, err := readLedger(ledgerPath)
	if err != nil {
		return res, fmt.Errorf("read ledger: %w", err)
	}

	workDir := opts.WorkDir
	if workDir == "" {
		workDir, _ = os.UserHomeDir()
	}

	for _, m := range migs {
		if applied[m.name] {
			res.Skipped = append(res.Skipped, m.name)
			continue
		}
		if opts.DryRun {
			res.Pending = append(res.Pending, m.name)
			continue
		}
		fmt.Fprintf(opts.Stderr, "spore migrate: applying %s\n", m.name)
		if err := run(m, workDir, opts.Stdout, opts.Stderr); err != nil {
			res.Failed = m.name
			return res, fmt.Errorf("migration %s: %w", m.name, err)
		}
		if err := appendLedger(ledgerPath, m.name, opts.Version); err != nil {
			res.Failed = m.name
			return res, fmt.Errorf("append ledger after %s: %w", m.name, err)
		}
		res.Applied = append(res.Applied, m.name)
	}
	return res, nil
}

type migration struct {
	name string
	body []byte
}

func discover(fsys fs.FS, prefix string) ([]migration, error) {
	var out []migration
	err := fs.WalkDir(fsys, prefix, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := path.Base(p)
		if !strings.HasSuffix(name, ".sh") {
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		out = append(out, migration{name: name, body: body})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

func resolveLedgerPath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "spore", "migrations.applied"), nil
}

func readLedger(p string) (map[string]bool, error) {
	out := map[string]bool{}
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name := line
		if i := strings.IndexAny(line, "\t "); i >= 0 {
			name = line[:i]
		}
		out[name] = true
	}
	return out, s.Err()
}

func appendLedger(p, name, version string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	ts := time.Now().UTC().Format(time.RFC3339)
	_, err = fmt.Fprintf(f, "%s\t%s\t%s\n", name, ts, version)
	return err
}

func run(m migration, workDir string, stdout, stderr io.Writer) error {
	tmp, err := os.CreateTemp("", "spore-migrate-*.sh")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(m.body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	cmd := exec.Command("bash", "-eu", tmp.Name())
	cmd.Env = os.Environ()
	cmd.Dir = workDir
	cmd.Stdin = nil
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
