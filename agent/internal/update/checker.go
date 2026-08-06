package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	stableManifestPath = "/downloads/windows/stable/latest.json"
	defaultHTTPTimeout = 30 * time.Second
)

// Candidate 是已通过认证且比当前 Agent 更新的发布版本。
type Candidate struct {
	Manifest     Manifest
	InstallerURL string
}

// StagedUpdate 是已通过精确大小与 SHA-256 校验的安装器。
type StagedUpdate struct {
	Version       string
	InstallerPath string
	Manifest      Manifest
}

// Checker 获取并认证发布元数据，然后暂存对应安装器。
// PublicKey 必须是包含单个 Ed25519 公钥的 canonical standard base64。
type Checker struct {
	Origin         string
	CurrentVersion string
	CurrentOS      string
	PublicKey      string
	StagingDir     string
	Timeout        time.Duration
	Transport      http.RoundTripper
	rename         func(string, string) error
}

func (c Checker) Check(ctx context.Context) (Candidate, error) {
	if ctx == nil {
		return Candidate{}, errors.New("update check context is required")
	}
	origin, err := parseCheckerOrigin(c.Origin)
	if err != nil {
		return Candidate{}, err
	}
	publicKey, err := parsePublicKey(c.PublicKey)
	if err != nil {
		return Candidate{}, err
	}
	manifestURL := *origin
	manifestURL.Path = stableManifestPath
	manifestURL.RawPath = ""
	manifestURL.RawQuery = ""
	manifestURL.Fragment = ""
	contents, err := c.fetchBounded(ctx, origin, manifestURL.String(), MaxManifestBytes)
	if err != nil {
		return Candidate{}, fmt.Errorf("fetch update manifest: %w", err)
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Candidate{}, fmt.Errorf("decode update manifest: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Candidate{}, fmt.Errorf("decode update manifest: %w", err)
	}
	if err := Verify(manifest, publicKey); err != nil {
		return Candidate{}, fmt.Errorf("authenticate update manifest: %w", err)
	}
	if err := ValidateCandidate(manifest, c.CurrentVersion, c.CurrentOS, c.Origin); err != nil {
		return Candidate{}, fmt.Errorf("validate update candidate: %w", err)
	}
	installerURL, err := resolveInstallerURL(origin, manifest.URL)
	if err != nil {
		return Candidate{}, err
	}
	return Candidate{Manifest: manifest, InstallerURL: installerURL}, nil
}

func (c Checker) Stage(ctx context.Context, candidate Candidate) (staged StagedUpdate, resultErr error) {
	if ctx == nil {
		return StagedUpdate{}, errors.New("update staging context is required")
	}
	origin, err := parseCheckerOrigin(c.Origin)
	if err != nil {
		return StagedUpdate{}, err
	}
	publicKey, err := parsePublicKey(c.PublicKey)
	if err != nil {
		return StagedUpdate{}, err
	}
	if err := Verify(candidate.Manifest, publicKey); err != nil {
		return StagedUpdate{}, fmt.Errorf("authenticate staged manifest: %w", err)
	}
	if err := ValidateCandidate(candidate.Manifest, c.CurrentVersion, c.CurrentOS, c.Origin); err != nil {
		return StagedUpdate{}, fmt.Errorf("validate staged candidate: %w", err)
	}
	if candidate.Manifest.Size <= 0 || candidate.Manifest.Size > MaxArtifactBytes {
		return StagedUpdate{}, fmt.Errorf("installer size is outside the allowed range")
	}
	installerURL, err := resolveInstallerURL(origin, candidate.Manifest.URL)
	if err != nil {
		return StagedUpdate{}, err
	}
	if candidate.InstallerURL != installerURL {
		return StagedUpdate{}, errors.New("candidate installer URL does not match its manifest")
	}
	if c.StagingDir == "" {
		return StagedUpdate{}, errors.New("update staging directory is required")
	}
	if err := os.MkdirAll(c.StagingDir, 0o700); err != nil {
		return StagedUpdate{}, fmt.Errorf("create update staging directory: %w", err)
	}
	if err := secureStagingDirectory(c.StagingDir); err != nil {
		return StagedUpdate{}, fmt.Errorf("secure update staging directory: %w", err)
	}

	filename := fmt.Sprintf("AceAgentSetup-windows-amd64-V%s.exe", candidate.Manifest.Version)
	finalPath := filepath.Join(c.StagingDir, filename)
	partialPath := finalPath + ".partial"
	if err := removeIfPresent(partialPath); err != nil {
		return StagedUpdate{}, fmt.Errorf("remove previous partial installer: %w", err)
	}
	defer func() {
		if resultErr != nil {
			_ = os.Remove(partialPath)
		}
	}()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, installerURL, nil)
	if err != nil {
		return StagedUpdate{}, fmt.Errorf("create installer request: %w", err)
	}
	response, err := c.httpClient(origin).Do(request)
	if err != nil {
		return StagedUpdate{}, fmt.Errorf("download installer: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return StagedUpdate{}, fmt.Errorf("download installer: unexpected HTTP status %d", response.StatusCode)
	}
	if response.ContentLength >= 0 && response.ContentLength != candidate.Manifest.Size {
		return StagedUpdate{}, fmt.Errorf("installer size = %d, want %d", response.ContentLength, candidate.Manifest.Size)
	}

	partial, err := os.OpenFile(partialPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return StagedUpdate{}, fmt.Errorf("create partial installer: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = partial.Close()
		}
	}()
	if err := secureStagingFile(partialPath); err != nil {
		return StagedUpdate{}, fmt.Errorf("secure partial installer: %w", err)
	}
	hash := sha256.New()
	count, err := io.Copy(io.MultiWriter(partial, hash), io.LimitReader(response.Body, candidate.Manifest.Size+1))
	if err != nil {
		return StagedUpdate{}, fmt.Errorf("write partial installer: %w", err)
	}
	if count != candidate.Manifest.Size {
		return StagedUpdate{}, fmt.Errorf("installer size = %d, want %d", count, candidate.Manifest.Size)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != candidate.Manifest.SHA256 {
		return StagedUpdate{}, errors.New("installer SHA-256 does not match manifest")
	}
	if err := partial.Sync(); err != nil {
		return StagedUpdate{}, fmt.Errorf("sync partial installer: %w", err)
	}
	if err := partial.Close(); err != nil {
		return StagedUpdate{}, fmt.Errorf("close partial installer: %w", err)
	}
	closed = true
	rename := c.rename
	if rename == nil {
		rename = replaceStagedFile
	}
	if err := rename(partialPath, finalPath); err != nil {
		return StagedUpdate{}, fmt.Errorf("publish staged installer: %w", err)
	}
	if err := syncStagingDirectory(c.StagingDir); err != nil {
		return StagedUpdate{}, fmt.Errorf("sync update staging directory: %w", err)
	}
	return StagedUpdate{Version: candidate.Manifest.Version, InstallerPath: finalPath, Manifest: candidate.Manifest}, nil
}

func (c Checker) fetchBounded(ctx context.Context, origin *url.URL, target string, maximum int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	response, err := c.httpClient(origin).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	if response.ContentLength > maximum {
		return nil, fmt.Errorf("response exceeds %d bytes", maximum)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > maximum {
		return nil, fmt.Errorf("response exceeds %d bytes", maximum)
	}
	return contents, nil
}

func (c Checker) httpClient(origin *url.URL) *http.Client {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	transport := c.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: rejectCrossOriginRedirects(origin)}
}

func rejectCrossOriginRedirects(origin *url.URL) func(*http.Request, []*http.Request) error {
	want := checkerOriginIdentity(origin)
	return func(request *http.Request, _ []*http.Request) error {
		if request == nil || request.URL == nil || request.URL.User != nil || checkerOriginIdentity(request.URL) != want {
			return errors.New("update redirect changed origin")
		}
		return nil
	}
}

func parseCheckerOrigin(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, errors.New("update origin must be an absolute HTTP or HTTPS URL without credentials, query, or fragment")
	}
	return parsed, nil
}

func parsePublicKey(encoded string) (ed25519.PublicKey, error) {
	if encoded == "" || strings.ContainsAny(encoded, " \t\r\n") {
		return nil, errors.New("embedded update public key is unavailable")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(decoded) != encoded {
		return nil, errors.New("embedded update public key is invalid")
	}
	return ed25519.PublicKey(decoded), nil
}

func resolveInstallerURL(origin *url.URL, value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("parse installer URL: %w", err)
	}
	resolved := origin.ResolveReference(parsed)
	if resolved.User != nil || checkerOriginIdentity(resolved) != checkerOriginIdentity(origin) {
		return "", errors.New("installer URL changed update origin")
	}
	return resolved.String(), nil
}

func checkerOriginIdentity(value *url.URL) string {
	if value == nil || (value.Scheme != "http" && value.Scheme != "https") || value.Host == "" {
		return ""
	}
	port := value.Port()
	if port == "" {
		if value.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return strings.ToLower(value.Scheme) + "://" + net.JoinHostPort(strings.ToLower(value.Hostname()), port)
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("manifest contains multiple JSON values")
		}
		return err
	}
	return nil
}

func removeIfPresent(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
