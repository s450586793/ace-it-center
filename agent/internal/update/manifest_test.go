package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestManifestCanonicalPayloadIsDeterministicAndExcludesSignature(t *testing.T) {
	manifest := validManifest()
	manifest.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))

	got, err := CanonicalPayload(manifest)
	if err != nil {
		t.Fatalf("CanonicalPayload returned error: %v", err)
	}
	want := `{"schema":1,"channel":"stable","version":"0.2.0","published_at":"2026-07-27T00:00:00Z","minimum_os":"10.0.17763","url":"/downloads/windows/stable/AceAgentSetup-windows-amd64-V0.2.0.exe","size":12582912,"sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`
	if string(got) != want {
		t.Fatalf("CanonicalPayload = %s, want %s", got, want)
	}
	if strings.Contains(string(got), "signature") {
		t.Fatalf("canonical payload contains signature: %s", got)
	}
}

func TestManifestSignatureCoversAllReleaseFields(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey returned error: %v", err)
	}
	signed, err := Sign(validManifest(), privateKey)
	if err != nil {
		t.Fatalf("Sign returned error: %v", err)
	}
	if err := Verify(signed, publicKey); err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}

	mutations := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "schema", mutate: func(value *Manifest) { value.Schema++ }},
		{name: "channel", mutate: func(value *Manifest) { value.Channel = "beta" }},
		{name: "version", mutate: func(value *Manifest) { value.Version = "0.2.1" }},
		{name: "published at", mutate: func(value *Manifest) { value.PublishedAt = value.PublishedAt.Add(time.Second) }},
		{name: "minimum OS", mutate: func(value *Manifest) { value.MinimumOS = "10.0.19045" }},
		{name: "URL", mutate: func(value *Manifest) { value.URL += ".different" }},
		{name: "size", mutate: func(value *Manifest) { value.Size++ }},
		{name: "SHA-256", mutate: func(value *Manifest) { value.SHA256 = strings.Repeat("f", 64) }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			tampered := signed
			mutation.mutate(&tampered)
			if err := Verify(tampered, publicKey); err == nil {
				t.Fatal("tampered manifest verified")
			}
		})
	}
}

func TestManifestRejectsInvalidReleaseFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "schema", mutate: func(value *Manifest) { value.Schema = 2 }},
		{name: "channel", mutate: func(value *Manifest) { value.Channel = "beta" }},
		{name: "version with v prefix", mutate: func(value *Manifest) { value.Version = "v0.2.0" }},
		{name: "incomplete semver", mutate: func(value *Manifest) { value.Version = "0.2" }},
		{name: "semver leading zero", mutate: func(value *Manifest) { value.Version = "0.02.0" }},
		{name: "zero published at", mutate: func(value *Manifest) { value.PublishedAt = time.Time{} }},
		{name: "non UTC published at", mutate: func(value *Manifest) {
			value.PublishedAt = time.Date(2026, 7, 27, 8, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
		}},
		{name: "fractional published at", mutate: func(value *Manifest) { value.PublishedAt = value.PublishedAt.Add(time.Nanosecond) }},
		{name: "minimum OS missing build", mutate: func(value *Manifest) { value.MinimumOS = "10.0" }},
		{name: "minimum OS leading zero", mutate: func(value *Manifest) { value.MinimumOS = "10.0.017763" }},
		{name: "bare relative URL", mutate: func(value *Manifest) { value.URL = "downloads/agent.exe" }},
		{name: "scheme relative URL", mutate: func(value *Manifest) { value.URL = "//evil.example/agent.exe" }},
		{name: "encoded scheme relative URL", mutate: func(value *Manifest) { value.URL = "/%2f%2fevil.example/agent.exe" }},
		{name: "URL userinfo", mutate: func(value *Manifest) { value.URL = "https://user@it.example.com/agent.exe" }},
		{name: "URL query", mutate: func(value *Manifest) { value.URL += "?token=secret" }},
		{name: "URL fragment", mutate: func(value *Manifest) { value.URL += "#fragment" }},
		{name: "URL backslash", mutate: func(value *Manifest) { value.URL = `/downloads\\agent.exe` }},
		{name: "zero size", mutate: func(value *Manifest) { value.Size = 0 }},
		{name: "oversized artifact", mutate: func(value *Manifest) { value.Size = MaxArtifactBytes + 1 }},
		{name: "short SHA-256", mutate: func(value *Manifest) { value.SHA256 = "0123" }},
		{name: "uppercase SHA-256", mutate: func(value *Manifest) { value.SHA256 = strings.Repeat("A", 64) }},
		{name: "non hex SHA-256", mutate: func(value *Manifest) { value.SHA256 = strings.Repeat("z", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManifest()
			test.mutate(&manifest)
			if _, err := CanonicalPayload(manifest); err == nil {
				t.Fatal("CanonicalPayload accepted invalid manifest")
			}
		})
	}
}

func TestManifestVerifyRejectsMalformedSignatureAndKeys(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey returned error: %v", err)
	}
	signed, err := Sign(validManifest(), privateKey)
	if err != nil {
		t.Fatalf("Sign returned error: %v", err)
	}

	for _, signature := range []string{
		"not-base64",
		base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize-1)),
		strings.TrimRight(signed.Signature, "="),
		" " + signed.Signature,
		signed.Signature + "\t",
		signed.Signature[:12] + "\r" + signed.Signature[12:],
		signed.Signature[:12] + "\n" + signed.Signature[12:],
	} {
		invalid := signed
		invalid.Signature = signature
		if err := Verify(invalid, publicKey); err == nil {
			t.Fatalf("Verify accepted signature %q", signature)
		}
	}
	if err := Verify(signed, publicKey[:len(publicKey)-1]); err == nil {
		t.Fatal("Verify accepted a short public key")
	}
	if _, err := Sign(validManifest(), privateKey[:len(privateKey)-1]); err == nil {
		t.Fatal("Sign accepted a short private key")
	}
	corruptPrivate := append(ed25519.PrivateKey(nil), privateKey...)
	corruptPrivate[len(corruptPrivate)-1] ^= 1
	if _, err := Sign(validManifest(), corruptPrivate); err == nil {
		t.Fatal("Sign accepted a private key with a corrupt public suffix")
	}
}

func TestManifestValidateCandidateRequiresNewerCompatibleSameOriginRelease(t *testing.T) {
	manifest := validManifest()
	if err := ValidateCandidate(manifest, "0.1.9", "10.0.19045", "https://it.example.com"); err != nil {
		t.Fatalf("ValidateCandidate returned error: %v", err)
	}
	manifest.URL = "https://it.example.com/downloads/AceAgentSetup.exe"
	if err := ValidateCandidate(manifest, "0.1.9", "10.0.19045", "https://it.example.com/base"); err != nil {
		t.Fatalf("ValidateCandidate rejected same-origin absolute URL: %v", err)
	}
}

func TestManifestValidateCandidateRejectsUnsafeCandidate(t *testing.T) {
	tests := []struct {
		name           string
		currentVersion string
		currentOS      string
		origin         string
		mutate         func(*Manifest)
	}{
		{name: "equal version", currentVersion: "0.2.0", currentOS: "10.0.19045", origin: "https://it.example.com"},
		{name: "build metadata equal version", currentVersion: "0.2.0+installed", currentOS: "10.0.19045", origin: "https://it.example.com", mutate: func(value *Manifest) { value.Version = "0.2.0+release" }},
		{name: "downgrade", currentVersion: "0.3.0", currentOS: "10.0.19045", origin: "https://it.example.com"},
		{name: "invalid current version", currentVersion: "development", currentOS: "10.0.19045", origin: "https://it.example.com"},
		{name: "OS below floor", currentVersion: "0.1.0", currentOS: "10.0.14393", origin: "https://it.example.com"},
		{name: "invalid current OS", currentVersion: "0.1.0", currentOS: "Windows 11", origin: "https://it.example.com"},
		{name: "cross origin host", currentVersion: "0.1.0", currentOS: "10.0.19045", origin: "https://it.example.com", mutate: func(value *Manifest) { value.URL = "https://evil.example/agent.exe" }},
		{name: "cross origin scheme", currentVersion: "0.1.0", currentOS: "10.0.19045", origin: "https://it.example.com", mutate: func(value *Manifest) { value.URL = "http://it.example.com/agent.exe" }},
		{name: "invalid origin", currentVersion: "0.1.0", currentOS: "10.0.19045", origin: "it.example.com"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManifest()
			if test.mutate != nil {
				test.mutate(&manifest)
			}
			if err := ValidateCandidate(manifest, test.currentVersion, test.currentOS, test.origin); err == nil {
				t.Fatal("ValidateCandidate accepted unsafe candidate")
			}
		})
	}
}

func validManifest() Manifest {
	return Manifest{
		Schema:      1,
		Channel:     "stable",
		Version:     "0.2.0",
		PublishedAt: time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC),
		MinimumOS:   "10.0.17763",
		URL:         "/downloads/windows/stable/AceAgentSetup-windows-amd64-V0.2.0.exe",
		Size:        12582912,
		SHA256:      "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
}
