package agent

import (
	"net"
	"os"
	"runtime"
	"testing"
	"time"
)

type testAddress string

func (address testAddress) Network() string { return "test" }
func (address testAddress) String() string  { return string(address) }

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

func TestHostCollectorSetsNetworkCapabilityAndSamplesConsecutively(t *testing.T) {
	fixedTime := beijingTestTime(2026, time.August, 3, 10, 0)
	collector := newHostCollector(newNetworkUsageSampler(
		sequenceReader([]networkReadResult{
			{counters: networkCounters{sent: 10_000_000, received: 20_000_000}},
			{counters: networkCounters{sent: 13_000_000, received: 28_000_000}},
		}),
		sequenceClock([]time.Time{fixedTime, fixedTime.Add(2 * time.Second)}),
		func() (NetworkUsageState, error) { return NetworkUsageState{}, os.ErrNotExist },
		func(NetworkUsageState) error { return nil },
		nil,
	))

	_, first, err := collector.Collect("0.1.0")
	if err != nil {
		t.Fatalf("first Collect() error = %v", err)
	}
	_, second, err := collector.Collect("0.1.0")
	if err != nil {
		t.Fatalf("second Collect() error = %v", err)
	}
	if !first.NetworkMetricsAvailable || !second.NetworkMetricsAvailable {
		t.Fatalf("network capability first=%t second=%t", first.NetworkMetricsAvailable, second.NetworkMetricsAvailable)
	}
	if first.NetworkUploadMBPerSecond != 0 || first.NetworkDownloadMBPerSecond != 0 || second.NetworkUploadMBPerSecond != 1.5 || second.NetworkDownloadMBPerSecond != 4 {
		t.Fatalf("network rates first=%v/%v second=%v/%v", first.NetworkUploadMBPerSecond, first.NetworkDownloadMBPerSecond, second.NetworkUploadMBPerSecond, second.NetworkDownloadMBPerSecond)
	}
	if !second.NetworkUsageAvailable || second.NetworkUsageDay != "2026-08-03" ||
		second.NetworkTodayUploadBytes != 3_000_000 || second.NetworkTodayDownloadBytes != 8_000_000 ||
		second.NetworkMonthUploadBytes != 3_000_000 || second.NetworkMonthDownloadBytes != 8_000_000 {
		t.Fatalf("network usage heartbeat = %#v", second)
	}
}

func TestPrimaryIPPrefersIPv4OverEarlierIPv6Address(t *testing.T) {
	t.Parallel()

	addresses := []net.Addr{
		testAddress("fd5d:892e:ce5f:44fd:59af:3d90:4ad4:9653/64"),
		testAddress("192.168.31.25/24"),
	}

	if got := primaryIPFromAddresses(addresses); got != "192.168.31.25" {
		t.Fatalf("primaryIPFromAddresses() = %q, want IPv4 address", got)
	}
}

func TestPrimaryIPFallsBackToIPv6WhenIPv4IsUnavailable(t *testing.T) {
	t.Parallel()

	addresses := []net.Addr{
		testAddress("127.0.0.1/8"),
		testAddress("fe80::1/64"),
		testAddress("fd5d:892e:ce5f:44fd:59af:3d90:4ad4:9653/64"),
	}

	if got := primaryIPFromAddresses(addresses); got != "fd5d:892e:ce5f:44fd:59af:3d90:4ad4:9653" {
		t.Fatalf("primaryIPFromAddresses() = %q, want IPv6 fallback", got)
	}
}
