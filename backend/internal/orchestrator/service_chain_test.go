package orchestrator

import (
	"errors"
	"reflect"
	"testing"
)

func TestCompileServiceChain_builds_symmetric_two_group_paths(t *testing.T) {
	// Given
	topology := serviceChainTopology(t, "group-a", "group-b")
	flow := mustParseFlow(t, FlowInput{SourceIP: "198.18.0.2", DestinationIP: "198.18.5.2", Protocol: ProtocolTCP, SourcePort: 41000, DestinationPort: 443})
	path := CompiledPath{Traversal: []string{"group-a", "group-b"}, Exit: PathExitLAN}
	bindings := []ServiceChainBindingInput{
		{Group: "group-b", WANFacingNextHop: "198.18.3.2", LANFacingNextHop: "198.18.4.2"},
		{Group: "group-a", WANFacingNextHop: "198.18.1.2", LANFacingNextHop: "198.18.2.2"},
	}

	// When
	chain, err := CompileServiceChain(topology, flow, path, bindings)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if got := serviceChainGroups(chain.Forward.Hops); !reflect.DeepEqual(got, []string{"group-a", "group-b"}) {
		t.Fatalf("forward groups = %#v", got)
	}
	if got := serviceChainGroups(chain.Reverse.Hops); !reflect.DeepEqual(got, []string{"group-b", "group-a"}) {
		t.Fatalf("reverse groups = %#v", got)
	}
	if chain.Forward.IngressInterface != "wan0" || chain.Forward.ExitInterface != "lan0" || chain.Reverse.IngressInterface != "lan0" || chain.Reverse.ExitInterface != "wan0" {
		t.Fatalf("chain edges = %#v / %#v", chain.Forward, chain.Reverse)
	}
	if chain.Forward.Hops[1].IngressInterface != "a-lan" || chain.Reverse.Hops[1].IngressInterface != "b-wan" {
		t.Fatalf("chain ingress progression = %#v / %#v", chain.Forward.Hops, chain.Reverse.Hops)
	}
	if chain.Reverse.Match.SourceIP != chain.Forward.Match.DestinationIP || chain.Reverse.Match.SourcePort != chain.Forward.Match.DestinationPort {
		t.Fatalf("reverse match = %#v, want swapped forward tuple", chain.Reverse.Match)
	}
}

func TestCompileServiceChain_is_deterministic_across_binding_order(t *testing.T) {
	// Given
	topology := serviceChainTopology(t, "group-a", "group-b")
	flow := mustParseFlow(t, FlowInput{SourceIP: "198.18.0.2", DestinationIP: "198.18.5.2", Protocol: ProtocolUDP, SourcePort: 53000, DestinationPort: 53})
	path := CompiledPath{Traversal: []string{"group-a", "group-b"}, Exit: PathExitLAN}
	bindings := []ServiceChainBindingInput{{Group: "group-a", WANFacingNextHop: "198.18.1.2", LANFacingNextHop: "198.18.2.2"}, {Group: "group-b", WANFacingNextHop: "198.18.3.2", LANFacingNextHop: "198.18.4.2"}}

	// When
	first, firstErr := CompileServiceChain(topology, flow, path, bindings)
	second, secondErr := CompileServiceChain(topology, flow, path, []ServiceChainBindingInput{bindings[1], bindings[0]})

	// Then
	if firstErr != nil || secondErr != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("deterministic compile = (%#v, %v) / (%#v, %v)", first, firstErr, second, secondErr)
	}
}

func TestCompileServiceChain_unmatched_path_is_direct(t *testing.T) {
	// Given
	topology := serviceChainTopology(t, "group-a")
	flow := mustParseFlow(t, FlowInput{SourceIP: "198.18.0.3", DestinationIP: "198.18.5.3", Protocol: ProtocolICMP})

	// When
	chain, err := CompileServiceChain(topology, flow, CompiledPath{Exit: PathExitLAN}, nil)

	// Then
	if err != nil || !chain.Direct || len(chain.Forward.Hops) != 0 || len(chain.Reverse.Hops) != 0 {
		t.Fatalf("direct chain = %#v, error = %v", chain, err)
	}
}

func TestCompileServiceChain_accepts_one_two_and_three_groups_without_loops(t *testing.T) {
	tests := [][]string{{"group-a"}, {"group-a", "group-b"}, {"group-a", "group-b", "group-c"}}
	for _, groups := range tests {
		t.Run(groups[len(groups)-1], func(t *testing.T) {
			// Given
			topology := serviceChainTopology(t, groups...)
			flow := mustParseFlow(t, FlowInput{SourceIP: "198.18.0.2", DestinationIP: "198.18.5.2", Protocol: ProtocolICMP})
			bindings := make([]ServiceChainBindingInput, 0, len(groups))
			for index, group := range groups {
				bindings = append(bindings, ServiceChainBindingInput{Group: group, WANFacingNextHop: "198.18." + string(rune('1'+index*2)) + ".2", LANFacingNextHop: "198.18." + string(rune('2'+index*2)) + ".2"})
			}

			// When
			chain, err := CompileServiceChain(topology, flow, CompiledPath{Traversal: groups, Exit: PathExitLAN}, bindings)

			// Then
			if err != nil || len(chain.Forward.Hops) != len(groups) || len(chain.Reverse.Hops) != len(groups) {
				t.Fatalf("chain = %#v, error = %v", chain, err)
			}
			if err := ValidateServiceChainState(chain); err != nil {
				t.Fatalf("compiled chain is not loop-safe: %v", err)
			}
		})
	}
}

func TestCompileServiceChain_rejects_invalid_chain_state(t *testing.T) {
	tests := []struct {
		name      string
		traversal []string
		bindings  []ServiceChainBindingInput
		wantErr   error
	}{
		{name: "duplicate hop", traversal: []string{"group-a", "group-a"}, bindings: []ServiceChainBindingInput{{Group: "group-a", WANFacingNextHop: "198.18.1.2", LANFacingNextHop: "198.18.2.2"}}, wantErr: ErrDuplicateServiceChainHop},
		{name: "missing return binding", traversal: []string{"group-a"}, bindings: []ServiceChainBindingInput{{Group: "group-a", WANFacingNextHop: "198.18.1.2"}}, wantErr: ErrMissingServiceChainReturn},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			topology := serviceChainTopology(t, "group-a")
			flow := mustParseFlow(t, FlowInput{SourceIP: "198.18.0.2", DestinationIP: "198.18.5.2", Protocol: ProtocolTCP, DestinationPort: 443})

			// When
			_, err := CompileServiceChain(topology, flow, CompiledPath{Traversal: test.traversal, Exit: PathExitLAN}, test.bindings)

			// Then
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestCompileServiceChain_rejects_hop_limit(t *testing.T) {
	// Given
	names := make([]string, MaxServiceChainHops+1)
	for index := range names {
		names[index] = "group-" + string(rune('a'+index))
	}
	path := CompiledPath{Traversal: names, Exit: PathExitLAN}
	flow := mustParseFlow(t, FlowInput{SourceIP: "198.18.0.2", DestinationIP: "198.18.5.2", Protocol: ProtocolICMP})

	// When
	_, err := CompileServiceChain(Topology{}, flow, path, nil)

	// Then
	if !errors.Is(err, ErrServiceChainHopLimit) {
		t.Fatalf("error = %v, want ErrServiceChainHopLimit", err)
	}
}

func TestCompileServiceChainWithHealth_bypassesFailedGroupsSymmetrically(t *testing.T) {
	topology := serviceChainTopology(t, "group-a", "group-b", "group-c")
	flow := mustParseFlow(t, FlowInput{SourceIP: "198.18.0.2", DestinationIP: "198.18.5.2", Protocol: ProtocolTCP, DestinationPort: 443})
	path := CompiledPath{Traversal: []string{"group-a", "group-b", "group-c"}, Exit: PathExitLAN}
	bindings := []ServiceChainBindingInput{
		{Group: "group-a", WANFacingNextHop: "198.18.1.2", LANFacingNextHop: "198.18.2.2"},
		{Group: "group-b", WANFacingNextHop: "198.18.3.2", LANFacingNextHop: "198.18.4.2"},
		{Group: "group-c", WANFacingNextHop: "198.18.5.2", LANFacingNextHop: "198.18.6.2"},
	}

	chain, err := CompileServiceChainWithHealth(topology, flow, path, bindings, map[string]bool{"group-b": true})
	if err != nil {
		t.Fatal(err)
	}
	if got := serviceChainGroups(chain.Forward.Hops); !reflect.DeepEqual(got, []string{"group-a", "group-c"}) {
		t.Fatalf("healthy forward groups = %#v", got)
	}
	if got := serviceChainGroups(chain.Reverse.Hops); !reflect.DeepEqual(got, []string{"group-c", "group-a"}) {
		t.Fatalf("healthy reverse groups = %#v", got)
	}
	if !reflect.DeepEqual(chain.BypassedGroups, []string{"group-b"}) {
		t.Fatalf("bypassed groups = %#v", chain.BypassedGroups)
	}
	if err := ValidateServiceChainState(chain); err != nil {
		t.Fatal(err)
	}
}

func TestCompileServiceChainWithHealth_allGroupsFailedDefaultsToDirect(t *testing.T) {
	topology := serviceChainTopology(t, "group-a")
	flow := mustParseFlow(t, FlowInput{SourceIP: "198.18.0.2", DestinationIP: "198.18.5.2", Protocol: ProtocolTCP, DestinationPort: 443})
	path := CompiledPath{Traversal: []string{"group-a"}, Exit: PathExitLAN}
	chain, err := CompileServiceChainWithHealth(topology, flow, path, []ServiceChainBindingInput{{Group: "group-a", WANFacingNextHop: "198.18.1.2", LANFacingNextHop: "198.18.2.2"}}, map[string]bool{"group-a": true})
	if err != nil {
		t.Fatal(err)
	}
	if !chain.Direct || len(chain.Forward.Hops) != 0 || len(chain.Reverse.Hops) != 0 || !reflect.DeepEqual(chain.BypassedGroups, []string{"group-a"}) {
		t.Fatalf("all-failed chain = %#v", chain)
	}
}

func serviceChainTopology(t *testing.T, groups ...string) Topology {
	t.Helper()
	input := TopologyInput{SchemaVersion: SchemaVersion, ManagementInterface: "mgmt0", Interfaces: []InterfaceInput{{Name: "lan", Role: RoleLAN, Port: "lan0"}, {Name: "wan", Role: RoleWAN, Port: "wan0"}}}
	for index, name := range groups {
		letter := string(rune('a' + index))
		input.Groups = append(input.Groups, GroupInput{Name: name, Ports: []DirectedPortInput{{Interface: letter + "-lan", Direction: DirectionLANFacing}, {Interface: letter + "-wan", Direction: DirectionWANFacing}}})
	}
	topology, err := ParseTopology(input)
	if err != nil {
		t.Fatal(err)
	}
	return topology
}

func serviceChainGroups(hops []ServiceChainHop) []string {
	groups := make([]string, 0, len(hops))
	for _, hop := range hops {
		groups = append(groups, hop.Group)
	}
	return groups
}
