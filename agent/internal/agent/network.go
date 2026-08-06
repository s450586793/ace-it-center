package agent

import (
	"fmt"
	"time"

	"github.com/shirou/gopsutil/v4/net"
)

type networkCounters struct {
	sent     uint64
	received uint64
}

// NetworkRateSampler calculates aggregate host network rates between samples.
type NetworkRateSampler struct {
	read        func() (networkCounters, error)
	now         func() time.Time
	initialized bool
	previous    networkCounters
	sampledAt   time.Time
}

func NewNetworkRateSampler() *NetworkRateSampler {
	return newNetworkRateSampler(readNetworkCounters, time.Now)
}

func newNetworkRateSampler(read func() (networkCounters, error), now func() time.Time) *NetworkRateSampler {
	return &NetworkRateSampler{read: read, now: now}
}

func (s *NetworkRateSampler) Sample() (upload, download float64) {
	current, err := s.read()
	sampledAt := s.now()
	if err != nil {
		return 0, 0
	}
	if !s.initialized || !sampledAt.After(s.sampledAt) || current.sent < s.previous.sent || current.received < s.previous.received {
		s.previous, s.sampledAt, s.initialized = current, sampledAt, true
		return 0, 0
	}

	seconds := sampledAt.Sub(s.sampledAt).Seconds()
	upload = float64(current.sent-s.previous.sent) / seconds / 1_000_000
	download = float64(current.received-s.previous.received) / seconds / 1_000_000
	s.previous, s.sampledAt = current, sampledAt
	return upload, download
}

func readNetworkCounters() (networkCounters, error) {
	counters, err := net.IOCounters(false)
	if err != nil {
		return networkCounters{}, fmt.Errorf("read network counters: %w", err)
	}
	if len(counters) == 0 {
		return networkCounters{}, fmt.Errorf("read network counters: no aggregate counters")
	}
	return networkCounters{sent: counters[0].BytesSent, received: counters[0].BytesRecv}, nil
}
