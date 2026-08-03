package orchestrator

import (
	"fmt"
	"slices"
	"strings"
)

type StageKind string

const (
	StageSecurity       StageKind = "security"
	StageTrafficControl StageKind = "traffic_control"
	StageOrchestration  StageKind = "orchestration"
	StageDefault        StageKind = "default"
)

type PathExit string

const (
	PathExitLAN  PathExit = "lan"
	PathExitDrop PathExit = "drop"
)

type CompiledStage struct {
	Kind        StageKind  `json:"kind"`
	Reference   string     `json:"reference,omitempty"`
	PolicyGroup string     `json:"policy_group,omitempty"`
	RuleID      string     `json:"rule_id,omitempty"`
	Sequence    int        `json:"sequence,omitempty"`
	Action      ActionKind `json:"action,omitempty"`
}

type GroupDecision struct {
	PolicyGroup        string     `json:"policy_group"`
	RuleID             string     `json:"rule_id"`
	Sequence           int        `json:"sequence"`
	Action             ActionKind `json:"action"`
	OrchestrationGroup string     `json:"orchestration_group,omitempty"`
}

type CompiledPath struct {
	Stages    []CompiledStage `json:"stages"`
	Decisions []GroupDecision `json:"decisions"`
	Traversal []string        `json:"traversal"`
	Exit      PathExit        `json:"exit"`
}

func ParsePrelude(input PreludeInput) (Prelude, error) {
	prelude := Prelude{}
	if strings.TrimSpace(input.SecurityDrop) != "" {
		name, err := parsePolicyName(input.SecurityDrop)
		if err != nil {
			return Prelude{}, fmt.Errorf("%w: security drop", ErrInvalidPolicyPrelude)
		}
		prelude.securityDrop = name
	}
	seen := make(map[string]struct{}, len(input.TrafficControls))
	for _, raw := range input.TrafficControls {
		name, err := parsePolicyName(raw)
		if err != nil {
			return Prelude{}, fmt.Errorf("%w: traffic control", ErrInvalidPolicyPrelude)
		}
		if _, exists := seen[name.String()]; exists {
			return Prelude{}, fmt.Errorf("%w: duplicate traffic control %q", ErrInvalidPolicyPrelude, name)
		}
		seen[name.String()] = struct{}{}
		prelude.trafficControls = append(prelude.trafficControls, name)
	}
	slices.SortFunc(prelude.trafficControls, func(left, right policyName) int { return strings.Compare(left.String(), right.String()) })
	return prelude, nil
}

func CompilePolicy(policy Policy, flow PolicyFlow, prelude Prelude) (CompiledPath, error) {
	if !policy.parsed || !flow.parsed {
		return CompiledPath{}, ErrInvalidPolicyCompile
	}
	if prelude.securityDrop.String() != "" {
		return CompiledPath{
			Stages: []CompiledStage{{Kind: StageSecurity, Reference: prelude.securityDrop.String(), Action: ActionDrop}},
			Exit:   PathExitDrop,
		}, nil
	}
	compiled := CompiledPath{
		Stages:    make([]CompiledStage, 0, len(prelude.trafficControls)+len(policy.groups)+1),
		Decisions: make([]GroupDecision, 0, len(policy.groups)),
		Traversal: make([]string, 0, len(policy.groups)),
	}
	for _, control := range prelude.trafficControls {
		compiled.Stages = append(compiled.Stages, CompiledStage{Kind: StageTrafficControl, Reference: control.String()})
	}
	for _, group := range policy.groups {
		rule, matched := firstMatchingRule(group.rules, flow)
		if !matched {
			continue
		}
		decision := GroupDecision{PolicyGroup: group.id.String(), RuleID: rule.id.String(), Sequence: rule.sequence, Action: rule.action.kind}
		stage := CompiledStage{Kind: StageOrchestration, PolicyGroup: group.id.String(), RuleID: rule.id.String(), Sequence: rule.sequence, Action: rule.action.kind}
		switch rule.action.kind {
		case ActionVia:
			decision.OrchestrationGroup = rule.action.group.String()
			stage.Reference = rule.action.group.String()
			compiled.Traversal = append(compiled.Traversal, rule.action.group.String())
			compiled.Stages = append(compiled.Stages, stage)
		case ActionDirect:
			compiled.Stages = append(compiled.Stages, stage)
		case ActionDrop:
			compiled.Decisions = append(compiled.Decisions, decision)
			compiled.Stages = append(compiled.Stages, stage)
			compiled.Exit = PathExitDrop
			return compiled, nil
		}
		compiled.Decisions = append(compiled.Decisions, decision)
	}
	compiled.Stages = append(compiled.Stages, CompiledStage{Kind: StageDefault, Action: policy.defaultAction.kind})
	if policy.defaultAction.kind == ActionDrop {
		compiled.Exit = PathExitDrop
		return compiled, nil
	}
	compiled.Exit = PathExitLAN
	return compiled, nil
}

func firstMatchingRule(rules []policyRule, flow PolicyFlow) (policyRule, bool) {
	for _, rule := range rules {
		if rule.match.matches(flow) {
			return rule, true
		}
	}
	return policyRule{}, false
}

func (policy Policy) View() PolicyView {
	view := PolicyView{SchemaVersion: PolicySchemaVersion, Default: ActionInput{Kind: policy.defaultAction.kind}}
	view.IPObjects = make([]IPObjectView, 0, len(policy.ipObjects))
	for _, object := range policy.ipObjects {
		prefixes := make([]string, 0, len(object.prefixes))
		for _, prefix := range object.prefixes {
			prefixes = append(prefixes, prefix.String())
		}
		view.IPObjects = append(view.IPObjects, IPObjectView{ID: object.id.String(), Prefixes: prefixes})
	}
	view.Groups = make([]PolicyGroupView, 0, len(policy.groups))
	for _, group := range policy.groups {
		groupView := PolicyGroupView{ID: group.id.String(), Position: group.position, Rules: make([]PolicyRuleView, 0, len(group.rules))}
		for _, rule := range group.rules {
			groupView.Rules = append(groupView.Rules, rule.view())
		}
		view.Groups = append(view.Groups, groupView)
	}
	return view
}

func (rule policyRule) view() PolicyRuleView {
	match := PolicyMatchInput{Sources: rule.match.sources.view(), Destinations: rule.match.destinations.view(), Protocol: rule.match.protocol}
	for _, item := range rule.match.sourcePorts {
		match.SourcePorts = append(match.SourcePorts, PortRangeInput{Start: item.start, End: item.end})
	}
	for _, item := range rule.match.destinationPorts {
		match.DestinationPorts = append(match.DestinationPorts, PortRangeInput{Start: item.start, End: item.end})
	}
	action := ActionInput{Kind: rule.action.kind}
	if rule.action.kind == ActionVia {
		action.Group = rule.action.group.String()
	}
	return PolicyRuleView{ID: rule.id.String(), Sequence: rule.sequence, Match: match, Action: action}
}

func (selector ipSelector) view() []string {
	if selector.any {
		return []string{"any"}
	}
	values := make([]string, 0, len(selector.references))
	for _, reference := range selector.references {
		values = append(values, reference.String())
	}
	return values
}
