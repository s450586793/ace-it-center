package systemupdate

import (
	"context"
	"errors"
	"time"

	"golang.org/x/mod/semver"
)

// RunningImageReader reads the image currently used by a managed service.
type RunningImageReader interface {
	InspectService(ctx context.Context, service ServiceName) (Image, error)
}

// ConfigurationError reports inconsistent local or published image metadata.
type ConfigurationError struct {
	Message string
}

func (err *ConfigurationError) Error() string {
	return err.Message
}

// Checker compares the currently running backend and Web images with stable.
type Checker struct {
	resolver    ImageResolver
	runtime     RunningImageReader
	backendRepo string
	webRepo     string
	now         func() time.Time
}

// NewChecker constructs a checker for the fixed backend and Web repositories.
func NewChecker(resolver ImageResolver, runtime RunningImageReader, backendRepo, webRepo string, now func() time.Time) *Checker {
	if now == nil {
		now = time.Now
	}
	return &Checker{resolver: resolver, runtime: runtime, backendRepo: backendRepo, webRepo: webRepo, now: now}
}

// Check reports whether a matching stable image pair is newer than the running pair.
func (checker *Checker) Check(ctx context.Context) (CheckResult, error) {
	if ctx == nil {
		return CheckResult{}, configurationError("update check context is required")
	}
	if checker == nil || checker.resolver == nil || checker.runtime == nil || checker.backendRepo == "" || checker.webRepo == "" {
		return CheckResult{}, configurationError("update checker is not configured")
	}
	currentBackend, err := checker.runtime.InspectService(ctx, ServiceBackend)
	if err != nil {
		return CheckResult{}, errors.New("inspect backend service")
	}
	currentWeb, err := checker.runtime.InspectService(ctx, ServiceWeb)
	if err != nil {
		return CheckResult{}, errors.New("inspect web service")
	}
	current := ImagePair{Backend: currentBackend, Web: currentWeb}
	if err := checker.validatePair(current, "current"); err != nil {
		return CheckResult{}, err
	}

	targetBackend, err := checker.resolver.Resolve(ctx, checker.backendRepo, stableTag)
	if err != nil {
		return CheckResult{}, &RegistryError{}
	}
	targetWeb, err := checker.resolver.Resolve(ctx, checker.webRepo, stableTag)
	if err != nil {
		return CheckResult{}, &RegistryError{}
	}
	target := ImagePair{Backend: targetBackend, Web: targetWeb}
	if err := checker.validatePair(target, "stable"); err != nil {
		return CheckResult{}, err
	}

	comparison := semver.Compare(target.Backend.Version, current.Backend.Version)
	if comparison < 0 {
		return CheckResult{}, configurationError("stable version must not be older than current version")
	}
	return CheckResult{
		Current:   current,
		Target:    target,
		Available: comparison > 0,
		CheckedAt: checker.now().UTC(),
	}, nil
}

func (checker *Checker) validatePair(pair ImagePair, kind string) error {
	if err := checker.validateImage(pair.Backend, checker.backendRepo, kind+" backend"); err != nil {
		return err
	}
	if err := checker.validateImage(pair.Web, checker.webRepo, kind+" web"); err != nil {
		return err
	}
	if pair.Backend.Version != pair.Web.Version {
		return configurationError(kind + " image versions must match")
	}
	return nil
}

func (checker *Checker) validateImage(image Image, repository, label string) error {
	if image.Repository != repository {
		return configurationError(label + " repository is unexpected")
	}
	if image.Digest == "" {
		return configurationError(label + " digest is required")
	}
	if err := ValidateVersion(image.Version); err != nil {
		return configurationError(label + " version is invalid")
	}
	return nil
}

func configurationError(message string) error {
	return &ConfigurationError{Message: message}
}
