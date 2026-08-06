package systemupdate

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFileStoreLoadMissingFileReturnsEmptyState(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "missing.json"))

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got != (PersistentState{}) {
		t.Fatalf("Load = %#v, want empty state", got)
	}
}

func TestFileStoreSaveAndLoadRoundTrip(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "nested", "state.json"))
	want := PersistentState{Task: &Task{ID: "task-1", Stage: StagePulling}}

	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%#v err=%v", got, err)
	}
}

func TestFileStoreLoadRejectsCorruptJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := NewFileStore(path).Load()
	if err == nil {
		t.Fatal("Load accepted corrupt JSON")
	}
}

func TestFileStoreLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := NewFileStore(path).Load()
	if err == nil {
		t.Fatal("Load accepted unknown fields")
	}
}

func TestFileStoreSaveUsesPrivateParentAndStateModes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	if err := NewFileStore(path).Save(PersistentState{}); err != nil {
		t.Fatal(err)
	}

	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("parent mode = %o, want 0700", got)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("state mode = %o, want 0600", got)
	}
}

func TestFileStoreSaveCleansTemporaryFileWhenRenameFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewFileStore(path)
	old := PersistentState{Task: &Task{ID: "old", Stage: StagePulling}}
	if err := store.Save(old); err != nil {
		t.Fatal(err)
	}

	originalOperations := fileStoreOps
	fileStoreOps.rename = func(_, _ string) error { return errors.New("injected rename failure") }
	t.Cleanup(func() { fileStoreOps = originalOperations })

	err := store.Save(PersistentState{Task: &Task{ID: "new", Stage: StageSucceeded}})
	if err == nil || !strings.Contains(err.Error(), "replace state") {
		t.Fatalf("Save error = %v, want injected rename failure", err)
	}
	got, err := store.Load()
	if err != nil || !reflect.DeepEqual(got, old) {
		t.Fatalf("Load after failed Save = %#v, %v; want %#v", got, err, old)
	}
	temporary, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".systemupdate-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("temporary files remain: %v", temporary)
	}
}

func TestFileStoreLoadRejectsFilesAboveOneMiB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, make([]byte, maxStateBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := NewFileStore(path).Load()
	if err == nil {
		t.Fatal("Load accepted a state file larger than 1 MiB")
	}
}
