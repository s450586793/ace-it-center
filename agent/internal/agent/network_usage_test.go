package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNetworkUsageSamplerEstablishesBaselineThenAccumulates(t *testing.T) {
	start := beijingTestTime(2026, time.August, 3, 9, 0)
	var saved NetworkUsageState
	sampler := newNetworkUsageSampler(
		sequenceReader([]networkReadResult{
			{counters: networkCounters{sent: 10_000_000, received: 20_000_000}},
			{counters: networkCounters{sent: 13_000_000, received: 28_000_000}},
		}),
		sequenceClock([]time.Time{start, start.Add(2 * time.Second)}),
		func() (NetworkUsageState, error) { return NetworkUsageState{}, os.ErrNotExist },
		func(state NetworkUsageState) error { saved = state; return nil },
		nil,
	)

	first := sampler.Sample()
	second := sampler.Sample()

	if !first.MetricsAvailable || !first.UsageAvailable || first.UsageDay != "2026-08-03" || first.TodayUploadBytes != 0 || first.TodayDownloadBytes != 0 {
		t.Fatalf("first snapshot = %#v", first)
	}
	if second.UploadMBPerSecond != 1.5 || second.DownloadMBPerSecond != 4 ||
		second.TodayUploadBytes != 3_000_000 || second.TodayDownloadBytes != 8_000_000 ||
		second.MonthUploadBytes != 3_000_000 || second.MonthDownloadBytes != 8_000_000 {
		t.Fatalf("second snapshot = %#v", second)
	}
	if saved.PreviousSentBytes != 13_000_000 || saved.PreviousReceivedBytes != 28_000_000 || !saved.Initialized {
		t.Fatalf("saved state = %#v", saved)
	}
}

func TestNetworkUsageSamplerResumesFromPersistedState(t *testing.T) {
	start := beijingTestTime(2026, time.August, 3, 10, 0)
	state := usageState("2026-08-03", "2026-08", 5_000_000, 8_000_000, 15_000_000, 28_000_000, 100_000_000, 200_000_000)
	sampler := newNetworkUsageSampler(
		sequenceReader([]networkReadResult{{counters: networkCounters{sent: 103_000_000, received: 209_000_000}}}),
		sequenceClock([]time.Time{start}),
		func() (NetworkUsageState, error) { return state, nil },
		func(next NetworkUsageState) error { state = next; return nil },
		nil,
	)

	snapshot := sampler.Sample()
	if snapshot.UploadMBPerSecond != 0 || snapshot.DownloadMBPerSecond != 0 {
		t.Fatalf("first process rates = %v/%v, want zero", snapshot.UploadMBPerSecond, snapshot.DownloadMBPerSecond)
	}
	if snapshot.TodayUploadBytes != 8_000_000 || snapshot.TodayDownloadBytes != 17_000_000 ||
		snapshot.MonthUploadBytes != 18_000_000 || snapshot.MonthDownloadBytes != 37_000_000 {
		t.Fatalf("resumed snapshot = %#v", snapshot)
	}
}

func TestNetworkUsageSamplerRollsDayButKeepsMonth(t *testing.T) {
	state := usageState("2026-08-02", "2026-08", 50, 80, 150, 280, 1_000, 2_000)
	snapshot := samplePersistedUsage(t, beijingTestTime(2026, time.August, 3, 0, 0), &state, networkCounters{sent: 1_030, received: 2_090})

	if snapshot.UsageDay != "2026-08-03" || snapshot.TodayUploadBytes != 30 || snapshot.TodayDownloadBytes != 90 ||
		snapshot.MonthUploadBytes != 180 || snapshot.MonthDownloadBytes != 370 {
		t.Fatalf("rolled day snapshot = %#v", snapshot)
	}
}

func TestNetworkUsageSamplerRollsMonth(t *testing.T) {
	state := usageState("2026-07-31", "2026-07", 50, 80, 150, 280, 1_000, 2_000)
	snapshot := samplePersistedUsage(t, beijingTestTime(2026, time.August, 1, 0, 0), &state, networkCounters{sent: 1_030, received: 2_090})

	if snapshot.UsageDay != "2026-08-01" || snapshot.TodayUploadBytes != 30 || snapshot.TodayDownloadBytes != 90 ||
		snapshot.MonthUploadBytes != 30 || snapshot.MonthDownloadBytes != 90 {
		t.Fatalf("rolled month snapshot = %#v", snapshot)
	}
}

func TestNetworkUsageSamplerResetsBothDirectionsAfterCounterRollback(t *testing.T) {
	state := usageState("2026-08-03", "2026-08", 50, 80, 150, 280, 1_000, 2_000)
	snapshot := samplePersistedUsage(t, beijingTestTime(2026, time.August, 3, 11, 0), &state, networkCounters{sent: 900, received: 2_090})

	if snapshot.TodayUploadBytes != 50 || snapshot.TodayDownloadBytes != 80 ||
		snapshot.MonthUploadBytes != 150 || snapshot.MonthDownloadBytes != 280 {
		t.Fatalf("rollback snapshot = %#v", snapshot)
	}
	if state.PreviousSentBytes != 900 || state.PreviousReceivedBytes != 2_090 {
		t.Fatalf("rollback baseline = %#v", state)
	}
}

func TestNetworkUsageSamplerRecoversFromCorruptState(t *testing.T) {
	start := beijingTestTime(2026, time.August, 3, 12, 0)
	var reported []error
	var saved NetworkUsageState
	sampler := newNetworkUsageSampler(
		sequenceReader([]networkReadResult{{counters: networkCounters{sent: 100, received: 200}}}),
		sequenceClock([]time.Time{start}),
		func() (NetworkUsageState, error) { return NetworkUsageState{}, errors.New("decode network usage state") },
		func(state NetworkUsageState) error { saved = state; return nil },
		func(err error) { reported = append(reported, err) },
	)

	snapshot := sampler.Sample()
	if !snapshot.UsageAvailable || snapshot.TodayUploadBytes != 0 || snapshot.TodayDownloadBytes != 0 {
		t.Fatalf("recovered snapshot = %#v", snapshot)
	}
	if len(reported) != 1 || reported[0] == nil || !saved.Initialized {
		t.Fatalf("reported=%v saved=%#v", reported, saved)
	}
}

func TestNetworkUsageSamplerRetriesStateSaveWithoutLosingInMemoryTotals(t *testing.T) {
	start := beijingTestTime(2026, time.August, 3, 13, 0)
	saveCalls := 0
	var reported []error
	sampler := newNetworkUsageSampler(
		sequenceReader([]networkReadResult{
			{counters: networkCounters{sent: 100, received: 200}},
			{counters: networkCounters{sent: 130, received: 290}},
		}),
		sequenceClock([]time.Time{start, start.Add(time.Second)}),
		func() (NetworkUsageState, error) { return NetworkUsageState{}, os.ErrNotExist },
		func(NetworkUsageState) error {
			saveCalls++
			if saveCalls == 1 {
				return errors.New("disk full")
			}
			return nil
		},
		func(err error) { reported = append(reported, err) },
	)

	first := sampler.Sample()
	second := sampler.Sample()
	if first.TodayUploadBytes != 0 || second.TodayUploadBytes != 30 || second.TodayDownloadBytes != 90 {
		t.Fatalf("snapshots first=%#v second=%#v", first, second)
	}
	if saveCalls != 2 || len(reported) != 1 {
		t.Fatalf("save calls=%d reported=%v", saveCalls, reported)
	}
}

func TestNetworkUsageStateFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "network-usage.json")
	want := usageState("2026-08-03", "2026-08", 1, 2, 3, 4, 5, 6)
	if err := SaveNetworkUsageState(path, want); err != nil {
		t.Fatalf("SaveNetworkUsageState() error = %v", err)
	}
	got, err := LoadNetworkUsageState(path)
	if err != nil {
		t.Fatalf("LoadNetworkUsageState() error = %v", err)
	}
	if got != want {
		t.Fatalf("loaded state = %#v, want %#v", got, want)
	}
}

func samplePersistedUsage(t *testing.T, now time.Time, state *NetworkUsageState, counters networkCounters) NetworkSnapshot {
	t.Helper()
	sampler := newNetworkUsageSampler(
		sequenceReader([]networkReadResult{{counters: counters}}),
		sequenceClock([]time.Time{now}),
		func() (NetworkUsageState, error) { return *state, nil },
		func(next NetworkUsageState) error { *state = next; return nil },
		nil,
	)
	return sampler.Sample()
}

func usageState(day, month string, todayUp, todayDown, monthUp, monthDown, previousSent, previousReceived uint64) NetworkUsageState {
	return NetworkUsageState{
		Schema:                 networkUsageStateSchema,
		Day:                    day,
		Month:                  month,
		TodayUploadBytes:       todayUp,
		TodayDownloadBytes:     todayDown,
		MonthUploadBytes:       monthUp,
		MonthDownloadBytes:     monthDown,
		PreviousSentBytes:      previousSent,
		PreviousReceivedBytes:  previousReceived,
		Initialized:            true,
	}
}

func beijingTestTime(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
}
