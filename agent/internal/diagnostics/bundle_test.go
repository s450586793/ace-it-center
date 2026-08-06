package diagnostics

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aceitcenter.local/platform/agent/internal/agent"
	"aceitcenter.local/platform/agent/internal/app"
)

func TestDiagnosticBundleExcludesCredential(t *testing.T) {
	path, err := Create(context.Background(), testOptions(t, "device-secret", "one-time"))
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	content := readZipText(t, path)
	if strings.Contains(content, "device-secret") || strings.Contains(content, "one-time") {
		t.Fatalf("secret leaked in diagnostic bundle: %s", content)
	}
}

func TestDiagnosticBundleExcludesPendingPairingCredential(t *testing.T) {
	options := testOptions(t, "device-secret", "one-time")
	options.Config.PendingPairing = &agent.PendingPairing{
		ServerURL: "https://it.example", PairingID: "pairing-1", Credential: "pairing-secret", ExpiresAt: time.Now().Add(time.Minute),
	}
	options.Status.Error = "pairing failed for pairing-secret"
	if err := os.WriteFile(options.LogPath, []byte("pairing credential=pairing-secret"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	path, err := Create(context.Background(), options)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if content := readZipText(t, path); strings.Contains(content, "pairing-secret") || strings.Contains(content, "pairing_credential") {
		t.Fatalf("pending pairing credential leaked in diagnostic bundle: %s", content)
	}
}

func TestDiagnosticBundleIncludesExpectedFilesAndBoundsLogs(t *testing.T) {
	options := testOptions(t, "device-secret", "one-time")
	options.MaxLogBytes = 12
	if err := os.WriteFile(options.LogPath, []byte("0123456789abcdef"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	path, err := Create(context.Background(), options)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	entries := readZipEntries(t, path)
	for _, name := range []string{"build.json", "config.json", "status.json", "system.json", "logs/agent.log"} {
		if _, ok := entries[name]; !ok {
			t.Fatalf("diagnostic bundle missing %q: %#v", name, entries)
		}
	}
	if got := string(entries["logs/agent.log"]); got != "456789abcdef" {
		t.Fatalf("bounded log = %q, want recent 12 bytes", got)
	}
}

func TestDiagnosticBundleRedactsSecretsAcrossLogTailBoundary(t *testing.T) {
	credential := "credential-boundary"
	token := "token-boundary"
	options := testOptions(t, credential, token)
	options.MaxLogBytes = 12
	log := []byte("prefix-" + credential + "-middle-" + token)
	if err := os.WriteFile(options.LogPath, log, 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	path, err := Create(context.Background(), options)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	got := readZipEntries(t, path)["logs/agent.log"]
	if len(got) > int(options.MaxLogBytes) {
		t.Fatalf("log copy length = %d, want at most %d", len(got), options.MaxLogBytes)
	}
	for _, secretPart := range []string{credential, token, "boundary"} {
		if bytes.Contains(got, []byte(secretPart)) {
			t.Fatalf("secret or boundary fragment %q leaked in %q", secretPart, got)
		}
	}
}

func TestDiagnosticBundleDropsLeadingSecretFragmentBeforeBounding(t *testing.T) {
	credential := "credential-boundary"
	options := testOptions(t, credential, "one-time")
	options.MaxLogBytes = 12
	if err := os.WriteFile(options.LogPath, []byte("prefix-"+credential+"?"+credential), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	path, err := Create(context.Background(), options)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if got := string(readZipEntries(t, path)["logs/agent.log"]); got != "?[REDACTED]" {
		t.Fatalf("bounded log = %q, want fully redacted trailing record", got)
	}
}

func TestDiagnosticBundleRedactsOverlappingSecretsInNormalLog(t *testing.T) {
	options := testOptions(t, "device", "device-secret")
	if err := os.WriteFile(options.LogPath, []byte("credential=device enrollment=device-secret"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	path, err := Create(context.Background(), options)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	got := string(readZipEntries(t, path)["logs/agent.log"])
	if strings.Contains(got, "device") || strings.Contains(got, "secret") {
		t.Fatalf("overlapping secret leaked in log: %q", got)
	}
}

func TestDiagnosticBundleRedactsOverlappingSecretsAtTailBoundary(t *testing.T) {
	options := testOptions(t, "device", "device-secret")
	options.MaxLogBytes = 12
	if err := os.WriteFile(options.LogPath, []byte("prefix-device-secret"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	path, err := Create(context.Background(), options)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	got := readZipEntries(t, path)["logs/agent.log"]
	if len(got) > int(options.MaxLogBytes) {
		t.Fatalf("log copy length = %d, want at most %d", len(got), options.MaxLogBytes)
	}
	if bytes.Contains(got, []byte("device")) || bytes.Contains(got, []byte("secret")) {
		t.Fatalf("overlapping secret leaked at log tail: %q", got)
	}
}

func testOptions(t *testing.T, credential, token string) Options {
	t.Helper()
	directory := t.TempDir()
	return Options{
		OutputDir:       directory,
		Config:          agent.Config{ServerURL: "https://it.example", NodeID: "node-1", Credential: credential},
		Status:          app.StatusSnapshot{State: app.StateError, NodeID: "node-1", ServerURL: "https://it.example", Error: "enrollment " + token + " failed for " + credential},
		EnrollmentToken: token,
		LogPath:         filepath.Join(directory, "agent.log"),
	}
}

func readZipText(t *testing.T, path string) string {
	t.Helper()
	entries := readZipEntries(t, path)
	var content strings.Builder
	for _, data := range entries {
		content.Write(data)
	}
	return content.String()
}

func readZipEntries(t *testing.T, path string) map[string][]byte {
	t.Helper()
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	t.Cleanup(func() { _ = archive.Close() })
	entries := make(map[string][]byte, len(archive.File))
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatalf("open zip file %q: %v", file.Name, err)
		}
		data, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			t.Fatalf("read zip file %q: %v", file.Name, err)
		}
		entries[file.Name] = data
	}
	return entries
}
