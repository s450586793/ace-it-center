package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"aceitcenter.local/platform/agent/internal/agentpaths"
	"aceitcenter.local/platform/internal/release"
)

type ProcessRunner interface {
	Run(context.Context, string, []string, int) ([]byte, error)
	StartDetached(context.Context, string, []string, DetachedLaunchOptions) error
}

type ProcessClient struct {
	AgentPath string
	Runner    ProcessRunner
}

type CheckOptions struct {
	Origin         string
	CurrentVersion string
	CurrentOS      string
	StagingDir     string
}

type ApplyOptions struct {
	InstallerPath string
	BackupPath    string
	Version       string
}

func (client ProcessClient) Check(ctx context.Context, options CheckOptions) (CheckResult, error) {
	if ctx == nil {
		return CheckResult{}, errors.New("updater check context is required")
	}
	if err := validateProcessClient(client); err != nil {
		return CheckResult{}, err
	}
	if options.Origin == "" || options.CurrentVersion == "" || options.CurrentOS == "" || options.StagingDir == "" {
		return CheckResult{}, errors.New("updater check requires origin, current version, current OS, and staging directory")
	}
	if _, err := release.CompareVersions(options.CurrentVersion, options.CurrentVersion); err != nil {
		return CheckResult{}, errors.New("updater check current version must use valid semantic versioning")
	}
	if !isAbsoluteUpdatePath(options.StagingDir) {
		return CheckResult{}, errors.New("updater check staging path must be absolute")
	}
	runner, err := client.processRunner()
	if err != nil {
		return CheckResult{}, err
	}
	arguments := []string{
		"check",
		"--origin", options.Origin,
		"--current-version", options.CurrentVersion,
		"--current-os", options.CurrentOS,
		"--staging", options.StagingDir,
	}
	output, err := runner.Run(ctx, agentpaths.UpdaterPath(client.AgentPath), arguments, MaxCheckResultBytes)
	if err != nil {
		return CheckResult{}, fmt.Errorf("run fixed updater check: %w", err)
	}
	result, err := DecodeCheckResult(bytes.NewReader(output))
	if err != nil {
		return CheckResult{}, fmt.Errorf("decode fixed updater check: %w", err)
	}
	return result, nil
}

func (client ProcessClient) LaunchApply(ctx context.Context, options ApplyOptions) error {
	if ctx == nil {
		return errors.New("updater apply context is required")
	}
	if err := validateProcessClient(client); err != nil {
		return err
	}
	if options.InstallerPath == "" || options.BackupPath == "" || options.Version == "" {
		return errors.New("updater apply requires installer, backup, and version")
	}
	if !isAbsoluteUpdatePath(options.InstallerPath) || !isAbsoluteUpdatePath(options.BackupPath) {
		return errors.New("updater apply paths must be absolute")
	}
	if _, err := release.CompareVersions(options.Version, options.Version); err != nil {
		return errors.New("updater apply version must use valid semantic versioning")
	}
	runner, err := client.processRunner()
	if err != nil {
		return err
	}
	arguments := []string{
		"apply",
		"--installer", options.InstallerPath,
		"--agent", client.AgentPath,
		"--backup", options.BackupPath,
		"--version", options.Version,
	}
	launch := DetachedLaunchOptions{NewProcessGroup: true, Detached: true, BreakawayFromJob: true}
	if err := runner.StartDetached(ctx, agentpaths.UpdaterPath(client.AgentPath), arguments, launch); err != nil {
		return fmt.Errorf("start fixed updater apply: %w", err)
	}
	return nil
}

func validateProcessClient(client ProcessClient) error {
	if client.AgentPath == "" || !isAbsoluteUpdatePath(client.AgentPath) {
		return errors.New("installed Agent path must be absolute")
	}
	return nil
}

func (client ProcessClient) processRunner() (ProcessRunner, error) {
	if client.Runner != nil {
		return client.Runner, nil
	}
	runner := defaultProcessRunner()
	if runner == nil {
		return nil, errors.New("fixed updater process is unavailable on this platform")
	}
	return runner, nil
}

func isAbsoluteUpdatePath(path string) bool {
	return filepath.IsAbs(path) || windowsAbsolutePathPattern.MatchString(path)
}

type boundedProcessBuffer struct {
	bytes.Buffer
	maximum  int
	exceeded bool
}

func (buffer *boundedProcessBuffer) Write(contents []byte) (int, error) {
	originalLength := len(contents)
	remaining := buffer.maximum - buffer.Len()
	if remaining < len(contents) {
		buffer.exceeded = true
		if remaining < 0 {
			remaining = 0
		}
		contents = contents[:remaining]
	}
	_, _ = buffer.Buffer.Write(contents)
	return originalLength, nil
}
