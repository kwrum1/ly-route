package orchestrator

import "testing"

func validPolicyInput() PolicyInput {
	return PolicyInput{
		SchemaVersion: PolicySchemaVersion,
		IPObjects: []IPObjectInput{
			{ID: "office", Prefixes: []string{"192.0.2.0/24"}},
			{ID: "internet", Prefixes: []string{"0.0.0.0/0", "::/0"}},
		},
		Groups: []PolicyGroupInput{
			{ID: "safety", Position: 10, Rules: []PolicyRuleInput{{ID: "safety-east", Sequence: 10, Match: tcpMatch("office", "any", 443), Action: ActionInput{Kind: ActionVia, Group: "inline-east"}}}},
			{ID: "default", Position: 20, Rules: []PolicyRuleInput{{ID: "default-east", Sequence: 10, Match: anyMatch(), Action: ActionInput{Kind: ActionVia, Group: "inline-west"}}}},
		},
		Default: ActionInput{Kind: ActionDirect},
	}
}

func validPolicyTopology(t *testing.T) Topology {
	t.Helper()
	input := validTopologyInput()
	input.Groups = append(input.Groups, GroupInput{
		Name: "inline-west",
		Ports: []DirectedPortInput{
			{Interface: "eth6", Direction: DirectionLANFacing},
			{Interface: "eth7", Direction: DirectionWANFacing},
		},
	})
	topology, err := ParseTopology(input)
	if err != nil {
		t.Fatalf("ParseTopology: %v", err)
	}
	return topology
}

func anyMatch() PolicyMatchInput {
	return PolicyMatchInput{Sources: []string{"any"}, Destinations: []string{"any"}, Protocol: ProtocolAny}
}

func tcpMatch(source, destination string, port uint16) PolicyMatchInput {
	return PolicyMatchInput{
		Sources:          []string{source},
		Destinations:     []string{destination},
		Protocol:         ProtocolTCP,
		DestinationPorts: []PortRangeInput{{Start: port, End: port}},
	}
}

func mustParsePolicy(t *testing.T, input PolicyInput) Policy {
	t.Helper()
	policy, err := ParsePolicy(validPolicyTopology(t), input)
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	return policy
}

func mustParseFlow(t *testing.T, input FlowInput) PolicyFlow {
	t.Helper()
	flow, err := ParseFlow(input)
	if err != nil {
		t.Fatalf("ParseFlow: %v", err)
	}
	return flow
}

func mustParsePrelude(t *testing.T, input PreludeInput) Prelude {
	t.Helper()
	prelude, err := ParsePrelude(input)
	if err != nil {
		t.Fatalf("ParsePrelude: %v", err)
	}
	return prelude
}

func mustCompilePolicy(t *testing.T, policy Policy, flow PolicyFlow, prelude Prelude) CompiledPath {
	t.Helper()
	compiled, err := CompilePolicy(policy, flow, prelude)
	if err != nil {
		t.Fatalf("CompilePolicy: %v", err)
	}
	return compiled
}

func clonePolicyInput(input PolicyInput) PolicyInput {
	clone := input
	clone.IPObjects = make([]IPObjectInput, len(input.IPObjects))
	for index, object := range input.IPObjects {
		clone.IPObjects[index] = IPObjectInput{ID: object.ID, Prefixes: append([]string(nil), object.Prefixes...)}
	}
	clone.Groups = make([]PolicyGroupInput, len(input.Groups))
	for groupIndex, group := range input.Groups {
		clone.Groups[groupIndex] = PolicyGroupInput{ID: group.ID, Position: group.Position}
		if group.Rules == nil {
			continue
		}
		clone.Groups[groupIndex].Rules = make([]PolicyRuleInput, len(group.Rules))
		for ruleIndex, rule := range group.Rules {
			clone.Groups[groupIndex].Rules[ruleIndex] = rule
			clone.Groups[groupIndex].Rules[ruleIndex].Match.Sources = append([]string(nil), rule.Match.Sources...)
			clone.Groups[groupIndex].Rules[ruleIndex].Match.Destinations = append([]string(nil), rule.Match.Destinations...)
			clone.Groups[groupIndex].Rules[ruleIndex].Match.SourcePorts = append([]PortRangeInput(nil), rule.Match.SourcePorts...)
			clone.Groups[groupIndex].Rules[ruleIndex].Match.DestinationPorts = append([]PortRangeInput(nil), rule.Match.DestinationPorts...)
		}
	}
	return clone
}
