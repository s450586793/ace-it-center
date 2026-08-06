// Package release validates and authenticates Ace Agent releases.
package release

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const (
	ManifestSchema   = 1
	ManifestChannel  = "stable"
	MaxManifestBytes = 64 << 10
	MaxArtifactBytes = 256 << 20
)

var strictSemverPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

type Manifest struct {
	Schema      int       `json:"schema"`
	Channel     string    `json:"channel"`
	Version     string    `json:"version"`
	PublishedAt time.Time `json:"published_at"`
	MinimumOS   string    `json:"minimum_os"`
	URL         string    `json:"url"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	Signature   string    `json:"signature"`
}

type canonicalManifest struct {
	Schema      int       `json:"schema"`
	Channel     string    `json:"channel"`
	Version     string    `json:"version"`
	PublishedAt time.Time `json:"published_at"`
	MinimumOS   string    `json:"minimum_os"`
	URL         string    `json:"url"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
}

func CanonicalPayload(manifest Manifest) ([]byte, error) {
	if err := validateManifest(manifest); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(canonicalManifest{
		Schema:      manifest.Schema,
		Channel:     manifest.Channel,
		Version:     manifest.Version,
		PublishedAt: manifest.PublishedAt,
		MinimumOS:   manifest.MinimumOS,
		URL:         manifest.URL,
		Size:        manifest.Size,
		SHA256:      manifest.SHA256,
	})
	if err != nil {
		return nil, fmt.Errorf("encode canonical manifest: %w", err)
	}
	return payload, nil
}

func Sign(manifest Manifest, privateKey ed25519.PrivateKey) (Manifest, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return Manifest{}, errors.New("invalid Ed25519 private key length")
	}
	derived := ed25519.NewKeyFromSeed(privateKey[:ed25519.SeedSize])
	if subtle.ConstantTimeCompare(privateKey, derived) != 1 {
		return Manifest{}, errors.New("invalid Ed25519 private key material")
	}
	payload, err := CanonicalPayload(manifest)
	if err != nil {
		return Manifest{}, err
	}
	manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(derived, payload))
	return manifest, nil
}

func Verify(manifest Manifest, publicKey ed25519.PublicKey) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("invalid Ed25519 public key length")
	}
	payload, err := CanonicalPayload(manifest)
	if err != nil {
		return err
	}
	if strings.ContainsAny(manifest.Signature, " \t\r\n") {
		return errors.New("invalid Ed25519 signature encoding")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(manifest.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(signature) != manifest.Signature {
		return errors.New("invalid Ed25519 signature encoding")
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return errors.New("manifest signature verification failed")
	}
	return nil
}

func ValidateCandidate(manifest Manifest, currentVersion, currentOS, origin string) error {
	if err := validateManifest(manifest); err != nil {
		return err
	}
	if !validSemver(currentVersion) {
		return errors.New("current version is not valid semantic versioning")
	}
	if semver.Compare("v"+manifest.Version, "v"+currentVersion) <= 0 {
		return errors.New("candidate version must be newer than the installed version")
	}
	minimum, err := parseWindowsVersion(manifest.MinimumOS)
	if err != nil {
		return err
	}
	installed, err := parseWindowsVersion(currentOS)
	if err != nil {
		return errors.New("current Windows version is invalid")
	}
	if compareWindowsVersions(installed, minimum) < 0 {
		return errors.New("candidate requires a newer Windows version")
	}
	base, err := parseOrigin(origin)
	if err != nil {
		return err
	}
	if err := validateArtifactURL(manifest.URL, base); err != nil {
		return err
	}
	return nil
}

// ValidateManifestOrigin verifies the manifest fields and binds its artifact URL
// to the configured server origin.
func ValidateManifestOrigin(manifest Manifest, origin string) error {
	if err := validateManifest(manifest); err != nil {
		return err
	}
	base, err := parseOrigin(origin)
	if err != nil {
		return err
	}
	return validateArtifactURL(manifest.URL, base)
}

func validateManifest(manifest Manifest) error {
	if manifest.Schema != ManifestSchema {
		return errors.New("manifest schema must be 1")
	}
	if manifest.Channel != ManifestChannel {
		return errors.New("manifest channel must be stable")
	}
	if !validSemver(manifest.Version) {
		return errors.New("manifest version is not valid semantic versioning")
	}
	if manifest.PublishedAt.IsZero() || manifest.PublishedAt.Location() != time.UTC || manifest.PublishedAt.Nanosecond() != 0 {
		return errors.New("manifest published_at must be a whole-second UTC timestamp")
	}
	if _, err := parseWindowsVersion(manifest.MinimumOS); err != nil {
		return err
	}
	if err := validateArtifactURL(manifest.URL, nil); err != nil {
		return err
	}
	if manifest.Size <= 0 || manifest.Size > MaxArtifactBytes {
		return fmt.Errorf("manifest size must be between 1 and %d bytes", MaxArtifactBytes)
	}
	if len(manifest.SHA256) != 64 || strings.ToLower(manifest.SHA256) != manifest.SHA256 {
		return errors.New("manifest sha256 must be 64 lowercase hexadecimal characters")
	}
	decodedHash, err := hex.DecodeString(manifest.SHA256)
	if err != nil || len(decodedHash) != 32 {
		return errors.New("manifest sha256 must be 64 lowercase hexadecimal characters")
	}
	return nil
}

func validSemver(value string) bool {
	return strictSemverPattern.MatchString(value) && semver.IsValid("v"+value)
}

type windowsVersion [4]uint32

func parseWindowsVersion(value string) (windowsVersion, error) {
	parts := strings.Split(value, ".")
	if len(parts) < 3 || len(parts) > 4 {
		return windowsVersion{}, errors.New("minimum_os must contain three or four numeric components")
	}
	var version windowsVersion
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return windowsVersion{}, errors.New("minimum_os contains an invalid numeric component")
		}
		component, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return windowsVersion{}, errors.New("minimum_os contains an invalid numeric component")
		}
		version[index] = uint32(component)
	}
	return version, nil
}

func compareWindowsVersions(left, right windowsVersion) int {
	for index := range left {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	return 0
}

func validateArtifactURL(value string, origin *url.URL) error {
	if value == "" || strings.Contains(value, `\`) || strings.HasPrefix(value, "//") {
		return errors.New("manifest URL must be same-origin absolute or origin-relative")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return errors.New("manifest URL must not contain userinfo, query, or fragment")
	}
	if parsed.IsAbs() {
		if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path == "" {
			return errors.New("manifest URL must use HTTP or HTTPS and include a path")
		}
		if origin != nil && originIdentity(parsed) != originIdentity(origin) {
			return errors.New("manifest URL must use the configured server origin")
		}
		return nil
	}
	if parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") || strings.Contains(parsed.Path, `\`) || parsed.Path == "/" {
		return errors.New("manifest URL must be same-origin absolute or origin-relative")
	}
	return nil
}

func parseOrigin(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, errors.New("server origin must be an absolute HTTP or HTTPS URL")
	}
	return parsed, nil
}

func originIdentity(value *url.URL) string {
	host := strings.ToLower(value.Hostname())
	port := value.Port()
	if port == "" {
		if value.Scheme == "https" {
			port = "443"
		} else if value.Scheme == "http" {
			port = "80"
		}
	}
	return strings.ToLower(value.Scheme) + "://" + net.JoinHostPort(host, port)
}
