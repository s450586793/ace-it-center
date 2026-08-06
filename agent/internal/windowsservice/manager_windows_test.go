//go:build windows

package windowsservice

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestToWindowsConfigUsesOwnProcessServiceType(t *testing.T) {
	configuration := toWindowsConfig(serviceConfiguration(`C:\Program Files\Ace IT Center\AceAgent.exe`))

	if configuration.ServiceType != windows.SERVICE_WIN32_OWN_PROCESS {
		t.Fatalf("service type = %#x, want SERVICE_WIN32_OWN_PROCESS", configuration.ServiceType)
	}
}
