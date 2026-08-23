package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type outOfOrderGatewayTelemetry struct {
	calls        atomic.Int32
	firstStarted chan struct{}
	releaseFirst chan struct{}
	newer        GatewayTelemetrySnapshot
}

func (collector *outOfOrderGatewayTelemetry) Collect(context.Context) (GatewayTelemetrySnapshot, error) {
	if collector.calls.Add(1) == 1 {
		close(collector.firstStarted)
		<-collector.releaseFirst
		return GatewayTelemetrySnapshot{}, errors.New("older collection failed")
	}
	return collector.newer, nil
}

func TestGatewayTelemetryIgnoresOlderCompletionAfterNewerSuccess(t *testing.T) {
	observedAt := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	collector := &outOfOrderGatewayTelemetry{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
		newer: GatewayTelemetrySnapshot{ObservedAt: observedAt, LogicalEgresses: []LogicalEgressCounter{
			{ID: "wan-a", Kind: LogicalEgressDirectWAN, Health: "healthy", DownloadBytes: 1_000, UploadBytes: 500},
		}},
	}
	server := New(WithClock(func() time.Time { return observedAt }), WithGatewayTelemetry(collector))
	olderDone := make(chan error, 1)
	go func() { olderDone <- server.collectGatewayTelemetry(context.Background()) }()
	<-collector.firstStarted

	newerErr := server.collectGatewayTelemetry(context.Background())
	close(collector.releaseFirst)
	olderErr := <-olderDone
	result := server.gatewayTrafficTrend(TrafficTrendQuery{Window: "24h", Points: 288})

	if newerErr != nil || olderErr != nil {
		t.Fatalf("collection errors = newer %v, older %v; want superseded completion ignored", newerErr, olderErr)
	}
	if result.State != "available" || result.Degraded {
		t.Fatalf("trend after out-of-order completion = %#v, want newer success available", result)
	}
}

func TestGatewayTelemetryRejectsFutureSourceTimestamp(t *testing.T) {
	now := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	collector := &scriptedGatewayTelemetry{snapshots: []GatewayTelemetrySnapshot{{
		ObservedAt:      now.Add(time.Second),
		LogicalEgresses: []LogicalEgressCounter{{ID: "wan-a", Kind: LogicalEgressDirectWAN, Health: "healthy"}},
	}}}
	server := New(WithClock(func() time.Time { return now }), WithGatewayTelemetry(collector))

	err := server.collectGatewayTelemetry(context.Background())
	result := server.gatewayTrafficTrend(TrafficTrendQuery{Window: "24h", Points: 288})

	if err == nil || !strings.Contains(err.Error(), "future") {
		t.Fatalf("future collection error = %v, want future timestamp rejection", err)
	}
	if result.State != "unavailable" || !result.Degraded {
		t.Fatalf("future collection trend = %#v, want unavailable", result)
	}
}

func TestGatewayTelemetryBucketsReadbacksAtFiveSecondCadence(t *testing.T) {
	start := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	state := newGatewayTelemetryState()

	state.record(GatewayTelemetrySnapshot{ObservedAt: start.Add(time.Second), LogicalEgresses: []LogicalEgressCounter{{ID: "wan-a", Kind: LogicalEgressDirectWAN, Health: "healthy", DownloadBytes: 1_000, UploadBytes: 500}}})
	state.record(GatewayTelemetrySnapshot{ObservedAt: start.Add(4 * time.Second), LogicalEgresses: []LogicalEgressCounter{{ID: "wan-a", Kind: LogicalEgressDirectWAN, Health: "healthy", DownloadBytes: 2_000, UploadBytes: 1_000}}})
	state.record(GatewayTelemetrySnapshot{ObservedAt: start.Add(6 * time.Second), LogicalEgresses: []LogicalEgressCounter{{ID: "wan-a", Kind: LogicalEgressDirectWAN, Health: "healthy", DownloadBytes: 32_000, UploadBytes: 16_000}}})

	samples := state.series["wan-a"].samples
	if len(samples) != 2 || !samples[0].Timestamp.Equal(start) || !samples[1].Timestamp.Equal(start.Add(5*time.Second)) {
		t.Fatalf("bucketed samples = %#v, want two five-second buckets", samples)
	}
	if samples[0].DownloadBytes != 2_000 || samples[1].DownloadBPS == nil {
		t.Fatalf("bucket replacement/rate = %#v, want latest readback and derived rate", samples)
	}
	wantDownload := float64(30_000 * 8 / 5)
	assertWithinFivePercent(t, *samples[1].DownloadBPS, wantDownload)
}

func TestGatewayTelemetryBoundsSamplesPerSeries(t *testing.T) {
	start := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	state := newGatewayTelemetryState()
	for index := 0; index < gatewayLogicalEgressSampleLimit+1; index++ {
		state.record(GatewayTelemetrySnapshot{ObservedAt: start.Add(time.Duration(index) * 5 * time.Second), LogicalEgresses: []LogicalEgressCounter{{ID: "wan-a", Kind: LogicalEgressDirectWAN, Health: "healthy", DownloadBytes: int64(index)}}})
	}

	if got := len(state.series["wan-a"].samples); got != gatewayLogicalEgressSampleLimit {
		t.Fatalf("retained samples = %d, want bounded %d", got, gatewayLogicalEgressSampleLimit)
	}
}

func TestGatewayTelemetryBoundsConnectionsAndSeriesIdentities(t *testing.T) {
	observedAt := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	state := newGatewayTelemetryState()
	connections := make([]GatewayConnection, 10_001)
	for index := range connections {
		connections[index] = GatewayConnection{SourceIP: "192.168.88.10", DestinationPort: index, Bytes: int64(index)}
	}
	state.record(GatewayTelemetrySnapshot{ObservedAt: observedAt, Connections: connections})
	for index := 0; index < 1_025; index++ {
		state.record(GatewayTelemetrySnapshot{ObservedAt: observedAt, LogicalEgresses: []LogicalEgressCounter{{ID: fmt.Sprintf("wan-%04d", index), Kind: LogicalEgressDirectWAN, Health: "healthy"}}})
	}

	if len(state.connections) > 10_000 || len(state.series) > 1_024 {
		t.Fatalf("state bounds = %d connections, %d series; want <= 10000 and <= 1024", len(state.connections), len(state.series))
	}
}

func TestGatewayTrafficTrendMarksOmittedSeriesUnavailableAndExpiresIdentity(t *testing.T) {
	start := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	now := start
	collector := &scriptedGatewayTelemetry{snapshots: []GatewayTelemetrySnapshot{
		{ObservedAt: start, LogicalEgresses: []LogicalEgressCounter{{ID: "wan-a", Kind: LogicalEgressDirectWAN, Health: "healthy", DownloadBytes: 1_000, UploadBytes: 500}}},
		{ObservedAt: start.Add(5 * time.Minute), LogicalEgresses: []LogicalEgressCounter{{ID: "wan-b", Kind: LogicalEgressDirectWAN, Health: "healthy", DownloadBytes: 2_000, UploadBytes: 1_000}}},
		{ObservedAt: start.Add(25 * time.Hour), LogicalEgresses: []LogicalEgressCounter{{ID: "wan-b", Kind: LogicalEgressDirectWAN, Health: "healthy", DownloadBytes: 32_000, UploadBytes: 16_000}}},
	}}
	server := New(WithClock(func() time.Time { return now }), WithGatewayTelemetry(collector))

	if err := server.collectGatewayTelemetry(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = start.Add(5 * time.Minute)
	if err := server.collectGatewayTelemetry(context.Background()); err != nil {
		t.Fatal(err)
	}
	omitted := server.gatewayTrafficTrend(TrafficTrendQuery{Window: "24h", Points: 288})
	now = start.Add(25 * time.Hour)
	if err := server.collectGatewayTelemetry(context.Background()); err != nil {
		t.Fatal(err)
	}
	expired := server.gatewayTrafficTrend(TrafficTrendQuery{Window: "24h", Points: 288})

	wanA := logicalSeriesByID(t, omitted.Series.LogicalEgresses, "wan-a")
	if wanA.State != "unavailable" || wanA.Fresh {
		t.Fatalf("omitted series = %#v, want unavailable and not fresh", wanA)
	}
	for _, series := range expired.Series.LogicalEgresses {
		if series.ID == "wan-a" {
			t.Fatalf("expired series retained after 24 hours: %#v", expired.Series.LogicalEgresses)
		}
	}
}

func TestGatewayTrafficTrendFiltersSamplesToRequestedWindow(t *testing.T) {
	start := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	now := start
	collector := &scriptedGatewayTelemetry{snapshots: []GatewayTelemetrySnapshot{
		{ObservedAt: start, LogicalEgresses: []LogicalEgressCounter{{ID: "wan-a", Kind: LogicalEgressDirectWAN, Health: "healthy", DownloadBytes: 1_000, UploadBytes: 500}}},
		{ObservedAt: start.Add(10 * time.Minute), LogicalEgresses: []LogicalEgressCounter{{ID: "wan-a", Kind: LogicalEgressDirectWAN, Health: "healthy", DownloadBytes: 61_000, UploadBytes: 30_500}}},
	}}
	server := New(WithClock(func() time.Time { return now }), WithGatewayTelemetry(collector))

	if err := server.collectGatewayTelemetry(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = start.Add(10 * time.Minute)
	if err := server.collectGatewayTelemetry(context.Background()); err != nil {
		t.Fatal(err)
	}
	result := server.gatewayTrafficTrend(TrafficTrendQuery{Window: "5m", Points: 288})

	samples := logicalSeriesByID(t, result.Series.LogicalEgresses, "wan-a").Samples
	if len(samples) != 1 || !samples[0].Timestamp.Equal(now) {
		t.Fatalf("five-minute samples = %#v, want only requested window", samples)
	}
}
