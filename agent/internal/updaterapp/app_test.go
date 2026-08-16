package updaterapp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"aceitcenter.local/platform/agent/internal/update"
)

func TestCheckDownloadsAuthenticatedCandidateAndWritesBoundedJSON(t *testing.T) {
	server, publicKey := newUpdateServer(t, "0.4.11", []byte("signed installer"), nil)
	defer server.Close()
	var output bytes.Buffer

	err := Run(context.Background(), []string{
		"check",
		"--origin", server.URL,
		"--current-version", "0.4.10",
		"--current-os", "10.0.19045",
		"--staging", t.TempDir(),
	}, &output, Dependencies{UpdatePublicKey: publicKey, Version: "0.4.11"})

	if err != nil {
		t.Fatalf("Run(check) error = %v", err)
	}
	result, err := update.DecodeCheckResult(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Available || result.Version != "0.4.11" || result.URL != server.URL+"/installer.exe" || !filepath.IsAbs(result.InstallerPath) {
		t.Fatalf("check result = %#v", result)
	}
	if contents, err := os.ReadFile(result.InstallerPath); err != nil || string(contents) != "signed installer" {
		t.Fatalf("staged installer = %q, %v", contents, err)
	}
}

func TestCheckReturnsUnavailableForCurrentVersion(t *testing.T) {
	server, publicKey := newUpdateServer(t, "0.4.10", []byte("installer"), nil)
	defer server.Close()
	var output bytes.Buffer

	err := Run(context.Background(), []string{
		"check",
		"--origin", server.URL,
		"--current-version", "0.4.10",
		"--current-os", "10.0.19045",
		"--staging", t.TempDir(),
	}, &output, Dependencies{UpdatePublicKey: publicKey})

	if err != nil {
		t.Fatalf("Run(check) error = %v", err)
	}
	result, err := update.DecodeCheckResult(bytes.NewReader(output.Bytes()))
	if err != nil || result != (update.CheckResult{}) {
		t.Fatalf("result = %#v, %v", result, err)
	}
}

func TestApplyReceivesOnlyLocalReleaseData(t *testing.T) {
	directory := t.TempDir()
	wantInstaller := filepath.Join(directory, "setup.exe")
	wantAgent := filepath.Join(directory, "program", "AceAgent.exe")
	wantBackup := filepath.Join(directory, "updates", "AceAgent.lkg.exe")
	var got update.HelperOptions

	err := Run(context.Background(), []string{
		"apply",
		"--installer", wantInstaller,
		"--agent", wantAgent,
		"--backup", wantBackup,
		"--version", "0.4.11",
	}, &bytes.Buffer{}, Dependencies{RunHelper: func(_ context.Context, options update.HelperOptions) error {
		got = options
		return nil
	}})

	if err != nil {
		t.Fatalf("Run(apply) error = %v", err)
	}
	if got.InstallerPath != wantInstaller || got.ExecutablePath != wantAgent || got.BackupPath != wantBackup || got.StagingDir != filepath.Dir(wantBackup) || got.Version != "0.4.11" {
		t.Fatalf("helper options = %#v", got)
	}
}

func TestRunRejectsUnknownIncompleteRelativeAndExtraArguments(t *testing.T) {
	directory := t.TempDir()
	validApply := []string{"apply", "--installer", filepath.Join(directory, "setup.exe"), "--agent", filepath.Join(directory, "AceAgent.exe"), "--backup", filepath.Join(directory, "AceAgent.lkg.exe"), "--version", "0.4.11"}
	tests := [][]string{
		nil,
		{"unknown"},
		{"check", "--origin", "https://it.example"},
		{"check", "--origin", "https://it.example", "--current-version", "0.4.10", "--current-os", "10.0.19045", "--staging", "relative"},
		append(append([]string(nil), validApply...), "extra"),
		{"apply", "--installer", "setup.exe", "--agent", filepath.Join(directory, "AceAgent.exe"), "--backup", filepath.Join(directory, "AceAgent.lkg.exe"), "--version", "0.4.11"},
		{"apply", "--credential", "secret"},
		{"version", "extra"},
	}
	for _, args := range tests {
		if err := Run(context.Background(), args, &bytes.Buffer{}, Dependencies{}); err == nil {
			t.Fatalf("Run(%q) accepted invalid arguments", args)
		}
	}
}

func TestCheckRejectsInvalidSignatureAndCrossOriginRedirect(t *testing.T) {
	t.Run("invalid signature", func(t *testing.T) {
		server, publicKey := newUpdateServer(t, "0.4.11", []byte("installer"), func(manifest *update.Manifest) {
			manifest.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
		})
		defer server.Close()
		err := Run(context.Background(), []string{"check", "--origin", server.URL, "--current-version", "0.4.10", "--current-os", "10.0.19045", "--staging", t.TempDir()}, &bytes.Buffer{}, Dependencies{UpdatePublicKey: publicKey})
		if err == nil {
			t.Fatal("Run(check) accepted invalid signature")
		}
	})

	t.Run("cross-origin redirect", func(t *testing.T) {
		target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(`{}`))
		}))
		defer target.Close()
		origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, target.URL+"/latest.json", http.StatusFound)
		}))
		defer origin.Close()
		_, publicKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		err = Run(context.Background(), []string{"check", "--origin", origin.URL, "--current-version", "0.4.10", "--current-os", "10.0.19045", "--staging", t.TempDir()}, &bytes.Buffer{}, Dependencies{UpdatePublicKey: base64.StdEncoding.EncodeToString(publicKey)})
		if err == nil {
			t.Fatal("Run(check) followed cross-origin redirect")
		}
	})
}

func TestVersionWritesOnlyBuildVersion(t *testing.T) {
	var output bytes.Buffer
	if err := Run(context.Background(), []string{"version"}, &output, Dependencies{Version: "0.4.11"}); err != nil {
		t.Fatal(err)
	}
	if output.String() != "0.4.11\n" {
		t.Fatalf("version output = %q", output.String())
	}
}

func TestRunRejectsNilContext(t *testing.T) {
	if err := Run(nil, []string{"version"}, &bytes.Buffer{}, Dependencies{Version: "0.4.11"}); err == nil {
		t.Fatal("Run() accepted nil context")
	}
}

func newUpdateServer(t *testing.T, version string, installer []byte, mutateSigned func(*update.Manifest)) (*httptest.Server, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(installer)
	manifest := update.Manifest{
		Schema:      update.ManifestSchema,
		Channel:     update.ManifestChannel,
		Version:     version,
		PublishedAt: time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC),
		MinimumOS:   "10.0.17763",
		URL:         "/installer.exe",
		Size:        int64(len(installer)),
		SHA256:      hex.EncodeToString(hash[:]),
	}
	manifest, err = update.Sign(manifest, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if mutateSigned != nil {
		mutateSigned(&manifest)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/downloads/windows/stable/latest.json":
			_ = json.NewEncoder(writer).Encode(manifest)
		case "/installer.exe":
			_, _ = writer.Write(installer)
		default:
			http.NotFound(writer, request)
		}
	}))
	return server, base64.StdEncoding.EncodeToString(publicKey)
}
