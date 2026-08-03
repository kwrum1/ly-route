package orchestrator

import (
	"errors"
	"reflect"
	"testing"
)

func TestParsePolicy_rejects_invalid_boundary_values(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(PolicyInput) PolicyInput
		wantErr error
	}{
		{"schema version", func(input PolicyInput) PolicyInput { input.SchemaVersion++; return input }, ErrInvalidPolicyVersion},
		{"empty IP object", func(input PolicyInput) PolicyInput { input.IPObjects[0].Prefixes = nil; return input }, ErrInvalidIPObject},
		{"invalid IP prefix", func(input PolicyInput) PolicyInput { input.IPObjects[0].Prefixes[0] = "not-an-ip"; return input }, ErrInvalidIPObject},
		{"duplicate IP object", func(input PolicyInput) PolicyInput {
			input.IPObjects = append(input.IPObjects, input.IPObjects[0])
			return input
		}, ErrDuplicateIPObject},
		{"empty source selector", func(input PolicyInput) PolicyInput { input.Groups[0].Rules[0].Match.Sources = nil; return input }, ErrInvalidPolicyMatch},
		{"any mixed with source object", func(input PolicyInput) PolicyInput {
			input.Groups[0].Rules[0].Match.Sources = []string{"any", "office"}
			return input
		}, ErrInvalidPolicyMatch},
		{"deleted source object", func(input PolicyInput) PolicyInput {
			input.Groups[0].Rules[0].Match.Sources = []string{"deleted"}
			return input
		}, ErrDeletedPolicyReference},
		{"invalid protocol", func(input PolicyInput) PolicyInput { input.Groups[0].Rules[0].Match.Protocol = "sctp"; return input }, ErrInvalidPolicyMatch},
		{"any protocol with ports", func(input PolicyInput) PolicyInput {
			input.Groups[0].Rules[0].Match.Protocol = ProtocolAny
			input.Groups[0].Rules[0].Match.DestinationPorts = []PortRangeInput{{Start: 443, End: 443}}
			return input
		}, ErrInvalidPolicyMatch},
		{"ICMP with ports", func(input PolicyInput) PolicyInput {
			input.Groups[0].Rules[0].Match.Protocol = ProtocolICMP
			input.Groups[0].Rules[0].Match.DestinationPorts = []PortRangeInput{{Start: 443, End: 443}}
			return input
		}, ErrInvalidPolicyMatch},
		{"zero port", func(input PolicyInput) PolicyInput {
			input.Groups[0].Rules[0].Match.Protocol = ProtocolTCP
			input.Groups[0].Rules[0].Match.DestinationPorts = []PortRangeInput{{Start: 0, End: 443}}
			return input
		}, ErrInvalidPolicyMatch},
		{"reversed port range", func(input PolicyInput) PolicyInput {
			input.Groups[0].Rules[0].Match.Protocol = ProtocolTCP
			input.Groups[0].Rules[0].Match.DestinationPorts = []PortRangeInput{{Start: 444, End: 443}}
			return input
		}, ErrInvalidPolicyMatch},
		{"duplicate policy group", func(input PolicyInput) PolicyInput {
			input.Groups = append(input.Groups, input.Groups[0])
			return input
		}, ErrDuplicatePolicyGroup},
		{"zero position", func(input PolicyInput) PolicyInput { input.Groups[0].Position = 0; return input }, ErrInvalidPolicyPosition},
		{"duplicate position", func(input PolicyInput) PolicyInput { input.Groups[1].Position = input.Groups[0].Position; return input }, ErrInvalidPolicyPosition},
		{"empty group", func(input PolicyInput) PolicyInput { input.Groups[0].Rules = nil; return input }, ErrInvalidPolicyGroup},
		{"invalid rule ID", func(input PolicyInput) PolicyInput { input.Groups[0].Rules[0].ID = ""; return input }, ErrInvalidPolicyRule},
		{"duplicate rule ID", func(input PolicyInput) PolicyInput {
			input.Groups[0].Rules = append(input.Groups[0].Rules, input.Groups[0].Rules[0])
			input.Groups[0].Rules[1].Sequence = 20
			return input
		}, ErrInvalidPolicyRule},
		{"zero sequence", func(input PolicyInput) PolicyInput { input.Groups[0].Rules[0].Sequence = 0; return input }, ErrInvalidRuleSequence},
		{"duplicate sequence", func(input PolicyInput) PolicyInput {
			input.Groups[0].Rules = append(input.Groups[0].Rules, input.Groups[0].Rules[0])
			input.Groups[0].Rules[1].ID = "other"
			return input
		}, ErrDuplicateRuleSequence},
		{"empty action", func(input PolicyInput) PolicyInput { input.Groups[0].Rules[0].Action = ActionInput{}; return input }, ErrInvalidPolicyAction},
		{"via without group", func(input PolicyInput) PolicyInput {
			input.Groups[0].Rules[0].Action = ActionInput{Kind: ActionVia}
			return input
		}, ErrInvalidPolicyAction},
		{"via deleted group", func(input PolicyInput) PolicyInput {
			input.Groups[0].Rules[0].Action = ActionInput{Kind: ActionVia, Group: "deleted"}
			return input
		}, ErrDeletedPolicyReference},
		{"direct with group", func(input PolicyInput) PolicyInput {
			input.Groups[0].Rules[0].Action = ActionInput{Kind: ActionDirect, Group: "inline-east"}
			return input
		}, ErrInvalidPolicyAction},
		{"duplicate traversal across groups", duplicateTraversal, ErrPolicyLoop},
		{"empty default", func(input PolicyInput) PolicyInput { input.Default = ActionInput{}; return input }, ErrEmptyPolicyDefault},
		{"default via group", func(input PolicyInput) PolicyInput {
			input.Default = ActionInput{Kind: ActionVia, Group: "inline-east"}
			return input
		}, ErrInvalidPolicyAction},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			input := test.mutate(validPolicyInput())
			before := clonePolicyInput(input)

			// When
			_, err := ParsePolicy(validPolicyTopology(t), input)

			// Then
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ParsePolicy error = %v, want %v", err, test.wantErr)
			}
			if !reflect.DeepEqual(input, before) {
				t.Fatal("ParsePolicy mutated rejected input")
			}
		})
	}
}

func TestParseFlow_rejects_protocol_port_mismatch(t *testing.T) {
	// Given
	input := FlowInput{SourceIP: "192.0.2.10", DestinationIP: "198.51.100.10", Protocol: ProtocolICMP, SourcePort: 1234}

	// When
	_, err := ParseFlow(input)

	// Then
	if !errors.Is(err, ErrInvalidPolicyFlow) {
		t.Fatalf("ParseFlow error = %v, want ErrInvalidPolicyFlow", err)
	}
}

func duplicateTraversal(input PolicyInput) PolicyInput {
	input.Groups[1].Rules[0].Action = ActionInput{Kind: ActionVia, Group: "inline-east"}
	return input
}
