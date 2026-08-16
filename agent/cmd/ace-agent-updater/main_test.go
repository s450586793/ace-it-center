package main

import (
	"bytes"
	"context"
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

func TestRunUpdaterReturnsNonzeroWithoutWritingErrorToStdout(t *testing.T) {
	var output bytes.Buffer
	exitCode := runUpdater(context.Background(), []string{"unknown"}, &output, updaterapp.Dependencies{})
	if exitCode == 0 || output.Len() != 0 {
		t.Fatalf("runUpdater() = %d, output %q", exitCode, output.String())
	}
}
