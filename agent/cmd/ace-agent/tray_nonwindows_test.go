//go:build !windows

package main

import (
	"strings"
	"testing"
)

func TestRunTrayAcceptsShowOption(t *testing.T) {
	err := runTray([]string{"--show"})
	if err == nil {
		t.Fatal("runTray unexpectedly succeeded on a non-Windows host")
	}
	if strings.Contains(err.Error(), "does not accept arguments") {
		t.Fatalf("runTray rejected the --show option: %v", err)
	}
	if !strings.Contains(err.Error(), "only supported on Windows") {
		t.Fatalf("runTray error = %v, want native platform error", err)
	}
}
