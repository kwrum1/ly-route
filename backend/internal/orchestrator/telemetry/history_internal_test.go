package telemetry

import (
	"testing"
	"time"
)

func TestCollector_history_has_hard_point_bound_within_retention_window(t *testing.T) {
	// Given
	collector := &Collector{}
	start := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	totals := BoundaryTotals{
		WAN: EndpointTraffic{State: StateAvailable},
		LAN: EndpointTraffic{State: StateAvailable},
	}

	// When
	for index := 0; index < maxHistoryPoints+1; index++ {
		collector.record(start.Add(time.Duration(index)*time.Millisecond), totals, nil, nil)
	}

	// Then
	if len(collector.trafficHistory) != maxHistoryPoints || len(collector.connectionHistory) != maxHistoryPoints {
		t.Fatalf("history lengths = traffic %d connections %d, want hard bound %d", len(collector.trafficHistory), len(collector.connectionHistory), maxHistoryPoints)
	}
}
