package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const networkUsageStateSchema = 1

var beijingLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

// NetworkUsageState is the Agent-local durable network usage checkpoint.
type NetworkUsageState struct {
	Schema                int    `json:"schema"`
	Day                   string `json:"day"`
	Month                 string `json:"month"`
	TodayUploadBytes      uint64 `json:"today_upload_bytes"`
	TodayDownloadBytes    uint64 `json:"today_download_bytes"`
	MonthUploadBytes      uint64 `json:"month_upload_bytes"`
	MonthDownloadBytes    uint64 `json:"month_download_bytes"`
	PreviousSentBytes     uint64 `json:"previous_sent_bytes"`
	PreviousReceivedBytes uint64 `json:"previous_received_bytes"`
	Initialized           bool   `json:"initialized"`
}

// NetworkSnapshot contains current rates and Agent-local period totals.
type NetworkSnapshot struct {
	MetricsAvailable    bool
	UploadMBPerSecond   float64
	DownloadMBPerSecond float64
	UsageAvailable      bool
	UsageDay            string
	TodayUploadBytes    uint64
	TodayDownloadBytes  uint64
	MonthUploadBytes    uint64
	MonthDownloadBytes  uint64
}

// NetworkUsageSampler calculates current rates and persists day/month totals.
type NetworkUsageSampler struct {
	mu sync.Mutex

	read   func() (networkCounters, error)
	now    func() time.Time
	load   func() (NetworkUsageState, error)
	save   func(NetworkUsageState) error
	report func(error)

	loaded        bool
	state         NetworkUsageState
	rateReady     bool
	ratePrevious  networkCounters
	rateSampledAt time.Time
}

func NewNetworkUsageSampler(path string, report func(error)) *NetworkUsageSampler {
	load := func() (NetworkUsageState, error) {
		if path == "" {
			return NetworkUsageState{}, os.ErrNotExist
		}
		return LoadNetworkUsageState(path)
	}
	save := func(state NetworkUsageState) error {
		if path == "" {
			return nil
		}
		return SaveNetworkUsageState(path, state)
	}
	return newNetworkUsageSampler(readNetworkCounters, time.Now, load, save, report)
}

func newNetworkUsageSampler(
	read func() (networkCounters, error),
	now func() time.Time,
	load func() (NetworkUsageState, error),
	save func(NetworkUsageState) error,
	report func(error),
) *NetworkUsageSampler {
	return &NetworkUsageSampler{read: read, now: now, load: load, save: save, report: report}
}

func (s *NetworkUsageSampler) Sample() NetworkSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	current, err := s.read()
	sampledAt := s.now()
	if err != nil {
		s.reportError(fmt.Errorf("sample network usage: %w", err))
		return NetworkSnapshot{}
	}

	s.loadState()
	day, month := usagePeriod(sampledAt)
	s.rollPeriod(day, month)

	if s.state.Initialized && current.sent >= s.state.PreviousSentBytes && current.received >= s.state.PreviousReceivedBytes {
		s.state.TodayUploadBytes += current.sent - s.state.PreviousSentBytes
		s.state.TodayDownloadBytes += current.received - s.state.PreviousReceivedBytes
		s.state.MonthUploadBytes += current.sent - s.state.PreviousSentBytes
		s.state.MonthDownloadBytes += current.received - s.state.PreviousReceivedBytes
	}
	s.state.Schema = networkUsageStateSchema
	s.state.Day = day
	s.state.Month = month
	s.state.PreviousSentBytes = current.sent
	s.state.PreviousReceivedBytes = current.received
	s.state.Initialized = true

	upload, download := s.sampleRate(current, sampledAt)
	if err := s.save(s.state); err != nil {
		s.reportError(fmt.Errorf("save network usage state: %w", err))
	}

	return NetworkSnapshot{
		MetricsAvailable:    true,
		UploadMBPerSecond:   upload,
		DownloadMBPerSecond: download,
		UsageAvailable:      true,
		UsageDay:            s.state.Day,
		TodayUploadBytes:    s.state.TodayUploadBytes,
		TodayDownloadBytes:  s.state.TodayDownloadBytes,
		MonthUploadBytes:    s.state.MonthUploadBytes,
		MonthDownloadBytes:  s.state.MonthDownloadBytes,
	}
}

func (s *NetworkUsageSampler) loadState() {
	if s.loaded {
		return
	}
	s.loaded = true
	state, err := s.load()
	if err == nil {
		s.state = state
		return
	}
	if !errors.Is(err, os.ErrNotExist) {
		s.reportError(fmt.Errorf("load network usage state: %w", err))
	}
	s.state = NetworkUsageState{}
}

func (s *NetworkUsageSampler) rollPeriod(day, month string) {
	if s.state.Month != month {
		s.state.MonthUploadBytes = 0
		s.state.MonthDownloadBytes = 0
	}
	if s.state.Day != day {
		s.state.TodayUploadBytes = 0
		s.state.TodayDownloadBytes = 0
	}
}

func (s *NetworkUsageSampler) sampleRate(current networkCounters, sampledAt time.Time) (float64, float64) {
	if !s.rateReady || !sampledAt.After(s.rateSampledAt) || current.sent < s.ratePrevious.sent || current.received < s.ratePrevious.received {
		s.ratePrevious = current
		s.rateSampledAt = sampledAt
		s.rateReady = true
		return 0, 0
	}
	seconds := sampledAt.Sub(s.rateSampledAt).Seconds()
	upload := float64(current.sent-s.ratePrevious.sent) / seconds / 1_000_000
	download := float64(current.received-s.ratePrevious.received) / seconds / 1_000_000
	s.ratePrevious = current
	s.rateSampledAt = sampledAt
	return upload, download
}

func (s *NetworkUsageSampler) reportError(err error) {
	if s.report != nil {
		s.report(err)
	}
}

func usagePeriod(now time.Time) (day, month string) {
	local := now.In(beijingLocation)
	return local.Format("2006-01-02"), local.Format("2006-01")
}

func LoadNetworkUsageState(path string) (NetworkUsageState, error) {
	file, err := os.Open(path)
	if err != nil {
		return NetworkUsageState{}, fmt.Errorf("open network usage state: %w", err)
	}
	defer file.Close()

	var state NetworkUsageState
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&state); err != nil {
		return NetworkUsageState{}, fmt.Errorf("decode network usage state: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return NetworkUsageState{}, err
	}
	if state.Schema != networkUsageStateSchema {
		return NetworkUsageState{}, fmt.Errorf("network usage state schema %d is unsupported", state.Schema)
	}
	return state, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode network usage state: %w", err)
	}
	return errors.New("decode network usage state: multiple JSON values")
}

func SaveNetworkUsageState(path string, state NetworkUsageState) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create network usage directory: %w", err)
	}
	if err := secureConfigDirectory(directory); err != nil {
		return fmt.Errorf("secure network usage directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".network-usage-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary network usage state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()

	if err := secureConfigFile(temporaryPath); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure temporary network usage state: %w", err)
	}
	if err := json.NewEncoder(temporary).Encode(state); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode network usage state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary network usage state: %w", err)
	}
	if err := closeExistingNetworkUsageState(path); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace network usage state: %w", err)
	}
	return nil
}

func closeExistingNetworkUsageState(path string) error {
	existing, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open existing network usage state for replacement: %w", err)
	}
	if err := existing.Close(); err != nil {
		return fmt.Errorf("close existing network usage state for replacement: %w", err)
	}
	return nil
}
