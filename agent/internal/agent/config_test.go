package agent

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveAndLoadConfigPreservesCredentialWithOwnerOnlyPermissions(t *testing.T) {
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

func TestConfigSanitizedExcludesCredential(t *testing.T) {
	config := Config{ServerURL: "https://it.example.com", NodeID: "node-1", Credential: "device-secret"}

	if got, want := config.Sanitized(), (SanitizedConfig{ServerURL: config.ServerURL, NodeID: config.NodeID}); got != want {
		t.Fatalf("Sanitized() = %#v, want %#v", got, want)
	}
}

func TestLoadConfigAcceptsPendingPairingWithoutNodeCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	want := Config{PendingPairing: &PendingPairing{
		ServerURL: "https://it.example.com", PairingID: "pairing-1", Credential: "pairing-secret", ExpiresAt: time.Now().Add(time.Minute),
	}}
	if err := SaveConfig(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(path)
	if err != nil || !got.IsPendingPairing() || got.IsEnrolled() {
		t.Fatalf("config=%#v err=%v", got, err)
	}
}

func TestLoadConfigPreservesExpiredPendingPairing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	want := Config{PendingPairing: &PendingPairing{
		ServerURL: "https://it.example.com", PairingID: "pairing-1", Credential: "pairing-secret", ExpiresAt: time.Now().Add(-time.Minute),
	}}
	if err := SaveConfig(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if !got.IsPendingPairing() || got.PendingPairing.PairingID != want.PendingPairing.PairingID || got.PendingPairing.Credential != want.PendingPairing.Credential {
		t.Fatalf("LoadConfig = %#v, want expired pending pairing preserved", got)
	}
}

func TestConfigSanitizedDoesNotSerializePendingPairingCredential(t *testing.T) {
	config := Config{PendingPairing: &PendingPairing{
		ServerURL: "https://it.example.com", PairingID: "pairing-1", Credential: "pairing-secret", ExpiresAt: time.Now().Add(time.Minute),
	}}

	encoded, err := json.Marshal(config.Sanitized())
	if err != nil {
		t.Fatalf("marshal sanitized config: %v", err)
	}
	if strings.Contains(string(encoded), "pairing-secret") || strings.Contains(string(encoded), "pairing_credential") {
		t.Fatalf("Sanitized JSON leaked pending credential: %s", encoded)
	}
	if !strings.Contains(string(encoded), "pairing-1") || !strings.Contains(string(encoded), "pairing_expires_at") {
		t.Fatalf("Sanitized JSON omitted pending pairing metadata: %s", encoded)
	}
}

func TestLoadConfigRejectsIncompleteOrConflictingConfiguration(t *testing.T) {
	tests := []Config{
		{ServerURL: "https://it.example.com", NodeID: "node-1"},
		{ServerURL: "file:///tmp/server", NodeID: "node-1", Credential: "device-secret"},
		{ServerURL: "https://it.example.com", NodeID: "node-1", Credential: "device-secret", PendingPairing: &PendingPairing{
			ServerURL: "https://it.example.com", PairingID: "pairing-1", Credential: "pairing-secret", ExpiresAt: time.Now().Add(time.Minute),
		}},
		{PendingPairing: &PendingPairing{ServerURL: "https://it.example.com", PairingID: "pairing-1", ExpiresAt: time.Now().Add(time.Minute)}},
	}
	for _, config := range tests {
		path := filepath.Join(t.TempDir(), "agent.json")
		if err := SaveConfig(path, config); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil || err.Error() != "agent config is incomplete" {
			t.Fatalf("LoadConfig(%#v) error = %v, want incomplete configuration", config, err)
		}
	}
}

func TestSaveConfigClosesDestinationBeforeInjectedRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, []byte("old config"), 0o600); err != nil {
		t.Fatalf("write old config: %v", err)
	}

	originalOperations := configOperations
	originalSecurity := secureConfigFile
	t.Cleanup(func() {
		configOperations = originalOperations
		secureConfigFile = originalSecurity
	})

	closed := false
	secured := false
	configOperations.openExisting = func(name string) (io.Closer, error) {
		if name != path {
			t.Fatalf("open existing path = %q, want %q", name, path)
		}
		return closeRecorder{onClose: func() { closed = true }}, nil
	}
	configOperations.rename = func(_, destination string) error {
		if !closed {
			t.Fatal("rename called before destination handle was closed")
		}
		if !secured {
			t.Fatal("rename called before security hook")
		}
		if destination != path {
			t.Fatalf("rename destination = %q, want %q", destination, path)
		}
		return nil
	}
	secureConfigFile = func(string) error {
		secured = true
		return nil
	}

	if err := SaveConfig(path, Config{ServerURL: "https://it.example.com", NodeID: "node-1", Credential: "device-secret"}); err != nil {
		t.Fatalf("SaveConfig returned error: %v", err)
	}
}

func TestSaveConfigSecuresDirectoryBeforeCreatingTemporaryFile(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "ace")
	path := filepath.Join(directory, "agent.json")

	originalOperations := configOperations
	originalDirectorySecurity := secureConfigDirectory
	t.Cleanup(func() {
		configOperations = originalOperations
		secureConfigDirectory = originalDirectorySecurity
	})
	secured := false
	secureConfigDirectory = func(got string) error {
		if got != directory {
			t.Fatalf("secured directory = %q, want %q", got, directory)
		}
		secured = true
		return nil
	}
	configOperations.createTemp = func(dir, pattern string) (*os.File, error) {
		if !secured {
			t.Fatal("temporary config was created before directory security was applied")
		}
		return os.CreateTemp(dir, pattern)
	}

	if err := SaveConfig(path, Config{ServerURL: "https://it.example.com", NodeID: "node-1", Credential: "device-secret"}); err != nil {
		t.Fatalf("SaveConfig returned error: %v", err)
	}
}

func TestSaveConfigRetainsPreviousFileWhenReplacementFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, []byte("old config"), 0o600); err != nil {
		t.Fatalf("write old config: %v", err)
	}

	originalOperations := configOperations
	t.Cleanup(func() { configOperations = originalOperations })
	configOperations.rename = func(string, string) error { return errors.New("replace denied") }

	err := SaveConfig(path, Config{ServerURL: "https://it.example.com", NodeID: "node-1", Credential: "device-secret"})
	if err == nil {
		t.Fatal("SaveConfig succeeded after replacement failure")
	}
	contents, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read existing config: %v", readErr)
	}
	if got := string(contents); got != "old config" {
		t.Fatalf("existing config = %q, want old config", got)
	}
}

type closeRecorder struct {
	onClose func()
}

func (recorder closeRecorder) Close() error {
	recorder.onClose()
	return nil
}
