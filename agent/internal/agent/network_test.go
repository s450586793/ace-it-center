package agent

import (
	"errors"
	"testing"
	"time"
)

func TestNetworkRateSamplerUsesDeltaAndElapsedTime(t *testing.T) {
	fixedTime := time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)
	sampler := newNetworkRateSampler(
		sequenceReader([]networkReadResult{
			{counters: networkCounters{sent: 10_000_000, received: 20_000_000}},
			{counters: networkCounters{sent: 13_000_000, received: 28_000_000}},
		}),
		sequenceClock([]time.Time{fixedTime, fixedTime.Add(2 * time.Second)}),
	)

	firstUp, firstDown := sampler.Sample()
	secondUp, secondDown := sampler.Sample()
	if firstUp != 0 || firstDown != 0 || secondUp != 1.5 || secondDown != 4 {
		t.Fatalf("rates first=%v/%v second=%v/%v", firstUp, firstDown, secondUp, secondDown)
	}
}

func TestNetworkRateSamplerResetsAfterCounterRollback(t *testing.T) {
	fixedTime := time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)
	sampler := newNetworkRateSampler(
		sequenceReader([]networkReadResult{
			{counters: networkCounters{sent: 10_000_000, received: 20_000_000}},
			{counters: networkCounters{sent: 5_000_000, received: 15_000_000}},
			{counters: networkCounters{sent: 6_000_000, received: 17_000_000}},
		}),
		sequenceClock([]time.Time{fixedTime, fixedTime.Add(time.Second), fixedTime.Add(3 * time.Second)}),
	)

	sampler.Sample()
	rollbackUp, rollbackDown := sampler.Sample()
	recoveredUp, recoveredDown := sampler.Sample()
	if rollbackUp != 0 || rollbackDown != 0 || recoveredUp != 0.5 || recoveredDown != 1 {
		t.Fatalf("rates after rollback=%v/%v recovered=%v/%v", rollbackUp, rollbackDown, recoveredUp, recoveredDown)
	}
}

func TestNetworkRateSamplerResetsAfterNonPositiveElapsedTime(t *testing.T) {
	fixedTime := time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)
	sampler := newNetworkRateSampler(
		sequenceReader([]networkReadResult{
			{counters: networkCounters{sent: 10_000_000, received: 20_000_000}},
			{counters: networkCounters{sent: 13_000_000, received: 28_000_000}},
			{counters: networkCounters{sent: 16_000_000, received: 36_000_000}},
		}),
		sequenceClock([]time.Time{fixedTime, fixedTime, fixedTime.Add(2 * time.Second)}),
	)

	sampler.Sample()
	nonPositiveUp, nonPositiveDown := sampler.Sample()
	recoveredUp, recoveredDown := sampler.Sample()
	if nonPositiveUp != 0 || nonPositiveDown != 0 || recoveredUp != 1.5 || recoveredDown != 4 {
		t.Fatalf("rates after non-positive elapsed=%v/%v recovered=%v/%v", nonPositiveUp, nonPositiveDown, recoveredUp, recoveredDown)
	}
}

func TestNetworkRateSamplerRecoversAfterReadFailure(t *testing.T) {
	fixedTime := time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)
	sampler := newNetworkRateSampler(
		sequenceReader([]networkReadResult{
			{counters: networkCounters{sent: 10_000_000, received: 20_000_000}},
			{err: errors.New("read counters")},
			{counters: networkCounters{sent: 13_000_000, received: 28_000_000}},
		}),
		sequenceClock([]time.Time{fixedTime, fixedTime.Add(time.Second), fixedTime.Add(2 * time.Second)}),
	)

	sampler.Sample()
	failedUp, failedDown := sampler.Sample()
	recoveredUp, recoveredDown := sampler.Sample()
	if failedUp != 0 || failedDown != 0 || recoveredUp != 1.5 || recoveredDown != 4 {
		t.Fatalf("rates after failed read=%v/%v recovered=%v/%v", failedUp, failedDown, recoveredUp, recoveredDown)
	}
}

func sequenceReader(results []networkReadResult) func() (networkCounters, error) {
	index := 0
	return func() (networkCounters, error) {
		result := results[index]
		index++
		return result.counters, result.err
	}
}

type networkReadResult struct {
	counters networkCounters
	err      error
}

func sequenceClock(times []time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		value := times[index]
		index++
		return value
	}
}
