// Package updaterapp implements the fixed Ace Agent Updater command protocol.
package updaterapp

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"aceitcenter.local/platform/agent/internal/update"
	"aceitcenter.local/platform/internal/release"
)

type Dependencies struct {
	UpdatePublicKey string
	Version         string
	RunHelper       func(context.Context, update.HelperOptions) error
}

func Run(ctx context.Context, args []string, stdout io.Writer, dependencies Dependencies) error {
	if ctx == nil {
		return errors.New("updater context is required")
	}
	if stdout == nil {
		return errors.New("updater stdout is required")
	}
	if len(args) == 0 {
		return errors.New("updater command is required")
	}
	switch args[0] {
	case "check":
		return runCheck(ctx, args[1:], stdout, dependencies)
	case "apply":
		return runApply(ctx, args[1:], dependencies)
	case "version":
		return runVersion(args[1:], stdout, dependencies.Version)
	default:
		return fmt.Errorf("unknown updater command %q", args[0])
	}
}

func runCheck(ctx context.Context, args []string, stdout io.Writer, dependencies Dependencies) error {
	flags := newFlagSet("check")
	var origin, currentVersion, currentOS, stagingDirectory string
	flags.StringVar(&origin, "origin", "", "public update origin")
	flags.StringVar(&currentVersion, "current-version", "", "installed Agent version")
	flags.StringVar(&currentOS, "current-os", "", "installed Windows version")
	flags.StringVar(&stagingDirectory, "staging", "", "absolute update staging directory")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse check arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return errors.New("check does not accept positional arguments")
	}
	if origin == "" || currentVersion == "" || currentOS == "" || stagingDirectory == "" {
		return errors.New("check requires origin, current-version, current-os, and staging")
	}
	if !filepath.IsAbs(stagingDirectory) {
		return errors.New("check staging path must be absolute")
	}
	checker := update.Checker{
		Origin:         origin,
		CurrentVersion: currentVersion,
		CurrentOS:      currentOS,
		PublicKey:      dependencies.UpdatePublicKey,
		StagingDir:     stagingDirectory,
	}
	candidate, err := checker.Check(ctx)
	if errors.Is(err, update.ErrNoUpdateAvailable) {
		return update.EncodeCheckResult(stdout, update.CheckResult{})
	}
	if err != nil {
		return fmt.Errorf("check release: %w", err)
	}
	staged, err := checker.Stage(ctx, candidate)
	if err != nil {
		return fmt.Errorf("stage release: %w", err)
	}
	return update.EncodeCheckResult(stdout, update.CheckResult{
		Available:     true,
		Version:       staged.Version,
		URL:           candidate.InstallerURL,
		InstallerPath: staged.InstallerPath,
	})
}

func runApply(ctx context.Context, args []string, dependencies Dependencies) error {
	flags := newFlagSet("apply")
	var installerPath, agentPath, backupPath, version string
	flags.StringVar(&installerPath, "installer", "", "verified installer path")
	flags.StringVar(&agentPath, "agent", "", "installed Agent path")
	flags.StringVar(&backupPath, "backup", "", "last-known-good Agent path")
	flags.StringVar(&version, "version", "", "candidate version")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse apply arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return errors.New("apply does not accept positional arguments")
	}
	if installerPath == "" || agentPath == "" || backupPath == "" || version == "" {
		return errors.New("apply requires installer, agent, backup, and version")
	}
	for _, path := range []string{installerPath, agentPath, backupPath} {
		if !filepath.IsAbs(path) {
			return errors.New("apply paths must be absolute")
		}
	}
	if _, err := release.CompareVersions(version, version); err != nil {
		return errors.New("apply version must use valid semantic versioning")
	}
	runner := dependencies.RunHelper
	if runner == nil {
		runner = update.RunHelper
	}
	return runner(ctx, update.HelperOptions{
		InstallerPath:  installerPath,
		ExecutablePath: agentPath,
		BackupPath:     backupPath,
		StagingDir:     filepath.Dir(backupPath),
		Version:        version,
	})
}

func runVersion(args []string, stdout io.Writer, version string) error {
	if len(args) != 0 {
		return errors.New("version does not accept arguments")
	}
	if version == "" {
		return errors.New("updater version is unavailable")
	}
	if _, err := fmt.Fprintln(stdout, version); err != nil {
		return fmt.Errorf("write updater version: %w", err)
	}
	return nil
}

func newFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}
