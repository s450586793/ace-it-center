// Package diagnostics creates local, sanitized Agent support bundles.
package diagnostics

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"

	"aceitcenter.local/platform/agent/internal/agent"
	"aceitcenter.local/platform/agent/internal/app"
	"aceitcenter.local/platform/agent/internal/buildinfo"
)

const defaultMaxLogBytes int64 = 1 << 20

type Options struct {
	OutputDir       string
	Config          agent.Config
	Status          app.StatusSnapshot
	EnrollmentToken string
	LogPath         string
	MaxLogBytes     int64
}

func Create(ctx context.Context, options Options) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if options.OutputDir == "" {
		return "", fmt.Errorf("diagnostic output directory is required")
	}
	if err := os.MkdirAll(options.OutputDir, 0o700); err != nil {
		return "", fmt.Errorf("create diagnostic directory: %w", err)
	}
	file, err := os.CreateTemp(options.OutputDir, "agent-diagnostics-*.zip")
	if err != nil {
		return "", fmt.Errorf("create diagnostic bundle: %w", err)
	}
	path := file.Name()
	completed := false
	defer func() {
		if !completed {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("secure diagnostic bundle: %w", err)
	}

	archive := zip.NewWriter(file)
	archiveClosed := false
	defer func() {
		if !archiveClosed {
			_ = archive.Close()
		}
	}()
	secrets := []string{options.Config.Credential, pendingPairingCredential(options.Config), options.EnrollmentToken}
	status := options.Status
	status.Error = redactText(status.Error, secrets...)
	build := map[string]string{
		"version":  buildinfo.Version,
		"commit":   buildinfo.Commit,
		"built_at": buildinfo.BuiltAt,
	}
	hostname, hostnameErr := os.Hostname()
	if hostnameErr != nil {
		hostname = "unknown"
	}
	system := map[string]string{
		"go_version": runtime.Version(),
		"goos":       runtime.GOOS,
		"goarch":     runtime.GOARCH,
		"hostname":   hostname,
	}
	if err := writeJSON(archive, "build.json", build); err != nil {
		return "", err
	}
	if err := writeJSON(archive, "config.json", options.Config.Sanitized()); err != nil {
		return "", err
	}
	if err := writeJSON(archive, "status.json", status); err != nil {
		return "", err
	}
	if err := writeJSON(archive, "system.json", system); err != nil {
		return "", err
	}
	logs, err := ReadLogTail(options.LogPath, options.MaxLogBytes, secrets)
	if err != nil {
		return "", err
	}
	if err := writeFile(archive, "logs/agent.log", logs); err != nil {
		return "", err
	}
	if err := archive.Close(); err != nil {
		return "", fmt.Errorf("close diagnostic archive: %w", err)
	}
	archiveClosed = true
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close diagnostic bundle: %w", err)
	}
	completed = true
	return path, nil
}

func pendingPairingCredential(config agent.Config) string {
	if config.PendingPairing == nil {
		return ""
	}
	return config.PendingPairing.Credential
}

func writeJSON(archive *zip.Writer, name string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	return writeFile(archive, name, encoded)
}

func writeFile(archive *zip.Writer, name string, contents []byte) error {
	writer, err := archive.Create(name)
	if err != nil {
		return fmt.Errorf("create diagnostic entry %s: %w", name, err)
	}
	if _, err := writer.Write(contents); err != nil {
		return fmt.Errorf("write diagnostic entry %s: %w", name, err)
	}
	return nil
}

// ReadLogTail returns a bounded, redacted tail of an Agent log file.
func ReadLogTail(path string, maximum int64, secrets []string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	if maximum == 0 {
		maximum = defaultMaxLogBytes
	}
	if maximum < 0 {
		return nil, fmt.Errorf("maximum log bytes must not be negative")
	}
	overlap := maxSecretBytes(secrets) - 1
	if overlap < 0 {
		overlap = 0
	}
	if maximum > int64(^uint(0)>>1)-int64(overlap) {
		return nil, fmt.Errorf("maximum log bytes is too large")
	}
	readMaximum := maximum + int64(overlap)
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open agent log: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat agent log: %w", err)
	}
	if info.Size() > readMaximum {
		if _, err := file.Seek(info.Size()-readMaximum, io.SeekStart); err != nil {
			return nil, fmt.Errorf("seek agent log: %w", err)
		}
	}
	contents, err := io.ReadAll(io.LimitReader(file, readMaximum))
	if err != nil {
		return nil, fmt.Errorf("read agent log: %w", err)
	}
	contents = trimLeadingSecretFragment(contents, secrets)
	redacted := []byte(redactText(string(contents), secrets...))
	if int64(len(redacted)) <= maximum {
		return redacted, nil
	}
	return redacted[len(redacted)-int(maximum):], nil
}

func maxSecretBytes(secrets []string) int {
	maximum := 0
	for _, secret := range secrets {
		if len(secret) > maximum {
			maximum = len(secret)
		}
	}
	return maximum
}

func trimLeadingSecretFragment(contents []byte, secrets []string) []byte {
	longestFragment := 0
	for _, secret := range secrets {
		for length := 1; length < len(secret); length++ {
			if length > longestFragment && bytes.HasPrefix(contents, []byte(secret[len(secret)-length:])) {
				longestFragment = length
			}
		}
	}
	return contents[longestFragment:]
}

func redactText(value string, secrets ...string) string {
	for _, secret := range normalizeSecrets(secrets) {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return value
}

func normalizeSecrets(secrets []string) []string {
	unique := make(map[string]struct{}, len(secrets))
	for _, secret := range secrets {
		if secret != "" {
			unique[secret] = struct{}{}
		}
	}
	normalized := make([]string, 0, len(unique))
	for secret := range unique {
		normalized = append(normalized, secret)
	}
	sort.Slice(normalized, func(left, right int) bool {
		if len(normalized[left]) != len(normalized[right]) {
			return len(normalized[left]) > len(normalized[right])
		}
		return normalized[left] < normalized[right]
	})
	return normalized
}
