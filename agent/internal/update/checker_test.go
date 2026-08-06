package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckerAcceptsSignedNewerSameOriginRelease(t *testing.T) {
	installer := []byte("signed installer")
	checker, server, _ := newCheckerServer(t, installer, nil)
	defer server.Close()

	candidate, err := checker.Check(context.Background())

	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if candidate.Manifest.Version != "0.2.0" || candidate.InstallerURL != server.URL+"/download/setup.exe" {
		t.Fatalf("candidate = %#v", candidate)
	}
}

func TestCheckerRejectsInvalidSignatureAndUnsafeVersion(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "invalid signature", mutate: func(manifest *Manifest) {
			manifest.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
		}},
		{name: "equal version", mutate: func(manifest *Manifest) { manifest.Version = "0.1.0" }},
		{name: "downgrade", mutate: func(manifest *Manifest) { manifest.Version = "0.0.9" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checker, server, _ := newCheckerServer(t, []byte("installer"), test.mutate)
			defer server.Close()

			if _, err := checker.Check(context.Background()); err == nil {
				t.Fatal("Check() accepted unsafe manifest")
			}
		})
	}
}

func TestCheckerRejectsMissingOrNonCanonicalPublicKey(t *testing.T) {
	checker, server, _ := newCheckerServer(t, []byte("installer"), nil)
	defer server.Close()
	keys := []string{"", "not-base64", strings.TrimRight(checker.PublicKey, "="), " " + checker.PublicKey}
	for _, key := range keys {
		checker.PublicKey = key
		if _, err := checker.Check(context.Background()); err == nil {
			t.Fatalf("Check() accepted public key %q", key)
		}
	}
}

func TestCheckerBoundsManifestAndTimesOutDedicatedClient(t *testing.T) {
	t.Run("manifest larger than 64 KiB", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(writer, strings.Repeat("x", MaxManifestBytes+1))
		}))
		defer server.Close()
		checker := Checker{Origin: server.URL, CurrentVersion: "0.1.0", CurrentOS: "10.0.19045", PublicKey: canonicalTestPublicKey(t), StagingDir: t.TempDir()}

		if _, err := checker.Check(context.Background()); err == nil {
			t.Fatal("Check() accepted oversized manifest")
		}
	})

	t.Run("manifest declares installer above hard maximum", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			manifest := validManifest()
			manifest.Size = MaxArtifactBytes + 1
			manifest.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
			_ = json.NewEncoder(writer).Encode(manifest)
		}))
		defer server.Close()
		checker := Checker{Origin: server.URL, CurrentVersion: "0.1.0", CurrentOS: "10.0.19045", PublicKey: canonicalTestPublicKey(t), StagingDir: t.TempDir()}

		if _, err := checker.Check(context.Background()); err == nil {
			t.Fatal("Check() accepted installer above hard maximum")
		}
	})

	t.Run("request timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			time.Sleep(100 * time.Millisecond)
		}))
		defer server.Close()
		checker := Checker{Origin: server.URL, CurrentVersion: "0.1.0", CurrentOS: "10.0.19045", PublicKey: canonicalTestPublicKey(t), StagingDir: t.TempDir(), Timeout: 10 * time.Millisecond}

		started := time.Now()
		_, err := checker.Check(context.Background())
		if err == nil || time.Since(started) > 80*time.Millisecond {
			t.Fatalf("Check() error = %v after %s", err, time.Since(started))
		}
	})
}

func TestCheckerRejectsEveryCrossOriginRedirectForm(t *testing.T) {
	origin, err := url.Parse("https://updates.example")
	if err != nil {
		t.Fatal(err)
	}
	policy := rejectCrossOriginRedirects(origin)
	tests := []string{
		"https://evil.example/latest.json",
		"http://updates.example/latest.json",
		"https://updates.example:444/latest.json",
		"https://user@updates.example/latest.json",
	}
	for _, target := range tests {
		request, requestErr := http.NewRequest(http.MethodGet, target, nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		if err := policy(request, nil); err == nil {
			t.Fatalf("redirect to %q was accepted", target)
		}
	}
	for _, target := range []string{"https://updates.example/latest.json", "https://updates.example:443/latest.json"} {
		request, requestErr := http.NewRequest(http.MethodGet, target, nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		if err := policy(request, nil); err != nil {
			t.Fatalf("same-origin redirect to %q rejected: %v", target, err)
		}
	}
}

func TestCheckerRejectsUnrelatedHTTPRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{}`)
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/latest.json", http.StatusFound)
	}))
	defer server.Close()
	checker := Checker{Origin: server.URL, CurrentVersion: "0.1.0", CurrentOS: "10.0.19045", PublicKey: canonicalTestPublicKey(t), StagingDir: t.TempDir()}

	if _, err := checker.Check(context.Background()); err == nil {
		t.Fatal("Check() followed unrelated redirect")
	}
}

func TestStageWritesVerifiedInstallerWithSafeDerivedName(t *testing.T) {
	installer := []byte("verified installer bytes")
	checker, server, _ := newCheckerServer(t, installer, nil)
	defer server.Close()
	candidate, err := checker.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	staged, err := checker.Stage(context.Background(), candidate)

	if err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	wantPath := filepath.Join(checker.StagingDir, "AceAgentSetup-windows-amd64-V0.2.0.exe")
	if staged.InstallerPath != wantPath {
		t.Fatalf("InstallerPath = %q, want %q", staged.InstallerPath, wantPath)
	}
	contents, readErr := os.ReadFile(staged.InstallerPath)
	if readErr != nil || string(contents) != string(installer) {
		t.Fatalf("staged contents = %q, err = %v", contents, readErr)
	}
	if _, statErr := os.Stat(staged.InstallerPath + ".partial"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial file remains: %v", statErr)
	}
}

func TestStageRejectsHashAndExactSizeMismatchAndCleansPartial(t *testing.T) {
	tests := []struct {
		name      string
		installer []byte
		mutate    func(*Manifest)
	}{
		{name: "hash mismatch", installer: []byte("installer"), mutate: func(manifest *Manifest) { manifest.SHA256 = strings.Repeat("0", 64) }},
		{name: "short installer", installer: []byte("short"), mutate: func(manifest *Manifest) { manifest.Size++ }},
		{name: "long installer", installer: []byte("too long"), mutate: func(manifest *Manifest) { manifest.Size-- }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checker, server, _ := newCheckerServer(t, test.installer, test.mutate)
			defer server.Close()
			candidate, err := checker.Check(context.Background())
			if err != nil {
				t.Fatal(err)
			}

			if _, err := checker.Stage(context.Background(), candidate); err == nil {
				t.Fatal("Stage() accepted invalid installer")
			}
			entries, readErr := os.ReadDir(checker.StagingDir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("staging entries after failure = %v", entries)
			}
		})
	}
}

func TestStageRejectsUnknownLengthOverflowAndCleansPartial(t *testing.T) {
	installer := []byte("installer")
	checker, server, _ := newCheckerServer(t, installer, nil)
	defer server.Close()
	candidate, err := checker.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	checker.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          io.NopCloser(bytes.NewReader(append(installer, 'x'))),
			ContentLength: -1,
		}, nil
	})

	if _, err := checker.Stage(context.Background(), candidate); err == nil {
		t.Fatal("Stage() accepted oversized installer without Content-Length")
	}
	assertStagingDirectoryEmpty(t, checker.StagingDir)
}

func TestStageCleansPartialWhenDownloadBodyFails(t *testing.T) {
	installer := []byte("installer")
	checker, server, _ := newCheckerServer(t, installer, nil)
	defer server.Close()
	candidate, err := checker.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	readErr := errors.New("download interrupted")
	checker.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          &failingReadCloser{contents: []byte("inst"), err: readErr},
			ContentLength: -1,
		}, nil
	})

	if _, err := checker.Stage(context.Background(), candidate); !errors.Is(err, readErr) {
		t.Fatalf("Stage() error = %v, want %v", err, readErr)
	}
	assertStagingDirectoryEmpty(t, checker.StagingDir)
}

func TestCheckerClosesRedirectResponseWhenRejectingCrossOrigin(t *testing.T) {
	origin, err := url.Parse("https://updates.example")
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	checker := Checker{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "updates.example" {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://evil.example/latest.json"}},
				Body:       &closeRecorder{Reader: strings.NewReader("redirect"), closed: &closed},
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	})}

	if _, err := checker.fetchBounded(context.Background(), origin, origin.String()+stableManifestPath, MaxManifestBytes); err == nil {
		t.Fatal("fetchBounded() followed a cross-origin redirect")
	}
	if !closed {
		t.Fatal("cross-origin redirect response body was not closed")
	}
}

func TestStagePreservesExistingInstallerWhenAtomicReplaceFails(t *testing.T) {
	installer := []byte("new installer")
	checker, server, _ := newCheckerServer(t, installer, nil)
	defer server.Close()
	candidate, err := checker.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(checker.StagingDir, "AceAgentSetup-windows-amd64-V0.2.0.exe")
	oldInstaller := []byte("existing verified installer")
	if err := os.WriteFile(finalPath, oldInstaller, 0o600); err != nil {
		t.Fatal(err)
	}
	replaceErr := errors.New("atomic replace failed")
	checker.rename = func(string, string) error { return replaceErr }

	if _, err := checker.Stage(context.Background(), candidate); !errors.Is(err, replaceErr) {
		t.Fatalf("Stage() error = %v, want %v", err, replaceErr)
	}
	contents, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(contents, oldInstaller) {
		t.Fatalf("existing installer = %q, want %q", contents, oldInstaller)
	}
	if _, err := os.Stat(finalPath + ".partial"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial installer remains: %v", err)
	}
}

func TestCheckerRejectsMalformedManifestAndUnexpectedStatus(t *testing.T) {
	for _, handler := range []http.HandlerFunc{
		func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusBadGateway) },
		func(writer http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(writer, `{"schema":`) },
		func(writer http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(writer, `{}`+"\n"+`{}`) },
	} {
		server := httptest.NewServer(handler)
		checker := Checker{Origin: server.URL, CurrentVersion: "0.1.0", CurrentOS: "10.0.19045", PublicKey: canonicalTestPublicKey(t), StagingDir: t.TempDir()}
		if _, err := checker.Check(context.Background()); err == nil {
			server.Close()
			t.Fatal("Check() accepted malformed response")
		}
		server.Close()
	}
}

func TestStageReauthenticatesCandidateBeforeCreatingFiles(t *testing.T) {
	checker, server, _ := newCheckerServer(t, []byte("installer"), nil)
	defer server.Close()
	candidate, err := checker.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tampered := candidate
	tampered.Manifest.Version = "0.3.0"
	if _, err := checker.Stage(context.Background(), tampered); err == nil {
		t.Fatal("Stage() accepted tampered candidate")
	}
	wrongURL := candidate
	wrongURL.InstallerURL = server.URL + "/download/other.exe"
	if _, err := checker.Stage(context.Background(), wrongURL); err == nil {
		t.Fatal("Stage() accepted mismatched installer URL")
	}
	entries, err := os.ReadDir(checker.StagingDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging entries = %v, err = %v", entries, err)
	}
}

func newCheckerServer(t *testing.T, installer []byte, mutate func(*Manifest)) (Checker, *httptest.Server, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/downloads/windows/stable/latest.json":
			hash := sha256.Sum256(installer)
			manifest := validManifest()
			manifest.URL = "/download/setup.exe"
			manifest.Size = int64(len(installer))
			manifest.SHA256 = hex.EncodeToString(hash[:])
			if mutate != nil {
				mutate(&manifest)
			}
			forcedSignature := manifest.Signature
			manifest.Signature = ""
			signed, signErr := Sign(manifest, privateKey)
			if signErr != nil {
				t.Errorf("Sign() error = %v", signErr)
				writer.WriteHeader(http.StatusInternalServerError)
				return
			}
			if forcedSignature != "" {
				signed.Signature = forcedSignature
			}
			_ = json.NewEncoder(writer).Encode(signed)
		case "/download/setup.exe":
			_, _ = writer.Write(installer)
		default:
			http.NotFound(writer, request)
		}
	}))
	checker := Checker{
		Origin:         server.URL,
		CurrentVersion: "0.1.0",
		CurrentOS:      "10.0.19045",
		PublicKey:      base64.StdEncoding.EncodeToString(publicKey),
		StagingDir:     t.TempDir(),
		Timeout:        time.Second,
	}
	return checker, server, privateKey
}

func canonicalTestPublicKey(t *testing.T) string {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(publicKey)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type failingReadCloser struct {
	contents []byte
	err      error
}

func (r *failingReadCloser) Read(buffer []byte) (int, error) {
	if len(r.contents) == 0 {
		return 0, r.err
	}
	n := copy(buffer, r.contents)
	r.contents = r.contents[n:]
	return n, nil
}

func (*failingReadCloser) Close() error { return nil }

type closeRecorder struct {
	io.Reader
	closed *bool
}

func (r *closeRecorder) Close() error {
	*r.closed = true
	return nil
}

func assertStagingDirectoryEmpty(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging entries after failure = %v", entries)
	}
}
