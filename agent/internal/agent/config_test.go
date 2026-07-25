package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoadConfigPreservesCredentialWithOwnerOnlyPermissions(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "ace", "agent.json")
	want := Config{ServerURL: "https://it.example.com", NodeID: "node-1", Credential: "device-secret"}
	if err := SaveConfig(path, want); err != nil {
		t.Fatalf("SaveConfig returned error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config permissions = %o, want 600", got)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if got != want {
		t.Fatalf("LoadConfig = %#v, want %#v", got, want)
	}
}
