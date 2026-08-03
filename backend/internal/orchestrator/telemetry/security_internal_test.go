package telemetry

import "testing"

func TestObservationExceedsLimits_rejects_each_oversized_source_slice(t *testing.T) {
	// Given
	tests := []struct {
		name        string
		observation Observation
	}{
		{name: "interfaces", observation: Observation{Interfaces: make([]InterfaceCounter, maxObservationInterfaces+1)}},
		{name: "policy hits", observation: Observation{PolicyHits: make([]PolicyHitCounter, maxObservationPolicyHits+1)}},
		{name: "neighbors", observation: Observation{Neighbors: make([]NeighborEntry, maxObservationNeighbors+1)}},
		{name: "connections", observation: Observation{Connections: make([]ConnectionEntry, maxObservationConnections+1)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// When
			exceeded := observationExceedsLimits(test.observation)

			// Then
			if !exceeded {
				t.Fatalf("oversized %s observation was accepted", test.name)
			}
		})
	}
}
