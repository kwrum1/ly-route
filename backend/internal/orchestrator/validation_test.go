package orchestrator

import (
	"errors"
	"strings"
	"testing"
)

func TestParseTopology_rejects_invalid_boundary_values(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(TopologyInput) TopologyInput
		wantErr error
	}{
		{
			name: "unsupported schema version",
			mutate: func(input TopologyInput) TopologyInput {
				input.SchemaVersion = 2
				return input
			},
			wantErr: ErrInvalidSchemaVersion,
		},
		{
			name: "non-ASCII physical interface",
			mutate: func(input TopologyInput) TopologyInput {
				input.Interfaces[1].Port = "以太网3"
				return input
			},
			wantErr: ErrInvalidName,
		},
		{
			name: "overlong interface name",
			mutate: func(input TopologyInput) TopologyInput {
				input.Interfaces[1].Port = strings.Repeat("a", maxNameLength+1)
				return input
			},
			wantErr: ErrInvalidName,
		},
		{
			name: "logical interface has port and bond",
			mutate: func(input TopologyInput) TopologyInput {
				input.Interfaces[1].Bond = &BondInput{Name: "bond-wan", Members: []string{"eth8", "eth9"}}
				return input
			},
			wantErr: ErrInvalidInterface,
		},
		{
			name: "bond has one member",
			mutate: func(input TopologyInput) TopologyInput {
				input.Interfaces[0].Bond.Members = []string{"eth1"}
				return input
			},
			wantErr: ErrInvalidBond,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			input := test.mutate(cloneTopologyInput(validTopologyInput()))

			// When
			_, err := ParseTopology(input)

			// Then
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ParseTopology error = %v, want %v", err, test.wantErr)
			}
		})
	}
}
