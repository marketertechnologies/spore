package composer

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"
)

func bundledPool() fstest.MapFS {
	m := fstest.MapFS{}
	for _, id := range DefaultCore {
		m[id+".md"] = &fstest.MapFile{Data: []byte("# " + id + "\n")}
	}
	return m
}

func TestMissingDefaults(t *testing.T) {
	_, consumersDir := setupPool(t)
	consumer := filepath.Join(consumersDir, "c.txt")
	writeFile(t, consumer, "# target: CLAUDE.md\ncore/a\n")

	got, err := MissingDefaults(consumer)
	if err != nil {
		t.Fatalf("MissingDefaults: %v", err)
	}
	if !reflect.DeepEqual(got, DefaultCore) {
		t.Fatalf("missing = %v, want %v", got, DefaultCore)
	}

	var lines string
	for _, id := range DefaultCore {
		lines += "?align " + id + "\n"
	}
	writeFile(t, consumer, "core/a\n"+lines)
	got, err = MissingDefaults(consumer)
	if err != nil {
		t.Fatalf("MissingDefaults: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("predicate-gated defaults reported missing: %v", got)
	}
}

func TestAddDefaults_AppendsAndCopiesFragments(t *testing.T) {
	rulesDir, consumersDir := setupPool(t)
	consumer := filepath.Join(consumersDir, "c.txt")
	writeFile(t, consumer, "core/a")

	added, err := AddDefaults(rulesDir, consumer, bundledPool())
	if err != nil {
		t.Fatalf("AddDefaults: %v", err)
	}
	if !reflect.DeepEqual(added, DefaultCore) {
		t.Fatalf("added = %v, want %v", added, DefaultCore)
	}
	if _, err := Compose(rulesDir, consumer, Options{}); err != nil {
		t.Fatalf("Compose after AddDefaults: %v", err)
	}
	if missing, _ := MissingDefaults(consumer); len(missing) != 0 {
		t.Fatalf("still missing after AddDefaults: %v", missing)
	}

	again, err := AddDefaults(rulesDir, consumer, bundledPool())
	if err != nil || len(again) != 0 {
		t.Fatalf("second AddDefaults = %v, %v; want no-op", again, err)
	}
}

func TestAddDefaults_KeepsLocalFragment(t *testing.T) {
	rulesDir, consumersDir := setupPool(t)
	consumer := filepath.Join(consumersDir, "c.txt")
	writeFile(t, consumer, "core/a\n")
	local := filepath.Join(rulesDir, DefaultCore[0]+".md")
	writeFile(t, local, "local body\n")

	if _, err := AddDefaults(rulesDir, consumer, bundledPool()); err != nil {
		t.Fatalf("AddDefaults: %v", err)
	}
	b, err := os.ReadFile(local)
	if err != nil || string(b) != "local body\n" {
		t.Fatalf("local fragment = %q, %v; want untouched", b, err)
	}
}
