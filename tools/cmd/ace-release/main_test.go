package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aceitcenter.local/platform/internal/release"
)

func TestKeygenWritesMatchingKeysWithoutExposingOrOverwritingPrivateKey(t *testing.T) {
	directory := t.TempDir()
	privatePath := filepath.Join(directory, "update-signing.key")
	publicPath := privatePath + ".pub"
	stdout, stderr, exitCode := invoke("keygen", "-private", privatePath, "-public", publicPath)
	if exitCode != 0 {
		t.Fatalf("keygen exit code = %d, stderr = %q", exitCode, stderr)
	}
	privateKey := readEncodedKey(t, privatePath, ed25519.PrivateKeySize)
	publicKey := readEncodedKey(t, publicPath, ed25519.PublicKeySize)
	if !bytes.Equal(privateKey[ed25519.PrivateKeySize-ed25519.PublicKeySize:], publicKey) {
		t.Fatal("public key does not match private key")
	}
	privateInfo, err := os.Stat(privatePath)
	if err != nil {
		t.Fatalf("stat private key: %v", err)
	}
	if got := privateInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("private key permissions = %o, want 600", got)
	}
	if strings.Contains(stdout+stderr, strings.TrimSpace(string(mustReadFile(t, privatePath)))) {
		t.Fatal("keygen output contains private key")
	}

	originalPrivate := append([]byte(nil), privateKey...)
	_, stderr, exitCode = invoke("keygen", "-private", privatePath, "-public", publicPath)
	if exitCode == 0 || !strings.Contains(stderr, "already exists") {
		t.Fatalf("second keygen exit code = %d, stderr = %q", exitCode, stderr)
	}
	if got := readEncodedKey(t, privatePath, ed25519.PrivateKeySize); !bytes.Equal(got, originalPrivate) {
		t.Fatal("keygen overwrote the existing private key")
	}
}

func TestKeygenDoesNotLeavePrivateKeyWhenPublicDestinationIsUnavailable(t *testing.T) {
	directory := t.TempDir()
	privatePath := filepath.Join(directory, "update-signing.key")
	publicPath := filepath.Join(directory, "missing", "update-signing.key.pub")
	_, _, exitCode := invoke("keygen", "-private", privatePath, "-public", publicPath)
	if exitCode == 0 {
		t.Fatal("keygen succeeded with unavailable public destination")
	}
	if _, err := os.Stat(privatePath); !os.IsNotExist(err) {
		t.Fatalf("private key remained after keygen failure: %v", err)
	}
}

func TestKeygenRefusesHalfExistingPairWithoutCreatingPrivateKey(t *testing.T) {
	directory := t.TempDir()
	privatePath := filepath.Join(directory, "update-signing.key")
	publicPath := privatePath + ".pub"
	if err := os.WriteFile(publicPath, []byte("existing public key"), 0o644); err != nil {
		t.Fatalf("write existing public key: %v", err)
	}
	_, stderr, exitCode := invoke("keygen", "-private", privatePath, "-public", publicPath)
	if exitCode == 0 || !strings.Contains(stderr, "already exists") {
		t.Fatalf("keygen exit code = %d, stderr = %q", exitCode, stderr)
	}
	if _, err := os.Stat(privatePath); !os.IsNotExist(err) {
		t.Fatalf("private key was created beside an existing public key: %v", err)
	}
	if got := string(mustReadFile(t, publicPath)); got != "existing public key" {
		t.Fatalf("existing public key = %q", got)
	}
}

func TestGenerateKeyPairPublishesAndSyncsInDurableOrder(t *testing.T) {
	privateDirectory := filepath.Join(t.TempDir(), "private")
	publicDirectory := filepath.Join(t.TempDir(), "public")
	if err := os.Mkdir(privateDirectory, 0o700); err != nil {
		t.Fatalf("create private directory: %v", err)
	}
	if err := os.Mkdir(publicDirectory, 0o700); err != nil {
		t.Fatalf("create public directory: %v", err)
	}
	privatePath := filepath.Join(privateDirectory, "update-signing.key")
	publicPath := filepath.Join(publicDirectory, "update-signing.key.pub")

	original := keyOperations
	t.Cleanup(func() { keyOperations = original })
	events := make([]string, 0, 4)
	keyOperations.link = func(oldPath, newPath string) error {
		switch newPath {
		case publicPath:
			events = append(events, "link public")
		case privatePath:
			events = append(events, "link private")
		default:
			t.Fatalf("unexpected link destination: %s", filepath.Base(newPath))
		}
		return os.Link(oldPath, newPath)
	}
	keyOperations.syncDirectory = func(directory string) error {
		temporaries, err := filepath.Glob(filepath.Join(directory, ".ace-release-*.tmp"))
		if err != nil {
			t.Fatalf("glob temporary keys: %v", err)
		}
		if len(temporaries) != 0 {
			return errors.New("temporary key remained before directory sync")
		}
		switch directory {
		case publicDirectory:
			events = append(events, "sync public")
		case privateDirectory:
			events = append(events, "sync private")
		default:
			t.Fatalf("unexpected sync directory: %s", filepath.Base(directory))
		}
		return syncDirectory(directory)
	}

	if err := generateKeyPair(privatePath, publicPath); err != nil {
		t.Fatalf("generateKeyPair returned error: %v", err)
	}
	wantEvents := []string{"link public", "sync public", "link private", "sync private"}
	if strings.Join(events, ",") != strings.Join(wantEvents, ",") {
		t.Fatalf("publication events = %q, want %q", events, wantEvents)
	}
}

func TestGenerateKeyPairRollsBackEveryLinkAndSyncFailure(t *testing.T) {
	tests := []struct {
		name          string
		failLink      int
		failSync      int
		wantLinkCalls int
		wantSyncCalls int
	}{
		{name: "public link", failLink: 1, wantLinkCalls: 1, wantSyncCalls: 1},
		{name: "public sync", failSync: 1, wantLinkCalls: 1, wantSyncCalls: 2},
		{name: "private link", failLink: 2, wantLinkCalls: 2, wantSyncCalls: 2},
		{name: "private sync", failSync: 2, wantLinkCalls: 2, wantSyncCalls: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			privatePath := filepath.Join(directory, "update-signing.key")
			publicPath := privatePath + ".pub"
			original := keyOperations
			t.Cleanup(func() { keyOperations = original })
			linkCalls := 0
			syncCalls := 0
			keyOperations.link = func(oldPath, newPath string) error {
				linkCalls++
				if linkCalls == test.failLink {
					return errors.New("injected link failure")
				}
				return os.Link(oldPath, newPath)
			}
			keyOperations.syncDirectory = func(path string) error {
				syncCalls++
				if syncCalls == test.failSync {
					return errors.New("injected sync failure")
				}
				return syncDirectory(path)
			}

			if err := generateKeyPair(privatePath, publicPath); err == nil {
				t.Fatal("generateKeyPair succeeded after injected failure")
			}
			if linkCalls != test.wantLinkCalls || syncCalls != test.wantSyncCalls {
				t.Fatalf("calls = link:%d sync:%d, want link:%d sync:%d", linkCalls, syncCalls, test.wantLinkCalls, test.wantSyncCalls)
			}
			for _, path := range []string{privatePath, publicPath} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("key output remained after failure: %s (%v)", filepath.Base(path), err)
				}
			}
			temporaries, err := filepath.Glob(filepath.Join(directory, ".ace-release-*.tmp"))
			if err != nil {
				t.Fatalf("glob temporary keys: %v", err)
			}
			if len(temporaries) != 0 {
				t.Fatalf("temporary keys remained after failure: %v", temporaries)
			}
		})
	}
}

func TestGenerateKeyPairCleansSensitiveTempAfterPreparationFailure(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*faultingKeyFile)
	}{
		{name: "write", configure: func(file *faultingKeyFile) { file.failWrite = true }},
		{name: "file sync", configure: func(file *faultingKeyFile) { file.failSync = true }},
		{name: "close", configure: func(file *faultingKeyFile) { file.failClose = 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			privatePath := filepath.Join(directory, "sensitive-private.key")
			publicPath := privatePath + ".pub"
			original := keyOperations
			t.Cleanup(func() { keyOperations = original })
			keyOperations.createTemp = func(dir, pattern string) (keyTemporaryFile, error) {
				file, err := os.CreateTemp(dir, pattern)
				if err != nil {
					return nil, err
				}
				faulting := &faultingKeyFile{File: file}
				test.configure(faulting)
				return faulting, nil
			}
			syncCalls := 0
			keyOperations.syncDirectory = func(path string) error {
				syncCalls++
				return syncDirectory(path)
			}

			err := generateKeyPair(privatePath, publicPath)
			if err == nil {
				t.Fatal("generateKeyPair succeeded after injected preparation failure")
			}
			if strings.Contains(err.Error(), directory) || strings.Contains(err.Error(), filepath.Base(privatePath)) {
				t.Fatalf("error exposed sensitive path: %q", err)
			}
			if syncCalls == 0 {
				t.Fatal("temporary key cleanup did not sync its directory")
			}
			assertNoGeneratedKeys(t, directory, privatePath, publicPath)
		})
	}
}

func TestGenerateKeyPairReportsIncompleteSensitiveTempCleanup(t *testing.T) {
	tests := []struct {
		name      string
		configure func(directory string)
	}{
		{name: "remove", configure: func(directory string) {
			keyOperations.remove = func(path string) error {
				if filepath.Dir(path) == directory && strings.HasPrefix(filepath.Base(path), ".ace-release-") {
					return errors.New("injected sensitive remove failure")
				}
				return os.Remove(path)
			}
		}},
		{name: "directory sync", configure: func(string) {
			keyOperations.syncDirectory = func(string) error {
				return errors.New("injected sensitive directory sync failure")
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			privatePath := filepath.Join(directory, "sensitive-private.key")
			publicPath := privatePath + ".pub"
			original := keyOperations
			t.Cleanup(func() { keyOperations = original })
			keyOperations.createTemp = func(dir, pattern string) (keyTemporaryFile, error) {
				file, err := os.CreateTemp(dir, pattern)
				if err != nil {
					return nil, err
				}
				return &faultingKeyFile{File: file, failWrite: true}, nil
			}
			test.configure(directory)

			err := generateKeyPair(privatePath, publicPath)
			if err == nil || err.Error() != "rollback was incomplete" {
				t.Fatalf("generateKeyPair error = %q, want fixed incomplete rollback error", err)
			}
			if strings.Contains(err.Error(), directory) || strings.Contains(err.Error(), filepath.Base(privatePath)) {
				t.Fatalf("error exposed sensitive path: %q", err)
			}
		})
	}
}

func TestGenerateKeyPairReportsIncompletePublishedKeyRollback(t *testing.T) {
	tests := []struct {
		name      string
		configure func(publicPath string)
	}{
		{name: "remove", configure: func(publicPath string) {
			keyOperations.remove = func(path string) error {
				if path == publicPath {
					return errors.New("injected published remove failure")
				}
				return os.Remove(path)
			}
		}},
		{name: "directory sync", configure: func(string) {
			syncCalls := 0
			keyOperations.syncDirectory = func(path string) error {
				syncCalls++
				if syncCalls == 2 {
					return errors.New("injected rollback directory sync failure")
				}
				return syncDirectory(path)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			privatePath := filepath.Join(directory, "sensitive-private.key")
			publicPath := privatePath + ".pub"
			original := keyOperations
			t.Cleanup(func() { keyOperations = original })
			linkCalls := 0
			keyOperations.link = func(oldPath, newPath string) error {
				linkCalls++
				if linkCalls == 2 {
					return errors.New("injected private link failure")
				}
				return os.Link(oldPath, newPath)
			}
			test.configure(publicPath)

			err := generateKeyPair(privatePath, publicPath)
			if err == nil || err.Error() != "rollback was incomplete" {
				t.Fatalf("generateKeyPair error = %q, want fixed incomplete rollback error", err)
			}
			if strings.Contains(err.Error(), directory) || strings.Contains(err.Error(), filepath.Base(privatePath)) {
				t.Fatalf("error exposed sensitive path: %q", err)
			}
			if _, err := os.Lstat(privatePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("private key remained after rollback failure: %v", err)
			}
		})
	}
}

type faultingKeyFile struct {
	*os.File
	failWrite bool
	failSync  bool
	failClose int
}

func (file *faultingKeyFile) Write(contents []byte) (int, error) {
	if file.failWrite {
		return 0, errors.New("injected key write failure")
	}
	return file.File.Write(contents)
}

func (file *faultingKeyFile) Sync() error {
	if file.failSync {
		return errors.New("injected key file sync failure")
	}
	return file.File.Sync()
}

func (file *faultingKeyFile) Close() error {
	if file.failClose > 0 {
		file.failClose--
		return errors.New("injected key close failure")
	}
	return file.File.Close()
}

func assertNoGeneratedKeys(t *testing.T, directory string, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("generated key remained after failure: %s (%v)", filepath.Base(path), err)
		}
	}
	temporaries, err := filepath.Glob(filepath.Join(directory, ".ace-release-*.tmp"))
	if err != nil {
		t.Fatalf("glob temporary keys: %v", err)
	}
	if len(temporaries) != 0 {
		t.Fatalf("temporary keys remained after failure: %v", temporaries)
	}
}

func TestPublicKeyDerivesMatchingPublicMaterialWithoutExposingPrivateKey(t *testing.T) {
	directory := t.TempDir()
	privatePath := filepath.Join(directory, "update-signing.key")
	publicPath := privatePath + ".pub"
	if _, stderr, exitCode := invoke("keygen", "-private", privatePath, "-public", publicPath); exitCode != 0 {
		t.Fatalf("keygen exit code = %d, stderr = %q", exitCode, stderr)
	}
	stdout, stderr, exitCode := invoke("public-key", "-private", privatePath)
	if exitCode != 0 {
		t.Fatalf("public-key exit code = %d, stderr = %q", exitCode, stderr)
	}
	if strings.TrimSpace(stdout) != strings.TrimSpace(string(mustReadFile(t, publicPath))) {
		t.Fatalf("public-key output = %q, want public key file", stdout)
	}
	if strings.Contains(stdout+stderr, strings.TrimSpace(string(mustReadFile(t, privatePath)))) {
		t.Fatal("public-key output contains private key")
	}
}

func TestPublicKeyRejectsNonCanonicalPrivateKeyFiles(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey returned error: %v", err)
	}
	canonical := base64.StdEncoding.EncodeToString(privateKey)
	tests := map[string]string{
		"missing newline":   canonical,
		"leading space":     " " + canonical + "\n",
		"trailing space":    canonical + " \n",
		"CRLF":              canonical + "\r\n",
		"extra newline":     canonical + "\n\n",
		"excessive padding": canonical + "=\n",
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private.key")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatalf("write key: %v", err)
			}
			stdout, stderr, exitCode := invoke("public-key", "-private", path)
			if exitCode == 0 || stdout != "" {
				t.Fatalf("public-key exit code = %d, stdout = %q", exitCode, stdout)
			}
			if strings.Contains(stderr, canonical) || strings.Contains(stderr, path) {
				t.Fatalf("public-key stderr exposed key material or path: %q", stderr)
			}
		})
	}
}

func TestPublicKeyRejectsPrivateKeyWithCorruptPublicSuffix(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey returned error: %v", err)
	}
	privateKey[len(privateKey)-1] ^= 1
	encoded := base64.StdEncoding.EncodeToString(privateKey)
	path := filepath.Join(t.TempDir(), "private.key")
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	stdout, stderr, exitCode := invoke("public-key", "-private", path)
	if exitCode == 0 || stdout != "" {
		t.Fatalf("public-key exit code = %d, stdout = %q", exitCode, stdout)
	}
	if strings.Contains(stderr, encoded) || strings.Contains(stderr, path) {
		t.Fatalf("public-key stderr exposed key material or path: %q", stderr)
	}
}

func TestPublicKeyRejectsOversizedRegularKeyWithoutExposingInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized-private.key")
	contents := strings.Repeat("sensitive-key-material", 1<<16)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write oversized key: %v", err)
	}
	stdout, stderr, exitCode := invoke("public-key", "-private", path)
	if exitCode == 0 || stdout != "" {
		t.Fatalf("public-key exit code = %d, stdout = %q", exitCode, stdout)
	}
	if strings.Contains(stderr, "sensitive-key-material") || strings.Contains(stderr, path) {
		t.Fatalf("public-key stderr exposed key material or path: %q", stderr)
	}
}

func TestSignAndVerifyUseExactArtifactBytesAndCompatibleManifestSignature(t *testing.T) {
	directory := t.TempDir()
	privatePath := filepath.Join(directory, "update-signing.key")
	publicPath := privatePath + ".pub"
	artifactPath := filepath.Join(directory, "AceAgentSetup.exe")
	manifestPath := filepath.Join(directory, "latest.json")
	artifact := []byte("real installer bytes\x00\x01")
	if err := os.WriteFile(artifactPath, artifact, 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if _, stderr, exitCode := invoke("keygen", "-private", privatePath, "-public", publicPath); exitCode != 0 {
		t.Fatalf("keygen exit code = %d, stderr = %q", exitCode, stderr)
	}
	stdout, stderr, exitCode := invoke(
		"sign",
		"-private", privatePath,
		"-artifact", artifactPath,
		"-manifest", manifestPath,
		"-version", "0.2.0",
		"-published-at", "2026-07-27T00:00:00Z",
		"-minimum-os", "10.0.17763",
		"-url", "/downloads/windows/stable/AceAgentSetup.exe",
	)
	if exitCode != 0 {
		t.Fatalf("sign exit code = %d, stderr = %q", exitCode, stderr)
	}
	if strings.Contains(stdout+stderr, strings.TrimSpace(string(mustReadFile(t, privatePath)))) {
		t.Fatal("sign output contains private key")
	}

	var manifest release.Manifest
	if err := json.Unmarshal(mustReadFile(t, manifestPath), &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	wantHash := sha256.Sum256(artifact)
	if manifest.Schema != 1 || manifest.Channel != "stable" || manifest.Version != "0.2.0" || manifest.Size != int64(len(artifact)) || manifest.SHA256 != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("manifest metadata = %#v", manifest)
	}
	publicKey := ed25519.PublicKey(readEncodedKey(t, publicPath, ed25519.PublicKeySize))
	if err := release.Verify(manifest, publicKey); err != nil {
		t.Fatalf("release.Verify rejected CLI manifest: %v", err)
	}

	if _, stderr, exitCode = invoke("verify", "-public", publicPath, "-manifest", manifestPath, "-artifact", artifactPath); exitCode != 0 {
		t.Fatalf("verify exit code = %d, stderr = %q", exitCode, stderr)
	}
	sameSizeTamper := append([]byte(nil), artifact...)
	sameSizeTamper[0] ^= 1
	if err := os.WriteFile(artifactPath, sameSizeTamper, 0o644); err != nil {
		t.Fatalf("tamper artifact hash: %v", err)
	}
	if _, stderr, exitCode = invoke("verify", "-public", publicPath, "-manifest", manifestPath, "-artifact", artifactPath); exitCode == 0 || !strings.Contains(stderr, "SHA-256") {
		t.Fatalf("same-size tampered verify exit code = %d, stderr = %q", exitCode, stderr)
	}
	if err := os.WriteFile(artifactPath, append(artifact, 'x'), 0o644); err != nil {
		t.Fatalf("tamper artifact: %v", err)
	}
	if _, stderr, exitCode = invoke("verify", "-public", publicPath, "-manifest", manifestPath, "-artifact", artifactPath); exitCode == 0 || !strings.Contains(stderr, "artifact") {
		t.Fatalf("tampered verify exit code = %d, stderr = %q", exitCode, stderr)
	}
}

func TestVerifyRejectsUnknownDuplicateAndTrailingManifestJSON(t *testing.T) {
	directory := t.TempDir()
	privatePath := filepath.Join(directory, "key")
	publicPath := privatePath + ".pub"
	artifactPath := filepath.Join(directory, "artifact.exe")
	manifestPath := filepath.Join(directory, "latest.json")
	if err := os.WriteFile(artifactPath, []byte("artifact"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if _, stderr, exitCode := invoke("keygen", "-private", privatePath, "-public", publicPath); exitCode != 0 {
		t.Fatalf("keygen exit code = %d, stderr = %q", exitCode, stderr)
	}
	if _, stderr, exitCode := invoke("sign", "-private", privatePath, "-artifact", artifactPath, "-manifest", manifestPath, "-version", "1.0.0", "-published-at", "2026-07-27T00:00:00Z", "-minimum-os", "10.0.17763", "-url", "/artifact.exe"); exitCode != 0 {
		t.Fatalf("sign exit code = %d, stderr = %q", exitCode, stderr)
	}
	valid := strings.TrimSpace(string(mustReadFile(t, manifestPath)))
	cases := map[string]string{
		"unknown field":             strings.TrimSuffix(valid, "}") + `,"unexpected":true}`,
		"case alias field":          strings.Replace(valid, `"schema"`, `"Schema"`, 1),
		"duplicate field":           strings.Replace(valid, `{`, `{"schema":1,`, 1),
		"case-fold duplicate field": strings.Replace(valid, `{`, `{"Schema":1,`, 1),
		"array root":                `[` + valid + `]`,
		"scalar root":               `true`,
		"trailing data":             valid + `{}`,
	}
	for name, contents := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(directory, strings.ReplaceAll(name, " ", "-")+".json")
			if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
				t.Fatalf("write malformed manifest: %v", err)
			}
			_, stderr, exitCode := invoke("verify", "-public", publicPath, "-manifest", path, "-artifact", artifactPath)
			if exitCode == 0 || !strings.Contains(stderr, "manifest") {
				t.Fatalf("verify exit code = %d, stderr = %q", exitCode, stderr)
			}
		})
	}
}

func TestVerifyRejectsEscapedSignatureLineBreaks(t *testing.T) {
	directory := t.TempDir()
	privatePath := filepath.Join(directory, "key")
	publicPath := privatePath + ".pub"
	artifactPath := filepath.Join(directory, "artifact.exe")
	manifestPath := filepath.Join(directory, "latest.json")
	if err := os.WriteFile(artifactPath, []byte("artifact"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if _, stderr, exitCode := invoke("keygen", "-private", privatePath, "-public", publicPath); exitCode != 0 {
		t.Fatalf("keygen exit code = %d, stderr = %q", exitCode, stderr)
	}
	if _, stderr, exitCode := invoke("sign", "-private", privatePath, "-artifact", artifactPath, "-manifest", manifestPath, "-version", "1.0.0", "-published-at", "2026-07-27T00:00:00Z", "-minimum-os", "10.0.17763", "-url", "/artifact.exe"); exitCode != 0 {
		t.Fatalf("sign exit code = %d, stderr = %q", exitCode, stderr)
	}
	valid := string(mustReadFile(t, manifestPath))
	for name, escaped := range map[string]string{"carriage return": `\r`, "line feed": `\n`} {
		t.Run(name, func(t *testing.T) {
			contents := strings.Replace(valid, `"signature": "`, `"signature": "`+escaped, 1)
			path := filepath.Join(directory, strings.ReplaceAll(name, " ", "-")+".json")
			if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
				t.Fatalf("write manifest: %v", err)
			}
			_, stderr, exitCode := invoke("verify", "-public", publicPath, "-manifest", path, "-artifact", artifactPath)
			if exitCode == 0 || !strings.Contains(stderr, "signature") {
				t.Fatalf("verify exit code = %d, stderr = %q", exitCode, stderr)
			}
		})
	}
}

func TestSignAndVerifyBindAbsoluteArtifactURLToOrigin(t *testing.T) {
	directory := t.TempDir()
	privatePath := filepath.Join(directory, "key")
	publicPath := privatePath + ".pub"
	artifactPath := filepath.Join(directory, "artifact.exe")
	manifestPath := filepath.Join(directory, "latest.json")
	if err := os.WriteFile(artifactPath, []byte("artifact"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if _, stderr, exitCode := invoke("keygen", "-private", privatePath, "-public", publicPath); exitCode != 0 {
		t.Fatalf("keygen exit code = %d, stderr = %q", exitCode, stderr)
	}
	signArgs := []string{"sign", "-private", privatePath, "-artifact", artifactPath, "-manifest", manifestPath, "-version", "1.0.0", "-published-at", "2026-07-27T00:00:00Z", "-minimum-os", "10.0.17763", "-url", "https://it.example.com/downloads/artifact.exe"}
	if _, stderr, exitCode := invoke(signArgs...); exitCode == 0 || !strings.Contains(stderr, "origin") {
		t.Fatalf("sign without origin exit code = %d, stderr = %q", exitCode, stderr)
	}
	signArgs = append(signArgs, "-origin", "https://it.example.com/base")
	if _, stderr, exitCode := invoke(signArgs...); exitCode != 0 {
		t.Fatalf("same-origin sign exit code = %d, stderr = %q", exitCode, stderr)
	}
	if _, stderr, exitCode := invoke("verify", "-public", publicPath, "-manifest", manifestPath, "-artifact", artifactPath, "-origin", "https://it.example.com"); exitCode != 0 {
		t.Fatalf("same-origin verify exit code = %d, stderr = %q", exitCode, stderr)
	}
	if _, stderr, exitCode := invoke("verify", "-public", publicPath, "-manifest", manifestPath, "-artifact", artifactPath, "-origin", "https://other.example.com"); exitCode == 0 || !strings.Contains(stderr, "origin") {
		t.Fatalf("cross-origin verify exit code = %d, stderr = %q", exitCode, stderr)
	}
}

func TestCommandsRejectInvalidArgumentsWithUsageExitCode(t *testing.T) {
	for _, args := range [][]string{{}, {"unknown"}, {"sign", "-version", "v1.0.0"}, {"verify"}, {"keygen"}} {
		_, stderr, exitCode := invoke(args...)
		if exitCode != 2 || stderr == "" {
			t.Fatalf("run(%q) exit code = %d, stderr = %q", args, exitCode, stderr)
		}
	}
}

func TestSignRejectsNonCanonicalPublishedAt(t *testing.T) {
	directory := t.TempDir()
	privatePath := filepath.Join(directory, "key")
	publicPath := privatePath + ".pub"
	artifactPath := filepath.Join(directory, "artifact.exe")
	if err := os.WriteFile(artifactPath, []byte("artifact"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if _, stderr, exitCode := invoke("keygen", "-private", privatePath, "-public", publicPath); exitCode != 0 {
		t.Fatalf("keygen exit code = %d, stderr = %q", exitCode, stderr)
	}
	for _, publishedAt := range []string{"2026-07-27T08:00:00+08:00", "2026-07-27T00:00:00.000Z", time.Time{}.Format(time.RFC3339)} {
		_, _, exitCode := invoke("sign", "-private", privatePath, "-artifact", artifactPath, "-manifest", filepath.Join(directory, "latest.json"), "-version", "1.0.0", "-published-at", publishedAt, "-minimum-os", "10.0.17763", "-url", "/artifact.exe")
		if exitCode == 0 {
			t.Fatalf("sign accepted published_at %q", publishedAt)
		}
	}
}

func invoke(args ...string) (string, string, int) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), exitCode
}

func readEncodedKey(t *testing.T, path string, expectedLength int) []byte {
	t.Helper()
	encoded := strings.TrimSpace(string(mustReadFile(t, path)))
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode key: %v", err)
	}
	if len(decoded) != expectedLength {
		t.Fatalf("key length = %d, want %d", len(decoded), expectedLength)
	}
	return decoded
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	return contents
}
