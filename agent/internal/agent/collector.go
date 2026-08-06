package agent

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"aceitcenter.local/platform/internal/core"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
)

type HostCollector struct {
	networkSampler *NetworkUsageSampler
}

func Collect(version string) (core.EnrollRequest, core.Heartbeat, error) {
	return NewHostCollector().Collect(version)
}

func NewHostCollector() *HostCollector {
	return NewHostCollectorWithNetworkUsage("", nil)
}

func NewHostCollectorWithNetworkUsage(path string, report func(error)) *HostCollector {
	return newHostCollector(NewNetworkUsageSampler(path, report))
}

func newHostCollector(networkSampler *NetworkUsageSampler) *HostCollector {
	return &HostCollector{networkSampler: networkSampler}
}

func (c *HostCollector) Collect(version string) (core.EnrollRequest, core.Heartbeat, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return core.EnrollRequest{}, core.Heartbeat{}, fmt.Errorf("read hostname: %w", err)
	}
	hostInfo, err := host.Info()
	if err != nil {
		return core.EnrollRequest{}, core.Heartbeat{}, fmt.Errorf("read host info: %w", err)
	}
	memory, err := mem.VirtualMemory()
	if err != nil {
		return core.EnrollRequest{}, core.Heartbeat{}, fmt.Errorf("read memory usage: %w", err)
	}
	percentages, err := cpu.Percent(200*time.Millisecond, false)
	if err != nil || len(percentages) == 0 {
		return core.EnrollRequest{}, core.Heartbeat{}, fmt.Errorf("read CPU usage: %w", err)
	}
	root := "/"
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	diskUsage, err := disk.Usage(root)
	if err != nil {
		return core.EnrollRequest{}, core.Heartbeat{}, fmt.Errorf("read disk usage: %w", err)
	}
	nodeType := "linux"
	if runtime.GOOS == "windows" {
		nodeType = "windows"
	}
	network := c.networkSampler.Sample()
	heartbeat := core.Heartbeat{
		Hostname:                   hostname,
		AgentVersion:               version,
		OSName:                     hostInfo.Platform,
		OSVersion:                  hostInfo.PlatformVersion,
		IPAddress:                  primaryIP(),
		CPUPercent:                 clampPercent(percentages[0]),
		MemoryPercent:              clampPercent(memory.UsedPercent),
		DiskPercent:                clampPercent(diskUsage.UsedPercent),
		NetworkMetricsAvailable:    network.MetricsAvailable,
		NetworkUploadMBPerSecond:   network.UploadMBPerSecond,
		NetworkDownloadMBPerSecond: network.DownloadMBPerSecond,
		NetworkUsageAvailable:      network.UsageAvailable,
		NetworkUsageDay:            network.UsageDay,
		NetworkTodayUploadBytes:    network.TodayUploadBytes,
		NetworkTodayDownloadBytes:  network.TodayDownloadBytes,
		NetworkMonthUploadBytes:    network.MonthUploadBytes,
		NetworkMonthDownloadBytes:  network.MonthDownloadBytes,
	}
	request := core.EnrollRequest{
		Hostname:  hostname,
		Type:      nodeType,
		Version:   version,
		MachineID: hostInfo.HostID,
	}
	if request.MachineID == "" {
		request.MachineID = hostname
	}
	return request, heartbeat, nil
}

func primaryIP() string {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	return primaryIPFromAddresses(addresses)
}

func primaryIPFromAddresses(addresses []net.Addr) string {
	var ipv6Fallback string
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err != nil || ip.IsLoopback() || !ip.IsGlobalUnicast() {
			continue
		}
		if ip.To4() != nil {
			return ip.String()
		}
		if ipv6Fallback == "" {
			ipv6Fallback = ip.String()
		}
	}
	return ipv6Fallback
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
