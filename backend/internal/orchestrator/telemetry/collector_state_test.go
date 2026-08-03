package telemetry_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ly-route/backend/internal/orchestrator/telemetry"
)

func TestCollector_idle_neighbors_and_connections_expire_explicitly(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	item := observation(now, []telemetry.InterfaceCounter{
		{Name: "wan0", RXBytes: 100, TXBytes: 50, LinkUp: true},
		{Name: "lan0", RXBytes: 50, TXBytes: 100, LinkUp: true},
	})
	item.Neighbors = append(item.Neighbors, telemetry.NeighborEntry{IP: "192.0.2.20", MAC: "00:11:22:33:44:66", Interface: "lan0", LastSeen: now.Add(-6 * time.Minute)})
	item.Connections = append(item.Connections, telemetry.ConnectionEntry{ID: "flow-expired", SourceIP: "192.0.2.20", DestinationIP: "203.0.113.20", Protocol: "udp", LastSeen: now.Add(-6 * time.Minute), Bytes: 2_000})
	collector := telemetry.NewCollector(mustTopology(t, false), &sequenceSource{observations: []telemetry.Observation{item}}, &fakeClock{now: now})

	// When
	snapshot := collector.Collect(context.Background())

	// Then
	if len(snapshot.OnlineUsers) != 1 || snapshot.OnlineUsers[0].IP != "192.0.2.10" {
		t.Fatalf("online users = %#v, want only active neighbor", snapshot.OnlineUsers)
	}
	if len(snapshot.TopConnections) != 1 || snapshot.TopConnections[0].ID != "flow-1" {
		t.Fatalf("top connections = %#v, want only active session", snapshot.TopConnections)
	}
	if snapshot.Expiration.Users != 1 || snapshot.Expiration.Connections != 1 {
		t.Fatalf("expiration = %#v, want one user and connection", snapshot.Expiration)
	}
}

func TestCollector_link_failure_marks_only_affected_group_unavailable(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)
	item := observation(now, []telemetry.InterfaceCounter{
		{Name: "wan0", RXBytes: 1_000, TXBytes: 500, LinkUp: true},
		{Name: "lan0", RXBytes: 490, TXBytes: 970, LinkUp: true},
		{Name: "east-lan", RXBytes: 970, TXBytes: 490, LinkUp: false},
		{Name: "east-wan", RXBytes: 500, TXBytes: 1_000, LinkUp: true},
		{Name: "west-lan", RXBytes: 970, TXBytes: 490, LinkUp: true},
		{Name: "west-wan", RXBytes: 500, TXBytes: 1_000, LinkUp: true},
	})
	collector := telemetry.NewCollector(mustTopology(t, true), &sequenceSource{observations: []telemetry.Observation{item}}, &fakeClock{now: now})

	// When
	snapshot := collector.Collect(context.Background())

	// Then
	if snapshot.Status.State != telemetry.StateAvailable || snapshot.Totals.WAN.State != telemetry.StateAvailable {
		t.Fatalf("boundary status = %#v totals %#v, want available", snapshot.Status, snapshot.Totals)
	}
	if snapshot.Groups[0].Name != "inline-east" || snapshot.Groups[0].State != telemetry.StateUnavailable || !strings.Contains(snapshot.Groups[0].Reason, "east-lan") {
		t.Fatalf("failed group = %#v, want inline-east arm failure", snapshot.Groups[0])
	}
	if snapshot.Groups[1].Name != "inline-west" || snapshot.Groups[1].State != telemetry.StateAvailable {
		t.Fatalf("healthy group = %#v, want inline-west available", snapshot.Groups[1])
	}
}

func TestCollector_missing_or_failed_source_is_explicitly_unavailable(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		source telemetry.Source
		reason string
	}{
		{name: "missing", source: nil, reason: "not configured"},
		{name: "failed", source: &sequenceSource{err: errors.New("secret stats socket path")}, reason: "collector failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			collector := telemetry.NewCollector(mustTopology(t, false), test.source, &fakeClock{now: now})

			// When
			snapshot := collector.Collect(context.Background())

			// Then
			if snapshot.Status.State != telemetry.StateUnavailable || !strings.Contains(snapshot.Status.Reason, test.reason) {
				t.Fatalf("status = %#v, want unavailable reason %q", snapshot.Status, test.reason)
			}
			if strings.Contains(snapshot.Status.Reason, "secret stats socket path") {
				t.Fatalf("status leaked source error: %#v", snapshot.Status)
			}
			if snapshot.Totals.WAN.State != telemetry.StateUnavailable || len(snapshot.History.Traffic) != 0 {
				t.Fatalf("unavailable snapshot = %#v", snapshot)
			}
		})
	}
}

func TestCollector_stale_observation_and_rolling_retention_are_deterministic(t *testing.T) {
	// Given
	start := time.Date(2026, 7, 28, 15, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: start.Add(3 * time.Minute)}
	first := observation(start, []telemetry.InterfaceCounter{{Name: "wan0", LinkUp: true}, {Name: "lan0", LinkUp: true}})
	secondAt := start.Add(24*time.Hour + time.Second)
	second := observation(secondAt, []telemetry.InterfaceCounter{{Name: "wan0", RXBytes: 10, LinkUp: true}, {Name: "lan0", TXBytes: 10, LinkUp: true}})
	collector := telemetry.NewCollector(mustTopology(t, false), &sequenceSource{observations: []telemetry.Observation{first, second}}, clock)

	// When
	stale := collector.Collect(context.Background())
	clock.now = secondAt
	fresh := collector.Collect(context.Background())

	// Then
	if stale.Status.State != telemetry.StateStale || stale.Status.Fresh || stale.Status.ObservedAt != start {
		t.Fatalf("stale status = %#v", stale.Status)
	}
	if fresh.Status.State != telemetry.StateAvailable || !fresh.Status.Fresh {
		t.Fatalf("fresh status = %#v", fresh.Status)
	}
	if len(fresh.History.Traffic) != 1 || fresh.History.Traffic[0].Timestamp != secondAt {
		t.Fatalf("traffic history = %#v, want first point pruned after 24h", fresh.History.Traffic)
	}
	if len(fresh.History.Connections) != 1 || fresh.History.Connections[0].Timestamp != secondAt {
		t.Fatalf("connection history = %#v, want rolling 24h retention", fresh.History.Connections)
	}
}

func TestCollector_future_observation_is_explicitly_unavailable(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC)
	future := observation(now.Add(time.Second), []telemetry.InterfaceCounter{
		{Name: "wan0", LinkUp: true},
		{Name: "lan0", LinkUp: true},
	})
	collector := telemetry.NewCollector(mustTopology(t, false), &sequenceSource{observations: []telemetry.Observation{future}}, &fakeClock{now: now})

	// When
	snapshot := collector.Collect(context.Background())

	// Then
	if snapshot.Status.State != telemetry.StateUnavailable || !strings.Contains(snapshot.Status.Reason, "future") {
		t.Fatalf("future observation status = %#v, want explicit unavailable", snapshot.Status)
	}
}

func TestCollector_records_collection_time_after_source_observation(t *testing.T) {
	start := time.Date(2026, 7, 29, 15, 30, 0, 0, time.UTC)
	clock := &fakeClock{now: start}
	source := telemetry.Source(advancingSource{clock: clock})
	collector := telemetry.NewCollector(mustTopology(t, false), source, clock)

	snapshot := collector.Collect(context.Background())

	if snapshot.Status.State != telemetry.StateAvailable || !snapshot.Status.ObservedAt.Equal(start.Add(time.Millisecond)) || !snapshot.Status.CollectedAt.Equal(start.Add(time.Millisecond)) {
		t.Fatalf("status = %#v, want observation followed by collection time", snapshot.Status)
	}
}

type advancingSource struct{ clock *fakeClock }

func (source advancingSource) Observe(context.Context) (telemetry.Observation, error) {
	source.clock.now = source.clock.now.Add(time.Millisecond)
	return observation(source.clock.now, []telemetry.InterfaceCounter{{Name: "wan0", LinkUp: true}, {Name: "lan0", LinkUp: true}}), nil
}

func TestCollector_missing_clock_is_explicitly_unavailable(t *testing.T) {
	// Given
	collector := telemetry.NewCollector(mustTopology(t, false), nil, nil)

	// When
	snapshot := collector.Collect(context.Background())

	// Then
	if snapshot.Status.State != telemetry.StateUnavailable || !strings.Contains(snapshot.Status.Reason, "clock") {
		t.Fatalf("missing clock status = %#v, want explicit unavailable", snapshot.Status)
	}
}
