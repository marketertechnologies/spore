package task

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEnsureRoleTaskDirCreatesLayout(t *testing.T) {
	root := t.TempDir()
	out, err := EnsureRoleTaskDir(root, "demo")
	if err != nil {
		t.Fatalf("EnsureRoleTaskDir: %v", err)
	}
	want := filepath.Join(root, ".spore", "demo")
	if out != want {
		t.Errorf("path = %q, want %q", out, want)
	}
	for _, sub := range []string{
		"responses",
		filepath.Join("reviews", "A"),
		filepath.Join("reviews", "B"),
	} {
		if fi, err := os.Stat(filepath.Join(out, sub)); err != nil || !fi.IsDir() {
			t.Errorf("missing dir %s: err=%v", sub, err)
		}
	}
}

func TestEnsureRoleTaskDirEmptySlug(t *testing.T) {
	if _, err := EnsureRoleTaskDir(t.TempDir(), ""); err == nil {
		t.Fatal("expected error on empty slug, got nil")
	}
}

func TestSpecRoundtrip(t *testing.T) {
	root := t.TempDir()
	body := []byte("# spec\n\ndo the thing.\n")
	if err := WriteSpec(root, "demo", body); err != nil {
		t.Fatalf("WriteSpec: %v", err)
	}
	got, err := ReadSpec(root, "demo")
	if err != nil {
		t.Fatalf("ReadSpec: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("spec = %q, want %q", got, body)
	}
}

func TestEngineerResponseRoundtrip(t *testing.T) {
	root := t.TempDir()
	in := EngineerResponse{
		Addressed: []string{"comment 1 -> commit abc", "comment 2 -> renamed Y"},
		Pushback:  []string{"comment 3 -> disagree because Z"},
		Notes:     "spec is ambiguous on naming convention",
	}
	if err := WriteEngineerResponse(root, "demo", 2, in); err != nil {
		t.Fatalf("WriteEngineerResponse: %v", err)
	}
	got, err := ReadEngineerResponse(root, "demo", 2)
	if err != nil {
		t.Fatalf("ReadEngineerResponse: %v", err)
	}
	if !reflect.DeepEqual(in, got) {
		t.Errorf("roundtrip mismatch\n want: %+v\n got:  %+v", in, got)
	}
}

func TestEngineerResponseRoundFloor(t *testing.T) {
	if err := WriteEngineerResponse(t.TempDir(), "demo", 0, EngineerResponse{}); err == nil {
		t.Fatal("expected error on round=0, got nil")
	}
}

func TestReviewRoundtrip(t *testing.T) {
	root := t.TempDir()
	in := Review{
		Verdict:  VerdictRequestChanges,
		Summary:  "naming drift in state machine",
		Comments: []string{"Phase enum should be string", "drop the helper struct"},
	}
	if err := WriteReview(root, "demo", ReviewerA, 1, in); err != nil {
		t.Fatalf("WriteReview: %v", err)
	}
	got, err := ReadReview(root, "demo", ReviewerA, 1)
	if err != nil {
		t.Fatalf("ReadReview: %v", err)
	}
	if !reflect.DeepEqual(in, got) {
		t.Errorf("roundtrip mismatch\n want: %+v\n got:  %+v", in, got)
	}
}

func TestReviewRejectsUnknownVerdict(t *testing.T) {
	err := WriteReview(t.TempDir(), "demo", ReviewerA, 1, Review{Verdict: "lgtm"})
	if err == nil {
		t.Fatal("expected error on unknown verdict, got nil")
	}
}

func TestReviewRejectsUnknownInstance(t *testing.T) {
	err := WriteReview(t.TempDir(), "demo", ReviewerInstance("C"), 1, Review{Verdict: VerdictApprove})
	if err == nil {
		t.Fatal("expected error on unknown instance, got nil")
	}
}

func TestReadMissingResponseIsNotExist(t *testing.T) {
	_, err := ReadEngineerResponse(t.TempDir(), "demo", 1)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestListEngineerRoundsSorted(t *testing.T) {
	root := t.TempDir()
	for _, n := range []int{3, 1, 2} {
		if err := WriteEngineerResponse(root, "demo", n, EngineerResponse{}); err != nil {
			t.Fatalf("write %d: %v", n, err)
		}
	}
	got, err := ListEngineerRounds(root, "demo")
	if err != nil {
		t.Fatalf("ListEngineerRounds: %v", err)
	}
	want := []int{1, 2, 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rounds = %v, want %v", got, want)
	}
}

func TestListReviewRoundsEmpty(t *testing.T) {
	root := t.TempDir()
	got, err := ListReviewRounds(root, "demo", ReviewerA)
	if err != nil {
		t.Fatalf("ListReviewRounds: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("rounds = %v, want []", got)
	}
}

func TestListReviewRoundsMixed(t *testing.T) {
	root := t.TempDir()
	if err := WriteReview(root, "demo", ReviewerA, 1, Review{Verdict: VerdictRequestChanges}); err != nil {
		t.Fatalf("write A1: %v", err)
	}
	if err := WriteReview(root, "demo", ReviewerA, 2, Review{Verdict: VerdictApprove}); err != nil {
		t.Fatalf("write A2: %v", err)
	}
	if err := WriteReview(root, "demo", ReviewerB, 1, Review{Verdict: VerdictApprove}); err != nil {
		t.Fatalf("write B1: %v", err)
	}
	if got, _ := ListReviewRounds(root, "demo", ReviewerA); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Errorf("A rounds = %v, want [1 2]", got)
	}
	if got, _ := ListReviewRounds(root, "demo", ReviewerB); !reflect.DeepEqual(got, []int{1}) {
		t.Errorf("B rounds = %v, want [1]", got)
	}
}
