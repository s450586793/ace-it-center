package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"aceitcenter.local/platform/agent/internal/updaterapp"
)

func TestRunUpdaterReturnsSuccessForVersion(t *testing.T) {
	var output bytes.Buffer
	exitCode := runUpdater(context.Background(), []string{"version"}, &output, updaterapp.Dependencies{Version: "0.4.11"})
	if exitCode != 0 || output.String() != "0.4.11\n" {
		t.Fatalf("runUpdater() = %d, output %q", exitCode, output.String())
	}
}

func TestLogUpdaterResultRecordsSafeCommandOutcomeOnly(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	logUpdaterResult(logger, []string{"apply", "--credential", "secret-value"}, 0)
	logUpdaterResult(logger, []string{"unknown", "secret-value"}, 1)

	logged := output.String()
	if !strings.Contains(logged, "updater command completed") || !strings.Contains(logged, `"command":"apply"`) || !strings.Contains(logged, "updater command failed") || !strings.Contains(logged, `"command":"unknown"`) {
		t.Fatalf("updater log = %q", logged)
	}
	if strings.Contains(logged, "secret-value") || strings.Contains(logged, "credential") {
		t.Fatalf("updater log leaked arguments: %q", logged)
	}
}

func TestRunUpdaterReturnsNonzeroWithoutWritingErrorToStdout(t *testing.T) {
	var output bytes.Buffer
	exitCode := runUpdater(context.Background(), []string{"unknown"}, &output, updaterapp.Dependencies{})
	if exitCode == 0 || output.Len() != 0 {
		t.Fatalf("runUpdater() = %d, output %q", exitCode, output.String())
	}
}
