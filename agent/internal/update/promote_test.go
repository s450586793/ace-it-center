package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakePromotionOperations struct {
	versionOutput string
	versionErr    error
	replaceErrors []error
	replaceErr    error
	replaceCalls  int
	retryableErr  error
}

func (operations *fakePromotionOperations) Lstat(path string) (os.FileInfo, error) {
	return os.Lstat(path)
}

func (operations *fakePromotionOperations) RunVersion(context.Context, string, int) (string, error) {
	return operations.versionOutput, operations.versionErr
}

func (operations *fakePromotionOperations) Replace(_, _ string) error {
	operations.replaceCalls++
	if len(operations.replaceErrors) == 0 {
		return operations.replaceErr
	}
	err := operations.replaceErrors[0]
	operations.replaceErrors = operations.replaceErrors[1:]
	return err
}

func (operations *fakePromotionOperations) IsRetryable(err error) bool {
	return operations.retryableErr != nil && errors.Is(err, operations.retryableErr)
}

func TestPromotePendingUpdaterRetriesSharingViolationThenReplacesFixedFile(t *testing.T) {
	directory := t.TempDir()
	pending := filepath.Join(directory, "AceAgentUpdater.next.exe")
	if err := os.WriteFile(pending, []byte("updater"), 0o700); err != nil {
		t.Fatal(err)
	}
	sharingViolation := errors.New("sharing violation")
	operations := &fakePromotionOperations{
		versionOutput: "0.4.11\n",
		replaceErrors: []error{sharingViolation, nil},
		retryableErr:  sharingViolation,
	}

	err := PromotePendingUpdater(context.Background(), PromotionOptions{
		AgentVersion:  "0.4.11",
		InstalledPath: filepath.Join(directory, "AceAgentUpdater.exe"),
		PendingPath:   pending,
		RetryInterval: time.Millisecond,
		Timeout:       50 * time.Millisecond,
		Operations:    operations,
	})

	if err != nil || operations.replaceCalls != 2 {
		t.Fatalf("promotion = %v, calls=%d", err, operations.replaceCalls)
	}
}

func TestPromotePendingUpdaterTreatsMissingPendingFileAsSuccess(t *testing.T) {
	directory := t.TempDir()
	operations := &fakePromotionOperations{}
	err := PromotePendingUpdater(context.Background(), PromotionOptions{
		AgentVersion:  "0.4.11",
		InstalledPath: filepath.Join(directory, "AceAgentUpdater.exe"),
		PendingPath:   filepath.Join(directory, "AceAgentUpdater.next.exe"),
		Operations:    operations,
	})
	if err != nil || operations.replaceCalls != 0 {
		t.Fatalf("promotion = %v, calls=%d", err, operations.replaceCalls)
	}
}

func TestPromotePendingUpdaterRejectsUnsafePendingFileAndVersionOutput(t *testing.T) {
	tests := []struct {
		name          string
		create        func(*testing.T, string)
		versionOutput string
	}{
		{name: "directory", create: func(t *testing.T, path string) { t.Helper(); mustMkdir(t, path) }, versionOutput: "0.4.11\n"},
		{name: "symlink", create: func(t *testing.T, path string) { t.Helper(); mustSymlink(t, path) }, versionOutput: "0.4.11\n"},
		{name: "different version", create: func(t *testing.T, path string) { t.Helper(); mustWrite(t, path) }, versionOutput: "0.4.10\n"},
		{name: "multiple lines", create: func(t *testing.T, path string) { t.Helper(); mustWrite(t, path) }, versionOutput: "0.4.11\nsecret\n"},
		{name: "oversized output", create: func(t *testing.T, path string) { t.Helper(); mustWrite(t, path) }, versionOutput: strings.Repeat("x", MaxUpdaterVersionBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			pending := filepath.Join(directory, "AceAgentUpdater.next.exe")
			test.create(t, pending)
			operations := &fakePromotionOperations{versionOutput: test.versionOutput}
			err := PromotePendingUpdater(context.Background(), PromotionOptions{
				AgentVersion:  "0.4.11",
				InstalledPath: filepath.Join(directory, "AceAgentUpdater.exe"),
				PendingPath:   pending,
				Operations:    operations,
			})
			if err == nil || operations.replaceCalls != 0 {
				t.Fatalf("promotion = %v, calls=%d", err, operations.replaceCalls)
			}
		})
	}
}

func TestPromotePendingUpdaterTimesOutWithoutDeletingEitherUpdater(t *testing.T) {
	directory := t.TempDir()
	fixed := filepath.Join(directory, "AceAgentUpdater.exe")
	pending := filepath.Join(directory, "AceAgentUpdater.next.exe")
	mustWrite(t, fixed)
	mustWrite(t, pending)
	sharingViolation := errors.New("sharing violation")
	operations := &fakePromotionOperations{versionOutput: "0.4.11\n", replaceErr: sharingViolation, retryableErr: sharingViolation}

	err := PromotePendingUpdater(context.Background(), PromotionOptions{
		AgentVersion:  "0.4.11",
		InstalledPath: fixed,
		PendingPath:   pending,
		RetryInterval: time.Millisecond,
		Timeout:       3 * time.Millisecond,
		Operations:    operations,
	})

	if err == nil {
		t.Fatal("promotion unexpectedly succeeded")
	}
	for _, path := range []string{fixed, pending} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("%s was removed: %v", filepath.Base(path), statErr)
		}
	}
}

func TestPromotePendingUpdaterCleansOnlyOldExactLegacyHelpers(t *testing.T) {
	directory := t.TempDir()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	old := now.Add(-25 * time.Hour)
	removable := filepath.Join(directory, ".AceAgent-update-helper-100.exe")
	current := filepath.Join(directory, ".AceAgent-update-helper-200.exe")
	newFile := filepath.Join(directory, ".AceAgent-update-helper-300.exe")
	similar := filepath.Join(directory, "AceAgent-update-helper-400.exe")
	wrongSuffix := filepath.Join(directory, ".AceAgent-update-helper-500.exe.bak")
	target := filepath.Join(directory, "target.exe")
	symlink := filepath.Join(directory, ".AceAgent-update-helper-600.exe")
	for _, path := range []string{removable, current, newFile, similar, wrongSuffix, target} {
		mustWrite(t, path)
	}
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{removable, current, similar, wrongSuffix, target} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(newFile, now, now); err != nil {
		t.Fatal(err)
	}

	installDirectory := t.TempDir()
	err := PromotePendingUpdater(context.Background(), PromotionOptions{
		AgentVersion:       "0.4.11",
		InstalledPath:      filepath.Join(installDirectory, "AceAgentUpdater.exe"),
		PendingPath:        filepath.Join(installDirectory, "AceAgentUpdater.next.exe"),
		StagingDirectory:   directory,
		CurrentProcessPath: current,
		Now:                func() time.Time { return now },
		Operations:         &fakePromotionOperations{},
	})
	if err != nil {
		t.Fatalf("promotion maintenance = %v", err)
	}
	if _, err := os.Stat(removable); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old legacy helper remains: %v", err)
	}
	for _, path := range []string{current, newFile, similar, wrongSuffix, target} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("safe file %s was removed: %v", filepath.Base(path), err)
		}
	}
	if info, err := os.Lstat(symlink); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("legacy-looking symlink changed: %v, %v", info, err)
	}
}

func TestPromotePendingUpdaterRejectsInvalidOptions(t *testing.T) {
	directory := t.TempDir()
	valid := PromotionOptions{
		AgentVersion:  "0.4.11",
		InstalledPath: filepath.Join(directory, "AceAgentUpdater.exe"),
		PendingPath:   filepath.Join(directory, "AceAgentUpdater.next.exe"),
		Operations:    &fakePromotionOperations{},
	}
	if err := PromotePendingUpdater(nil, valid); err == nil {
		t.Fatal("promotion accepted nil context")
	}
	tests := []PromotionOptions{
		{AgentVersion: "invalid", InstalledPath: valid.InstalledPath, PendingPath: valid.PendingPath},
		{AgentVersion: "0.4.11", InstalledPath: "relative", PendingPath: valid.PendingPath},
		{AgentVersion: "0.4.11", InstalledPath: filepath.Join(directory, "wrong.exe"), PendingPath: valid.PendingPath},
		{AgentVersion: "0.4.11", InstalledPath: valid.InstalledPath, PendingPath: filepath.Join(t.TempDir(), "AceAgentUpdater.next.exe")},
		{AgentVersion: "0.4.11", InstalledPath: valid.InstalledPath, PendingPath: valid.PendingPath, StagingDirectory: "relative"},
	}
	for _, options := range tests {
		options.Operations = &fakePromotionOperations{}
		if err := PromotePendingUpdater(context.Background(), options); err == nil {
			t.Fatalf("promotion accepted invalid options: %#v", options)
		}
	}
}

func TestPromotePendingUpdaterPropagatesVersionAndPermanentReplaceErrors(t *testing.T) {
	directory := t.TempDir()
	pending := filepath.Join(directory, "AceAgentUpdater.next.exe")
	mustWrite(t, pending)
	versionErr := errors.New("version process failed")
	options := PromotionOptions{
		AgentVersion:  "0.4.11",
		InstalledPath: filepath.Join(directory, "AceAgentUpdater.exe"),
		PendingPath:   pending,
		Operations:    &fakePromotionOperations{versionErr: versionErr},
	}
	if err := PromotePendingUpdater(context.Background(), options); !errors.Is(err, versionErr) {
		t.Fatalf("promotion version error = %v", err)
	}
	replaceErr := errors.New("replace denied")
	options.Operations = &fakePromotionOperations{versionOutput: "0.4.11\n", replaceErr: replaceErr}
	if err := PromotePendingUpdater(context.Background(), options); !errors.Is(err, replaceErr) {
		t.Fatalf("promotion replace error = %v", err)
	}
}

func mustWrite(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("file"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, path string) {
	t.Helper()
	target := filepath.Join(filepath.Dir(path), "target.exe")
	mustWrite(t, target)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}
