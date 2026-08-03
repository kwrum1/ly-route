package orchestrator

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestCompilePolicy_first_match_per_group_continues_across_groups(t *testing.T) {
	// Given
	input := validPolicyInput()
	input.Groups[0].Rules = []PolicyRuleInput{
		{ID: "fallback-direct", Sequence: 20, Match: anyMatch(), Action: ActionInput{Kind: ActionDirect}},
		{ID: "office-west", Sequence: 10, Match: tcpMatch("office", "any", 443), Action: ActionInput{Kind: ActionVia, Group: "inline-west"}},
	}
	input.Groups[1].Rules[0].Action = ActionInput{Kind: ActionVia, Group: "inline-east"}
	policy := mustParsePolicy(t, input)
	flow := mustParseFlow(t, FlowInput{SourceIP: "192.0.2.10", DestinationIP: "198.51.100.10", Protocol: ProtocolTCP, SourcePort: 50000, DestinationPort: 443})
	prelude := mustParsePrelude(t, PreludeInput{TrafficControls: []string{"limit-office"}})

	// When
	compiled := mustCompilePolicy(t, policy, flow, prelude)

	// Then
	wantDecisions := []GroupDecision{
		{PolicyGroup: "safety", RuleID: "office-west", Sequence: 10, Action: ActionVia, OrchestrationGroup: "inline-west"},
		{PolicyGroup: "default", RuleID: "default-east", Sequence: 10, Action: ActionVia, OrchestrationGroup: "inline-east"},
	}
	if !reflect.DeepEqual(compiled.Decisions, wantDecisions) {
		t.Fatalf("decisions = %#v, want %#v", compiled.Decisions, wantDecisions)
	}
	if !reflect.DeepEqual(compiled.Traversal, []string{"inline-west", "inline-east"}) {
		t.Fatalf("traversal = %#v, want west then east", compiled.Traversal)
	}
	if compiled.Exit != PathExitLAN {
		t.Fatalf("exit = %q, want LAN", compiled.Exit)
	}
	wantKinds := []StageKind{StageTrafficControl, StageOrchestration, StageOrchestration, StageDefault}
	if got := stageKinds(compiled.Stages); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("stage kinds = %#v, want %#v", got, wantKinds)
	}
}

func TestCompilePolicy_drop_is_terminal(t *testing.T) {
	// Given
	input := validPolicyInput()
	input.Groups[0].Rules[0].Action = ActionInput{Kind: ActionDrop}
	policy := mustParsePolicy(t, input)
	flow := mustParseFlow(t, FlowInput{SourceIP: "192.0.2.10", DestinationIP: "198.51.100.10", Protocol: ProtocolTCP, SourcePort: 50000, DestinationPort: 443})

	// When
	compiled := mustCompilePolicy(t, policy, flow, Prelude{})

	// Then
	if compiled.Exit != PathExitDrop || len(compiled.Decisions) != 1 || len(compiled.Traversal) != 0 {
		t.Fatalf("compiled drop = %#v", compiled)
	}
	if len(compiled.Stages) != 1 || compiled.Stages[0].Action != ActionDrop {
		t.Fatalf("drop stages = %#v, want one terminal drop", compiled.Stages)
	}
}

func TestCompilePolicy_security_drop_precedes_lower_phases(t *testing.T) {
	// Given
	policy := mustParsePolicy(t, validPolicyInput())
	flow := mustParseFlow(t, FlowInput{SourceIP: "192.0.2.10", DestinationIP: "198.51.100.10", Protocol: ProtocolTCP, SourcePort: 50000, DestinationPort: 443})
	prelude := mustParsePrelude(t, PreludeInput{SecurityDrop: "deny-malware", TrafficControls: []string{"limit-office"}})

	// When
	compiled := mustCompilePolicy(t, policy, flow, prelude)

	// Then
	want := []CompiledStage{{Kind: StageSecurity, Reference: "deny-malware", Action: ActionDrop}}
	if !reflect.DeepEqual(compiled.Stages, want) || compiled.Exit != PathExitDrop {
		t.Fatalf("compiled security drop = %#v, want terminal security stage", compiled)
	}
}

func TestCompilePolicy_no_match_uses_explicit_LAN_default(t *testing.T) {
	// Given
	input := validPolicyInput()
	for groupIndex := range input.Groups {
		input.Groups[groupIndex].Rules[0].Match = tcpMatch("office", "any", 443)
	}
	policy := mustParsePolicy(t, input)
	flow := mustParseFlow(t, FlowInput{SourceIP: "203.0.113.10", DestinationIP: "198.51.100.10", Protocol: ProtocolUDP, SourcePort: 50000, DestinationPort: 53})

	// When
	compiled := mustCompilePolicy(t, policy, flow, Prelude{})

	// Then
	wantStages := []CompiledStage{{Kind: StageDefault, Action: ActionDirect}}
	if !reflect.DeepEqual(compiled.Stages, wantStages) || compiled.Exit != PathExitLAN || len(compiled.Decisions) != 0 {
		t.Fatalf("compiled no-match = %#v, want explicit LAN default", compiled)
	}
}

func TestCompilePolicy_matches_Task19_fixture(t *testing.T) {
	// Given
	policy := mustParsePolicy(t, validPolicyInput())
	flow := mustParseFlow(t, FlowInput{SourceIP: "192.0.2.10", DestinationIP: "198.51.100.10", Protocol: ProtocolTCP, SourcePort: 50000, DestinationPort: 443})
	raw, err := os.ReadFile("testdata/task19-compiled-path.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var want CompiledPath
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}

	// When
	compiled := mustCompilePolicy(t, policy, flow, mustParsePrelude(t, PreludeInput{TrafficControls: []string{"limit-office"}}))

	// Then
	if !reflect.DeepEqual(compiled, want) {
		t.Fatalf("compiled path = %#v, want fixture %#v", compiled, want)
	}
}

func TestCompilePolicy_direct_is_recorded_without_traversal(t *testing.T) {
	// Given
	input := validPolicyInput()
	input.Groups[0].Rules[0].Action = ActionInput{Kind: ActionDirect}
	policy := mustParsePolicy(t, input)
	flow := mustParseFlow(t, FlowInput{SourceIP: "192.0.2.10", DestinationIP: "198.51.100.10", Protocol: ProtocolTCP, SourcePort: 50000, DestinationPort: 443})

	// When
	compiled := mustCompilePolicy(t, policy, flow, Prelude{})

	// Then
	wantKinds := []StageKind{StageOrchestration, StageOrchestration, StageDefault}
	if got := stageKinds(compiled.Stages); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("stage kinds = %#v, want direct, via, default stages", got)
	}
	if compiled.Stages[0].Action != ActionDirect || !reflect.DeepEqual(compiled.Traversal, []string{"inline-west"}) {
		t.Fatalf("compiled direct = %#v, want no traversal for first decision", compiled)
	}
}

func TestCompilePolicy_rejects_zero_domain_values(t *testing.T) {
	// Given
	policy := Policy{}
	flow := PolicyFlow{}

	// When
	_, err := CompilePolicy(policy, flow, Prelude{})

	// Then
	if !errors.Is(err, ErrInvalidPolicyCompile) {
		t.Fatalf("CompilePolicy error = %v, want ErrInvalidPolicyCompile", err)
	}
}

func TestParsePrefixesExpandsIPv4AndIPv6Ranges(t *testing.T) {
	t.Parallel()

	prefixes, err := parsePrefixes([]string{"192.168.1.10-192.168.1.20", "2001:db8::1-2001:db8::3"})
	if err != nil {
		t.Fatalf("parsePrefixes: %v", err)
	}
	got := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		got = append(got, prefix.String())
	}
	want := []string{"192.168.1.10/31", "192.168.1.12/30", "192.168.1.16/30", "192.168.1.20/32", "2001:db8::1/128", "2001:db8::2/127"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("range prefixes = %#v, want %#v", got, want)
	}
}

func stageKinds(stages []CompiledStage) []StageKind {
	kinds := make([]StageKind, 0, len(stages))
	for _, stage := range stages {
		kinds = append(kinds, stage.Kind)
	}
	return kinds
}
