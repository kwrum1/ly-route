package flow

import (
	"errors"
	"fmt"
	"strings"
)

type Granularity string

const (
	RuleGranularity  Granularity = "rule"
	ClassGranularity Granularity = "class"
)

type ActionKind string

const (
	ActionClassify ActionKind = "classify"
	ActionRemark   ActionKind = "remark"
	ActionPolicer  ActionKind = "policer"
	ActionDrop     ActionKind = "drop"
)

type Policer struct {
	RateBPS  uint64 `json:"rate_bps"`
	BurstBPS uint64 `json:"burst_bps"`
}

type Match struct {
	Sources      []string `json:"sources,omitempty"`
	Destinations []string `json:"destinations,omitempty"`
	Protocols    []string `json:"protocols,omitempty"`
	SourcePorts  []string `json:"source_ports,omitempty"`
	DestPorts    []string `json:"dest_ports,omitempty"`
	Direction    string   `json:"direction,omitempty"`
}

type RemarkBehavior struct {
	ProtectedClass   string `json:"protected_class,omitempty"`
	DownstreamPolicy string `json:"downstream_policy,omitempty"`
}

type Action struct {
	Kind           ActionKind      `json:"kind"`
	TrafficClass   string          `json:"traffic_class,omitempty"`
	DSCP           string          `json:"dscp,omitempty"`
	RemarkBehavior *RemarkBehavior `json:"remark_behavior,omitempty"`
	Policer        *Policer        `json:"policer,omitempty"`
}

func Classify(trafficClass string) Action {
	return Action{Kind: ActionClassify, TrafficClass: trafficClass}
}

func Remark(dscp string) Action {
	return Action{Kind: ActionRemark, DSCP: dscp}
}

func RemarkForProtectedClass(dscp, protectedClass string) Action {
	return RemarkWithBehavior(dscp, RemarkBehavior{ProtectedClass: protectedClass})
}

func RemarkForDownstreamPolicy(dscp, downstreamPolicy string) Action {
	return RemarkWithBehavior(dscp, RemarkBehavior{DownstreamPolicy: downstreamPolicy})
}

func RemarkWithBehavior(dscp string, behavior RemarkBehavior) Action {
	return Action{Kind: ActionRemark, DSCP: dscp, RemarkBehavior: &behavior}
}

func Police(rateBPS, burstBPS uint64) Action {
	return Action{Kind: ActionPolicer, Policer: &Policer{RateBPS: rateBPS, BurstBPS: burstBPS}}
}

type Rule struct {
	ID          string      `json:"id"`
	Granularity Granularity `json:"granularity"`
	Class       string      `json:"class,omitempty"`
	Match       Match       `json:"match,omitempty"`
	Actions     []Action    `json:"actions"`
}

func NewRule(id string, granularity Granularity, actions ...Action) Rule {
	return Rule{ID: id, Granularity: granularity, Actions: append([]Action(nil), actions...)}
}

func NewClassRule(id, trafficClass string, actions ...Action) Rule {
	return Rule{ID: id, Granularity: ClassGranularity, Class: trafficClass, Actions: append([]Action(nil), actions...)}
}

type Intent struct {
	ID    string `json:"id"`
	Rules []Rule `json:"rules"`
}

type Target struct {
	Kind           string          `json:"kind"`
	RuleID         string          `json:"rule_id"`
	Granularity    Granularity     `json:"granularity"`
	Action         ActionKind      `json:"action"`
	Class          string          `json:"class,omitempty"`
	DSCP           string          `json:"dscp,omitempty"`
	RemarkBehavior *RemarkBehavior `json:"remark_behavior,omitempty"`
	Policer        *Policer        `json:"policer,omitempty"`
	Match          Match           `json:"match,omitempty"`
	Attachments    []string        `json:"attachments,omitempty"`
	HitCount       *uint64         `json:"hit_count,omitempty"`
	HitCountState  string          `json:"hit_count_state,omitempty"`
}

type VPPObjectGroup struct {
	Kind    string      `json:"kind"`
	Objects []VPPObject `json:"objects"`
}

type VPPObject struct {
	Name           string          `json:"name"`
	RuleID         string          `json:"rule_id"`
	Granularity    Granularity     `json:"granularity"`
	Action         ActionKind      `json:"action"`
	Class          string          `json:"class,omitempty"`
	DSCP           string          `json:"dscp,omitempty"`
	RemarkBehavior *RemarkBehavior `json:"remark_behavior,omitempty"`
	Policer        *Policer        `json:"policer,omitempty"`
	Match          Match           `json:"match,omitempty"`
	Attachments    []string        `json:"attachments,omitempty"`
}

type CompiledIntent struct {
	ID        string           `json:"id"`
	Targets   []Target         `json:"targets"`
	VPPGroups []VPPObjectGroup `json:"vpp_groups"`
}

func Drop() Action {
	return Action{Kind: ActionDrop}
}

var ErrInvalidIntent = errors.New("invalid flow intent")

func NewIntent(id string, rules []Rule) Intent {
	return Intent{ID: id, Rules: append([]Rule(nil), rules...)}
}

func ValidateIntent(intent Intent) error {
	if strings.TrimSpace(intent.ID) == "" {
		return fmt.Errorf("%w: id is required", ErrInvalidIntent)
	}
	if len(intent.Rules) == 0 {
		return fmt.Errorf("%w: at least one rule is required", ErrInvalidIntent)
	}

	for ruleIndex, rule := range intent.Rules {
		if strings.TrimSpace(rule.ID) == "" {
			return fmt.Errorf("%w: rule %d id is required", ErrInvalidIntent, ruleIndex)
		}
		if rule.Granularity != RuleGranularity && rule.Granularity != ClassGranularity {
			return fmt.Errorf("%w: rule %q granularity must be %q or %q", ErrInvalidIntent, rule.ID, RuleGranularity, ClassGranularity)
		}
		if err := validateRuleGranularity(rule); err != nil {
			return err
		}
		if len(rule.Actions) == 0 {
			return fmt.Errorf("%w: rule %q must include at least one action", ErrInvalidIntent, rule.ID)
		}
		for actionIndex, action := range rule.Actions {
			if err := validateAction(rule.ID, actionIndex, action); err != nil {
				return err
			}
		}
	}

	return nil
}

func validateRuleGranularity(rule Rule) error {
	switch rule.Granularity {
	case RuleGranularity:
		if strings.TrimSpace(rule.Class) != "" {
			return fmt.Errorf("%w: rule %q rule granularity cannot include class", ErrInvalidIntent, rule.ID)
		}
	case ClassGranularity:
		if strings.TrimSpace(rule.Class) == "" {
			return fmt.Errorf("%w: rule %q class granularity requires class", ErrInvalidIntent, rule.ID)
		}
	}
	return nil
}

func CompileIntent(intent Intent) (CompiledIntent, error) {
	if err := ValidateIntent(intent); err != nil {
		return CompiledIntent{}, err
	}

	targets := make([]Target, 0, len(intent.Rules))
	groupObjects := map[string][]VPPObject{
		"vpp.acl.drop":       nil,
		"vpp.behavior.rate":  nil,
		"vpp.qos.classify":   nil,
		"vpp.qos.record":     nil,
		"vpp.qos.store":      nil,
		"vpp.qos.egress-map": nil,
		"vpp.qos.mark":       nil,
		"vpp.policer":        nil,
	}
	for _, rule := range intent.Rules {
		for _, action := range rule.Actions {
			match := normalizedMatch(rule.Match)
			attachments := directionAttachments(match.Direction)
			target := Target{RuleID: rule.ID, Granularity: rule.Granularity, Class: ruleClass(rule, action), Action: action.Kind}
			switch action.Kind {
			case ActionDrop:
				target.Match = match
				target.Attachments = attachments
				target.HitCountState = "unavailable"
				target.Kind = "vpp.acl.drop"
				groupObjects["vpp.acl.drop"] = append(groupObjects["vpp.acl.drop"], vppObject(intent.ID, rule, action, "vpp.acl.drop", "", nil, nil, match, attachments))
			case ActionClassify:
				target.Kind = "vpp.qos.classify"
				groupObjects["vpp.qos.classify"] = append(groupObjects["vpp.qos.classify"], vppObject(intent.ID, rule, action, "vpp.qos.classify", "", nil, nil, Match{}, nil))
				groupObjects["vpp.qos.record"] = append(groupObjects["vpp.qos.record"], vppObject(intent.ID, rule, action, "vpp.qos.record", "", nil, nil, Match{}, nil))
				groupObjects["vpp.qos.store"] = append(groupObjects["vpp.qos.store"], vppObject(intent.ID, rule, action, "vpp.qos.store", "", nil, nil, Match{}, nil))
			case ActionRemark:
				target.Kind = "vpp.qos.mark"
				target.DSCP = action.DSCP
				behavior := remarkBehavior(rule, action)
				target.RemarkBehavior = behavior
				groupObjects["vpp.qos.egress-map"] = append(groupObjects["vpp.qos.egress-map"], vppObject(intent.ID, rule, action, "vpp.qos.egress-map", action.DSCP, behavior, nil, Match{}, nil))
				groupObjects["vpp.qos.mark"] = append(groupObjects["vpp.qos.mark"], vppObject(intent.ID, rule, action, "vpp.qos.mark", action.DSCP, behavior, nil, Match{}, nil))
			case ActionPolicer:
				policer := *action.Policer
				target.Policer = &policer
				if hasExplicitMatch(rule.Match) {
					target.Kind = "vpp.behavior.rate"
					target.Match = match
					target.Attachments = attachments
					target.HitCountState = "unavailable"
					groupObjects["vpp.behavior.rate"] = append(groupObjects["vpp.behavior.rate"], vppObject(intent.ID, rule, action, "vpp.behavior.rate", "", nil, &policer, match, attachments))
				} else {
					target.Kind = "vpp.policer"
				}
				groupObjects["vpp.policer"] = append(groupObjects["vpp.policer"], vppObject(intent.ID, rule, action, "vpp.policer", "", nil, &policer, Match{}, nil))
			}
			targets = append(targets, target)
		}
	}

	return CompiledIntent{ID: intent.ID, Targets: targets, VPPGroups: vppObjectGroups(groupObjects)}, nil
}

func vppObjectGroups(groupObjects map[string][]VPPObject) []VPPObjectGroup {
	orderedKinds := []string{
		"vpp.acl.drop",
		"vpp.behavior.rate",
		"vpp.qos.classify",
		"vpp.qos.record",
		"vpp.qos.store",
		"vpp.qos.egress-map",
		"vpp.qos.mark",
		"vpp.policer",
	}

	groups := make([]VPPObjectGroup, 0, len(orderedKinds))
	for _, kind := range orderedKinds {
		if (kind == "vpp.acl.drop" || kind == "vpp.behavior.rate") && len(groupObjects[kind]) == 0 {
			continue
		}
		groups = append(groups, VPPObjectGroup{Kind: kind, Objects: groupObjects[kind]})
	}
	return groups
}

func vppObject(intentID string, rule Rule, action Action, kind, dscp string, behavior *RemarkBehavior, policer *Policer, match Match, attachments []string) VPPObject {
	object := VPPObject{
		Name:        strings.Join([]string{intentID, granularityScope(rule, action), strings.TrimPrefix(kind, "vpp.")}, "/"),
		RuleID:      rule.ID,
		Granularity: rule.Granularity,
		Action:      action.Kind,
		Class:       ruleClass(rule, action),
		DSCP:        dscp,
		Match:       match,
		Attachments: append([]string(nil), attachments...),
	}
	if behavior != nil {
		object.RemarkBehavior = copyRemarkBehavior(behavior)
	}
	if policer != nil {
		copied := *policer
		object.Policer = &copied
	}
	return object
}

func remarkBehavior(rule Rule, action Action) *RemarkBehavior {
	behavior := RemarkBehavior{}
	if action.RemarkBehavior != nil {
		behavior = *action.RemarkBehavior
	}
	if behavior.ProtectedClass == "" {
		behavior.ProtectedClass = ruleClass(rule, action)
	}
	if behavior.DownstreamPolicy == "" {
		behavior.DownstreamPolicy = rule.ID
	}
	return copyRemarkBehavior(&behavior)
}

func copyRemarkBehavior(behavior *RemarkBehavior) *RemarkBehavior {
	if behavior == nil {
		return nil
	}
	copied := *behavior
	return &copied
}

func granularityScope(rule Rule, action Action) string {
	switch rule.Granularity {
	case RuleGranularity:
		return strings.Join([]string{string(RuleGranularity), rule.ID}, "/")
	case ClassGranularity:
		class := ruleClass(rule, action)
		if class == "" {
			class = rule.ID
		}
		return strings.Join([]string{string(ClassGranularity), class}, "/")
	default:
		return rule.ID
	}
}

func ruleClass(rule Rule, action Action) string {
	if class := strings.TrimSpace(rule.Class); class != "" {
		return class
	}
	return strings.TrimSpace(action.TrafficClass)
}

func normalizedMatch(match Match) Match {
	normalized := Match{
		Sources:      normalizeList(match.Sources, "any"),
		Destinations: normalizeList(match.Destinations, "any"),
		Protocols:    normalizeList(match.Protocols, "any"),
		SourcePorts:  normalizeList(match.SourcePorts, "any"),
		DestPorts:    normalizeList(match.DestPorts, "any"),
		Direction:    strings.ToLower(strings.TrimSpace(match.Direction)),
	}
	if normalized.Direction == "" {
		normalized.Direction = "both"
	}
	return normalized
}

func hasExplicitMatch(match Match) bool {
	return len(match.Sources) > 0 || len(match.Destinations) > 0 || len(match.Protocols) > 0 || len(match.SourcePorts) > 0 || len(match.DestPorts) > 0 || strings.TrimSpace(match.Direction) != ""
}

func normalizeList(values []string, fallback string) []string {
	normalized := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) == 0 && fallback != "" {
		return []string{fallback}
	}
	return normalized
}

func directionAttachments(direction string) []string {
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "uplink", "lan_to_wan", "up":
		return []string{"input:host-$LY_ROUTE_LAN_INTERFACE"}
	case "downlink", "wan_to_lan", "down":
		return []string{"output:host-$LY_ROUTE_LAN_INTERFACE"}
	default:
		return []string{"input:host-$LY_ROUTE_LAN_INTERFACE", "output:host-$LY_ROUTE_LAN_INTERFACE"}
	}
}

func validateAction(ruleID string, actionIndex int, action Action) error {
	switch action.Kind {
	case ActionDrop:
		if action.TrafficClass != "" || action.DSCP != "" || action.RemarkBehavior != nil || action.Policer != nil {
			return fmt.Errorf("%w: rule %q action %d drop cannot include classify, remark, or policer fields", ErrInvalidIntent, ruleID, actionIndex)
		}
	case ActionClassify:
		if strings.TrimSpace(action.TrafficClass) == "" {
			return fmt.Errorf("%w: rule %q action %d classify traffic_class is required", ErrInvalidIntent, ruleID, actionIndex)
		}
		if action.DSCP != "" || action.RemarkBehavior != nil || action.Policer != nil {
			return fmt.Errorf("%w: rule %q action %d classify cannot include remark or policer fields", ErrInvalidIntent, ruleID, actionIndex)
		}
	case ActionRemark:
		if strings.TrimSpace(action.DSCP) == "" {
			return fmt.Errorf("%w: rule %q action %d remark dscp is required", ErrInvalidIntent, ruleID, actionIndex)
		}
		if err := validateRemarkBehavior(ruleID, actionIndex, action.RemarkBehavior); err != nil {
			return err
		}
		if action.TrafficClass != "" || action.Policer != nil {
			return fmt.Errorf("%w: rule %q action %d remark cannot include classify or policer fields", ErrInvalidIntent, ruleID, actionIndex)
		}
	case ActionPolicer:
		if action.Policer == nil {
			return fmt.Errorf("%w: rule %q action %d policer token bucket is required", ErrInvalidIntent, ruleID, actionIndex)
		}
		if action.Policer.RateBPS == 0 || action.Policer.BurstBPS == 0 {
			return fmt.Errorf("%w: rule %q action %d policer rate_bps and burst_bps must be positive", ErrInvalidIntent, ruleID, actionIndex)
		}
		if action.TrafficClass != "" || action.DSCP != "" || action.RemarkBehavior != nil {
			return fmt.Errorf("%w: rule %q action %d policer cannot include classify or remark fields", ErrInvalidIntent, ruleID, actionIndex)
		}
	default:
		return fmt.Errorf("%w: rule %q action %d kind %q is unsupported", ErrInvalidIntent, ruleID, actionIndex, action.Kind)
	}

	return nil
}

func validateRemarkBehavior(ruleID string, actionIndex int, behavior *RemarkBehavior) error {
	if behavior == nil {
		return nil
	}
	if strings.TrimSpace(behavior.ProtectedClass) != behavior.ProtectedClass || strings.TrimSpace(behavior.DownstreamPolicy) != behavior.DownstreamPolicy {
		return fmt.Errorf("%w: rule %q action %d remark behavior fields must be canonical", ErrInvalidIntent, ruleID, actionIndex)
	}
	if behavior.ProtectedClass == "" && behavior.DownstreamPolicy == "" {
		return fmt.Errorf("%w: rule %q action %d remark behavior requires protected_class or downstream_policy", ErrInvalidIntent, ruleID, actionIndex)
	}
	return nil
}
