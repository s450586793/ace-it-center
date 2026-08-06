//go:build !windows

package command

import "testing"

func TestNewPlatformRunnerIsDisabledOutsideWindows(t *testing.T) {
	t.Parallel()

	runner, supported := NewPlatformRunner()
	if supported || runner != nil {
		t.Fatalf("NewPlatformRunner = (%T, %v), want (nil, false)", runner, supported)
	}
}
