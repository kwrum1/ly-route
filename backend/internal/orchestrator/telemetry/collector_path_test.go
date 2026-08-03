package telemetry_test

import (
	"context"
	"math"
	"testing"
	"time"

	"ly-route/backend/internal/orchestrator"
	"ly-route/backend/internal/orchestrator/telemetry"
)

func TestCollector_direct_path_reconciles_boundary_totals_and_rates(t *testing.T) {
	// Given
	observedAt := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: observedAt}
	source := &sequenceSource{observations: []telemetry.Observation{
		observation(observedAt, []telemetry.InterfaceCounter{
			{Name: "wan0", RXBytes: 1_000, TXBytes: 500, LinkUp: true},
			{Name: "lan0", RXBytes: 490, TXBytes: 970, LinkUp: true},
		}),
		observation(observedAt.Add(10*time.Second), []telemetry.InterfaceCounter{
			{Name: "wan0", RXBytes: 2_000, TXBytes: 1_000, LinkUp: true},
			{Name: "lan0", RXBytes: 980, TXBytes: 1_940, LinkUp: true},
		}),
	}}
	collector := telemetry.NewCollector(mustTopology(t, false), source, clock)

	// When
	first := collector.Collect(context.Background())
	clock.now = observedAt.Add(10 * time.Second)
	second := collector.Collect(context.Background())

	// Then
	if first.Status.State != telemetry.StateAvailable || !first.Status.Fresh {
		t.Fatalf("first status = %#v, want fresh available", first.Status)
	}
	if first.Totals.WAN.WANToLAN.Bytes != 1_000 || first.Totals.LAN.WANToLAN.Bytes != 970 {
		t.Fatalf("WAN-to-LAN totals = WAN %d LAN %d", first.Totals.WAN.WANToLAN.Bytes, first.Totals.LAN.WANToLAN.Bytes)
	}
	if first.Totals.WAN.LANToWAN.Bytes != 500 || first.Totals.LAN.LANToWAN.Bytes != 490 {
		t.Fatalf("LAN-to-WAN totals = WAN %d LAN %d", first.Totals.WAN.LANToWAN.Bytes, first.Totals.LAN.LANToWAN.Bytes)
	}
	if first.Reconciliation.WANToLAN.DifferencePercent > 5 || first.Reconciliation.LANToWAN.DifferencePercent > 5 {
		t.Fatalf("reconciliation = %#v, want both directions within 5%%", first.Reconciliation)
	}
	if first.Totals.WAN.WANToLAN.RateState != telemetry.StateUnavailable {
		t.Fatalf("first rate state = %q, want unavailable without baseline", first.Totals.WAN.WANToLAN.RateState)
	}
	assertRate(t, second.Totals.WAN.WANToLAN, 100)
	assertRate(t, second.Totals.WAN.LANToWAN, 50)
	assertRate(t, second.Totals.LAN.WANToLAN, 97)
	assertRate(t, second.Totals.LAN.LANToWAN, 49)
	if len(second.History.Traffic) != 2 || second.History.WindowSeconds != int64((24*time.Hour)/time.Second) {
		t.Fatalf("history = %#v, want two points and 24h window", second.History)
	}
}

func TestCollector_multi_hop_groups_are_directional_and_non_additive(t *testing.T) {
	// Given
	observedAt := time.Date(2026, 7, 29, 11, 0, 0, 0, time.UTC)
	interfaces := []telemetry.InterfaceCounter{
		{Name: "wan0", RXBytes: 10_000, TXBytes: 5_000, LinkUp: true},
		{Name: "lan0", RXBytes: 4_900, TXBytes: 9_700, LinkUp: true},
		{Name: "east-lan", RXBytes: 9_800, TXBytes: 4_950, LinkUp: true},
		{Name: "east-wan", RXBytes: 4_980, TXBytes: 9_900, LinkUp: true},
		{Name: "west-lan", RXBytes: 9_750, TXBytes: 4_925, LinkUp: true},
		{Name: "west-wan", RXBytes: 4_960, TXBytes: 9_850, LinkUp: true},
	}
	source := &sequenceSource{observations: []telemetry.Observation{observation(observedAt, interfaces)}}
	collector := telemetry.NewCollector(mustTopology(t, true), source, &fakeClock{now: observedAt})

	// When
	snapshot := collector.Collect(context.Background())

	// Then
	if len(snapshot.Groups) != 2 {
		t.Fatalf("groups = %#v, want two traversable groups", snapshot.Groups)
	}
	want := map[string][2]uint64{"inline-east": {9_800, 4_980}, "inline-west": {9_750, 4_960}}
	var repeatedWANToLAN uint64
	for _, group := range snapshot.Groups {
		if group.Additive {
			t.Fatalf("group %q is additive, want diagnostic non-additive counter", group.Name)
		}
		bytes, exists := want[group.Name]
		if !exists || group.WANToLAN.Bytes != bytes[0] || group.LANToWAN.Bytes != bytes[1] {
			t.Fatalf("group %q directions = %#v, want %#v", group.Name, group, bytes)
		}
		repeatedWANToLAN += group.WANToLAN.Bytes
	}
	if repeatedWANToLAN <= snapshot.Totals.WAN.WANToLAN.Bytes {
		t.Fatalf("group sum = %d, want repeated hop bytes greater than WAN total %d", repeatedWANToLAN, snapshot.Totals.WAN.WANToLAN.Bytes)
	}
	if snapshot.Totals.WAN.WANToLAN.Bytes != 10_000 {
		t.Fatalf("WAN total = %d, want boundary counter independent of group sum", snapshot.Totals.WAN.WANToLAN.Bytes)
	}
	if len(snapshot.PolicyHits) != 1 || snapshot.PolicyHits[0].PolicyID != "allow-web" || snapshot.PolicyHits[0].Hits != 17 {
		t.Fatalf("policy hits = %#v", snapshot.PolicyHits)
	}
}

func assertRate(t *testing.T, counter telemetry.DirectionalCounter, want float64) {
	t.Helper()
	if counter.RateState != telemetry.StateAvailable || math.Abs(counter.BytesPerSecond-want) > 0.001 {
		t.Fatalf("rate = %#v, want %.3f available", counter, want)
	}
}

func observation(observedAt time.Time, interfaces []telemetry.InterfaceCounter) telemetry.Observation {
	return telemetry.Observation{
		ObservedAt:  observedAt,
		Interfaces:  interfaces,
		PolicyHits:  []telemetry.PolicyHitCounter{{PolicyID: "allow-web", Hits: 17}},
		Neighbors:   []telemetry.NeighborEntry{{IP: "192.0.2.10", MAC: "00:11:22:33:44:55", Interface: "lan0", LastSeen: observedAt, RXBytes: 300, TXBytes: 700}},
		Connections: []telemetry.ConnectionEntry{{ID: "flow-1", SourceIP: "192.0.2.10", DestinationIP: "198.51.100.10", Protocol: "tcp", SourcePort: 50000, DestinationPort: 443, Bytes: 1_000, Packets: 10, LastSeen: observedAt, Groups: []string{"inline-east", "inline-west"}}},
	}
}

func mustTopology(t *testing.T, withGroups bool) orchestrator.Topology {
	t.Helper()
	input := orchestrator.TopologyInput{
		SchemaVersion:       orchestrator.SchemaVersion,
		ManagementInterface: "eth0",
		Interfaces: []orchestrator.InterfaceInput{
			{Name: "lan", Role: orchestrator.RoleLAN, Port: "lan0"},
			{Name: "wan", Role: orchestrator.RoleWAN, Port: "wan0"},
		},
	}
	if withGroups {
		input.Groups = []orchestrator.GroupInput{
			{Name: "inline-west", Ports: []orchestrator.DirectedPortInput{{Interface: "west-wan", Direction: orchestrator.DirectionWANFacing}, {Interface: "west-lan", Direction: orchestrator.DirectionLANFacing}}},
			{Name: "inline-east", Ports: []orchestrator.DirectedPortInput{{Interface: "east-lan", Direction: orchestrator.DirectionLANFacing}, {Interface: "east-wan", Direction: orchestrator.DirectionWANFacing}}},
		}
	}
	topology, err := orchestrator.ParseTopology(input)
	if err != nil {
		t.Fatalf("ParseTopology: %v", err)
	}
	return topology
}

type fakeClock struct {
	now time.Time
}

func (clock *fakeClock) Now() time.Time { return clock.now }

type sequenceSource struct {
	observations []telemetry.Observation
	err          error
	next         int
}

func (source *sequenceSource) Observe(context.Context) (telemetry.Observation, error) {
	if source.err != nil {
		return telemetry.Observation{}, source.err
	}
	index := source.next
	if index >= len(source.observations) {
		index = len(source.observations) - 1
	}
	source.next++
	return source.observations[index], nil
}
