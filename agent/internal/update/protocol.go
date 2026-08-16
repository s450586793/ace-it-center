package update

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"regexp"

	"aceitcenter.local/platform/internal/release"
)

const MaxCheckResultBytes = 64 << 10

var windowsAbsolutePathPattern = regexp.MustCompile(`^[A-Za-z]:[\\/].+`)

type CheckResult struct {
	Available     bool   `json:"available"`
	Version       string `json:"version,omitempty"`
	URL           string `json:"url,omitempty"`
	InstallerPath string `json:"installer_path,omitempty"`
}

func (r CheckResult) Validate() error {
	if !r.Available {
		if r.Version != "" || r.URL != "" || r.InstallerPath != "" {
			return errors.New("unavailable update result must not contain release fields")
		}
		return nil
	}
	if _, err := release.CompareVersions(r.Version, r.Version); err != nil {
		return errors.New("available update result must contain a valid semantic version")
	}
	parsed, err := url.Parse(r.URL)
	if err != nil || !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path == "" || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return errors.New("available update result must contain a public HTTP or HTTPS URL")
	}
	if !filepath.IsAbs(r.InstallerPath) && !windowsAbsolutePathPattern.MatchString(r.InstallerPath) {
		return errors.New("available update result must contain an absolute installer path")
	}
	return nil
}

func EncodeCheckResult(writer io.Writer, result CheckResult) error {
	if writer == nil {
		return errors.New("check result writer is required")
	}
	if err := result.Validate(); err != nil {
		return err
	}
	var output bytes.Buffer
	if err := json.NewEncoder(&output).Encode(result); err != nil {
		return fmt.Errorf("encode check result: %w", err)
	}
	if output.Len() > MaxCheckResultBytes {
		return fmt.Errorf("check result exceeds %d bytes", MaxCheckResultBytes)
	}
	if _, err := writer.Write(output.Bytes()); err != nil {
		return fmt.Errorf("write check result: %w", err)
	}
	return nil
}

func DecodeCheckResult(reader io.Reader) (CheckResult, error) {
	if reader == nil {
		return CheckResult{}, errors.New("check result reader is required")
	}
	contents, err := io.ReadAll(io.LimitReader(reader, MaxCheckResultBytes+1))
	if err != nil {
		return CheckResult{}, fmt.Errorf("read check result: %w", err)
	}
	if len(contents) > MaxCheckResultBytes {
		return CheckResult{}, fmt.Errorf("check result exceeds %d bytes", MaxCheckResultBytes)
	}
	var result CheckResult
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return CheckResult{}, fmt.Errorf("decode check result: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return CheckResult{}, errors.New("decode check result: multiple JSON values")
		}
		return CheckResult{}, fmt.Errorf("decode check result: %w", err)
	}
	if err := result.Validate(); err != nil {
		return CheckResult{}, err
	}
	return result, nil
}
