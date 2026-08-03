package orchestrator

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"
)

func TestPolicy_property_insertion_order_does_not_change_view_or_compilation(t *testing.T) {
	// Given
	baselineInput := validPolicyInput()
	baselinePolicy := mustParsePolicy(t, baselineInput)
	flow := mustParseFlow(t, FlowInput{SourceIP: "192.0.2.10", DestinationIP: "198.51.100.10", Protocol: ProtocolTCP, SourcePort: 50000, DestinationPort: 443})
	baselineView, err := json.Marshal(baselinePolicy.View())
	if err != nil {
		t.Fatalf("marshal baseline view: %v", err)
	}
	baselinePath := mustCompilePolicy(t, baselinePolicy, flow, Prelude{})

	// When / Then
	property := func(seed uint64) bool {
		input := clonePolicyInput(baselineInput)
		random := rand.New(rand.NewSource(int64(seed)))
		random.Shuffle(len(input.IPObjects), func(left, right int) {
			input.IPObjects[left], input.IPObjects[right] = input.IPObjects[right], input.IPObjects[left]
		})
		random.Shuffle(len(input.Groups), func(left, right int) {
			input.Groups[left], input.Groups[right] = input.Groups[right], input.Groups[left]
		})
		for index := range input.Groups {
			random.Shuffle(len(input.Groups[index].Rules), func(left, right int) {
				input.Groups[index].Rules[left], input.Groups[index].Rules[right] = input.Groups[index].Rules[right], input.Groups[index].Rules[left]
			})
		}
		policy, parseErr := ParsePolicy(validPolicyTopology(t), input)
		if parseErr != nil {
			return false
		}
		view, marshalErr := json.Marshal(policy.View())
		compiled, compileErr := CompilePolicy(policy, flow, Prelude{})
		return marshalErr == nil && compileErr == nil && string(view) == string(baselineView) && reflect.DeepEqual(compiled, baselinePath)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatalf("insertion-order property: %v", err)
	}
}

func TestPolicy_group_position_changes_group_precedence_only(t *testing.T) {
	// Given
	input := validPolicyInput()
	input.Groups[0].Rules = append(input.Groups[0].Rules,
		PolicyRuleInput{ID: "later-safety", Sequence: 20, Match: anyMatch(), Action: ActionInput{Kind: ActionDirect}},
	)
	flow := mustParseFlow(t, FlowInput{SourceIP: "192.0.2.10", DestinationIP: "198.51.100.10", Protocol: ProtocolTCP, SourcePort: 50000, DestinationPort: 443})
	before := mustCompilePolicy(t, mustParsePolicy(t, input), flow, Prelude{})
	input.Groups[0].Position, input.Groups[1].Position = input.Groups[1].Position, input.Groups[0].Position

	// When
	after := mustCompilePolicy(t, mustParsePolicy(t, input), flow, Prelude{})

	// Then
	if before.Decisions[0].PolicyGroup != "safety" || after.Decisions[0].PolicyGroup != "default" {
		t.Fatalf("group precedence before/after = %#v / %#v", before.Decisions, after.Decisions)
	}
	if decisionByGroup(before.Decisions, "safety").RuleID != decisionByGroup(after.Decisions, "safety").RuleID {
		t.Fatal("group reorder changed rule precedence")
	}
}

func TestPolicy_rule_sequence_changes_rule_precedence_only(t *testing.T) {
	// Given
	input := validPolicyInput()
	input.Groups[0].Rules = append(input.Groups[0].Rules,
		PolicyRuleInput{ID: "safety-direct", Sequence: 20, Match: anyMatch(), Action: ActionInput{Kind: ActionDirect}},
	)
	flow := mustParseFlow(t, FlowInput{SourceIP: "192.0.2.10", DestinationIP: "198.51.100.10", Protocol: ProtocolTCP, SourcePort: 50000, DestinationPort: 443})
	before := mustCompilePolicy(t, mustParsePolicy(t, input), flow, Prelude{})
	input.Groups[0].Rules[0].Sequence, input.Groups[0].Rules[1].Sequence = input.Groups[0].Rules[1].Sequence, input.Groups[0].Rules[0].Sequence

	// When
	after := mustCompilePolicy(t, mustParsePolicy(t, input), flow, Prelude{})

	// Then
	if before.Decisions[0].PolicyGroup != after.Decisions[0].PolicyGroup || before.Decisions[1].PolicyGroup != after.Decisions[1].PolicyGroup {
		t.Fatal("rule reorder changed group precedence")
	}
	if before.Decisions[0].RuleID == after.Decisions[0].RuleID {
		t.Fatal("rule sequence did not change first-match precedence")
	}
}

func decisionByGroup(decisions []GroupDecision, group string) GroupDecision {
	for _, decision := range decisions {
		if decision.PolicyGroup == group {
			return decision
		}
	}
	return GroupDecision{}
}
