package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	orchestratortelemetry "ly-route/backend/internal/orchestrator/telemetry"
)

func TestVPPCTLOrchestratorTelemetrySourceParsesInterfacesAndNeighbors(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	run := func(_ context.Context, _ string, args ...string) (string, error) {
		switch args[len(args)-1] {
		case "interface":
			return "              Name               Idx    State  MTU (L3/IP4/IP6/MPLS)     Counter          Count\nlyroute-wan0                      1      up          9000/0/0/0     rx packets                    10\n                                                                    rx bytes                    1000\n                                                                    tx packets                     9\n                                                                    tx bytes                     900\nlyroute-inline-wan                2      up          9000/0/0/0     rx packets                     8\n                                                                    rx bytes                     800\n                                                                    tx packets                     7\n                                                                    tx bytes                     700\n", nil
		case "neighbors":
			return "     Age                       IP                    Flags      Ethernet              Interface\n     10.5000               192.0.2.10                  D    00:11:22:33:44:55 lyroute-lan0\n", nil
		case "orchestrator":
			return "state running\ngroup-health inline-a state bypass bypass-packets 12\npolicy office group-position 10 sequence 10 action via packets 7 bytes 700\nflow family ip4 src 192.0.2.10 dst 198.51.100.20 proto 6 sport 41000 dport 443 packets 4 bytes 400 age 2.500000 groups inline-a\n", nil
		default:
			return "", errors.New("unexpected command")
		}
	}
	source := vppctlOrchestratorTelemetrySource{binary: "vppctl", now: func() time.Time { return now }, run: run}
	observation, err := source.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Interfaces) != 2 || observation.Interfaces[0].Name != "wan0" || observation.Interfaces[0].RXBytes != 1000 || observation.Interfaces[0].TXBytes != 900 || !observation.Interfaces[0].LinkUp {
		t.Fatalf("interfaces = %#v", observation.Interfaces)
	}
	if len(observation.Neighbors) != 1 || observation.Neighbors[0].IP != "192.0.2.10" || observation.Neighbors[0].Interface != "lan0" || !observation.Neighbors[0].LastSeen.Equal(now.Add(-10_500*time.Millisecond)) {
		t.Fatalf("neighbors = %#v", observation.Neighbors)
	}
	if observation.Components.Interfaces.State != orchestratortelemetry.StateAvailable || observation.Components.Neighbors.State != orchestratortelemetry.StateAvailable || observation.Components.Connections.State != orchestratortelemetry.StateAvailable || observation.Components.PolicyHits.State != orchestratortelemetry.StateAvailable {
		t.Fatalf("components = %#v", observation.Components)
	}
	if len(observation.PolicyHits) != 1 || observation.PolicyHits[0].PolicyID != "office" || observation.PolicyHits[0].Hits != 7 || len(observation.Connections) != 1 || observation.Connections[0].Protocol != "tcp" || observation.Connections[0].Bytes != 400 {
		t.Fatalf("native telemetry = hits %#v connections %#v", observation.PolicyHits, observation.Connections)
	}
	if len(observation.GroupHealth) != 1 || !observation.GroupHealth[0].Bypass || observation.GroupHealth[0].BypassPackets != 12 {
		t.Fatalf("group health = %#v", observation.GroupHealth)
	}
}

func TestParseTransparentOrchestratorTelemetryAggregatesRuleVariantsAndWorkers(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	output := "policy office group-position 10 sequence 10 action via packets 3 bytes 300\npolicy office group-position 10 sequence 10 action via packets 4 bytes 400\n" +
		"flow family ip4 src 192.0.2.10 dst 198.51.100.20 proto 17 sport 50000 dport 53 packets 2 bytes 200 age 4.000000 groups dns-filter,ids\n" +
		"flow family ip4 src 192.0.2.10 dst 198.51.100.20 proto 17 sport 50000 dport 53 packets 5 bytes 500 age 2.000000 groups dns-filter,ids\n"
	hits, connections, health := parseTransparentOrchestratorTelemetry(output, now)
	if len(hits) != 1 || hits[0].Hits != 7 {
		t.Fatalf("hits = %#v", hits)
	}
	if len(connections) != 1 || connections[0].Packets != 7 || connections[0].Bytes != 700 || !connections[0].LastSeen.Equal(now.Add(-2*time.Second)) || len(connections[0].Groups) != 2 {
		t.Fatalf("connections = %#v", connections)
	}
	if len(health) != 0 {
		t.Fatalf("unexpected health = %#v", health)
	}
}

func TestVPPCTLOrchestratorTelemetrySourceMarksNeighborFailureUnavailable(t *testing.T) {
	run := func(_ context.Context, _ string, args ...string) (string, error) {
		if args[len(args)-1] == "interface" {
			return "lyroute-wan0 1 up 9000/0/0/0\n rx bytes 100\n tx bytes 90\n", nil
		}
		return "", errors.New("neighbor CLI unavailable")
	}
	observation, err := (vppctlOrchestratorTelemetrySource{now: time.Now, run: run}).Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if observation.Components.Neighbors.State != orchestratortelemetry.StateUnavailable || len(observation.Neighbors) != 0 {
		t.Fatalf("neighbor status = %#v entries=%#v", observation.Components.Neighbors, observation.Neighbors)
	}
}
