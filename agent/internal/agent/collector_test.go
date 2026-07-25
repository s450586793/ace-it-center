package agent

import (
	"runtime"
	"testing"
)

func TestCollectReturnsPlatformIdentityAndBoundedMetrics(t *testing.T) {
	request, heartbeat, err := Collect("0.1.0")
	if err != nil {
		t.Fatalf("Collect returned error: %v", err)
	}
	if request.Hostname == "" || request.MachineID == "" || request.Version != "0.1.0" {
		t.Fatalf("enrollment identity = %#v", request)
	}
	wantType := runtime.GOOS
	if wantType != "windows" {
		wantType = "linux"
	}
	if request.Type != wantType {
		t.Fatalf("node type = %q, want %q", request.Type, wantType)
	}
	for name, value := range map[string]float64{
		"cpu":    heartbeat.CPUPercent,
		"memory": heartbeat.MemoryPercent,
		"disk":   heartbeat.DiskPercent,
	} {
		if value < 0 || value > 100 {
			t.Fatalf("%s percent = %v, want 0..100", name, value)
		}
	}
}
