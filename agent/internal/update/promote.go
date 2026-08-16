package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aceitcenter.local/platform/internal/release"
)

const (
	MaxUpdaterVersionBytes   = 1024
	defaultPromotionTimeout  = 90 * time.Second
	defaultPromotionInterval = 500 * time.Millisecond
	legacyHelperMinimumAge   = 24 * time.Hour
	legacyHelperPrefix       = ".AceAgent-update-helper-"
	legacyHelperSuffix       = ".exe"
)

type PromotionOperations interface {
	Lstat(string) (os.FileInfo, error)
	RunVersion(context.Context, string, int) (string, error)
	Replace(source, destination string) error
	IsRetryable(error) bool
}

type PromotionOptions struct {
	AgentVersion       string
	InstalledPath      string
	PendingPath        string
	StagingDirectory   string
	CurrentProcessPath string
	RetryInterval      time.Duration
	Timeout            time.Duration
	Now                func() time.Time
	Operations         PromotionOperations
}

func PromotePendingUpdater(ctx context.Context, options PromotionOptions) error {
	if ctx == nil {
		return errors.New("updater promotion context is required")
	}
	if err := validatePromotionOptions(options); err != nil {
		return err
	}
	operations := options.Operations
	if operations == nil {
		operations = defaultPromotionOperations()
		if operations == nil {
			return errors.New("updater promotion is unavailable on this platform")
		}
	}
	info, err := operations.Lstat(options.PendingPath)
	if errors.Is(err, os.ErrNotExist) {
		return cleanupLegacyHelpers(options)
	}
	if err != nil {
		return fmt.Errorf("inspect pending updater: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("pending updater must be a regular file")
	}
	versionOutput, err := operations.RunVersion(ctx, options.PendingPath, MaxUpdaterVersionBytes)
	if err != nil {
		return fmt.Errorf("read pending updater version: %w", err)
	}
	if len(versionOutput) > MaxUpdaterVersionBytes || !exactUpdaterVersion(versionOutput, options.AgentVersion) {
		return errors.New("pending updater version does not match the Agent version")
	}

	timeout := options.Timeout
	if timeout <= 0 {
		timeout = defaultPromotionTimeout
	}
	interval := options.RetryInterval
	if interval <= 0 {
		interval = defaultPromotionInterval
	}
	promotionContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		err := operations.Replace(options.PendingPath, options.InstalledPath)
		if err == nil {
			return cleanupLegacyHelpers(options)
		}
		if !operations.IsRetryable(err) {
			return fmt.Errorf("replace fixed updater: %w", err)
		}
		timer := time.NewTimer(interval)
		select {
		case <-promotionContext.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return errors.Join(fmt.Errorf("replace fixed updater: %w", err), promotionContext.Err())
		case <-timer.C:
		}
	}
}

func validatePromotionOptions(options PromotionOptions) error {
	if _, err := release.CompareVersions(options.AgentVersion, options.AgentVersion); err != nil {
		return errors.New("promotion Agent version must use valid semantic versioning")
	}
	if !isAbsoluteUpdatePath(options.InstalledPath) || !isAbsoluteUpdatePath(options.PendingPath) {
		return errors.New("updater promotion paths must be absolute")
	}
	if !strings.EqualFold(filepath.Base(options.InstalledPath), "AceAgentUpdater.exe") || !strings.EqualFold(filepath.Base(options.PendingPath), "AceAgentUpdater.next.exe") {
		return errors.New("updater promotion paths must use fixed filenames")
	}
	if !strings.EqualFold(filepath.Clean(filepath.Dir(options.InstalledPath)), filepath.Clean(filepath.Dir(options.PendingPath))) {
		return errors.New("fixed and pending updaters must share an installation directory")
	}
	if options.StagingDirectory != "" && !isAbsoluteUpdatePath(options.StagingDirectory) {
		return errors.New("legacy helper staging directory must be absolute")
	}
	return nil
}

func exactUpdaterVersion(output, version string) bool {
	return output == version || output == version+"\n" || output == version+"\r\n"
}

func cleanupLegacyHelpers(options PromotionOptions) error {
	if options.StagingDirectory == "" {
		return nil
	}
	entries, err := os.ReadDir(options.StagingDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list legacy update helpers: %w", err)
	}
	now := time.Now()
	if options.Now != nil {
		now = options.Now()
	}
	current := filepath.Clean(options.CurrentProcessPath)
	var cleanupErrors []error
	for _, entry := range entries {
		name := entry.Name()
		if !legacyHelperName(name) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("inspect legacy helper %s: %w", name, err))
			continue
		}
		if !info.Mode().IsRegular() || now.Sub(info.ModTime()) <= legacyHelperMinimumAge {
			continue
		}
		path := filepath.Join(options.StagingDirectory, name)
		if current != "." && strings.EqualFold(filepath.Clean(path), current) {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove legacy helper %s: %w", name, err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func legacyHelperName(name string) bool {
	lower := strings.ToLower(name)
	prefix := strings.ToLower(legacyHelperPrefix)
	return strings.HasPrefix(lower, prefix) && strings.HasSuffix(lower, legacyHelperSuffix) && len(name) > len(legacyHelperPrefix)+len(legacyHelperSuffix)
}
