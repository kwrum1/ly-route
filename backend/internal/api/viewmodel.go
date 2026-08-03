package api

import (
	"strings"

	"ly-route/backend/internal/product"
	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/proxy"
)

const (
	ResourceKindProxyEgress = "egress"
	CapabilityAvailable     = "available"
	CapabilityDegraded      = "degraded"
)

type RuntimeCapability struct {
	Name      string
	Available bool
	Reason    string
}

type CapabilityState struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
}

type ProductProfile struct {
	Product      product.ID           `json:"product"`
	Capabilities []product.Capability `json:"capabilities"`
}

type ProxyEgressResource struct {
	ID             string                 `json:"id"`
	Kind           string                 `json:"kind"`
	Name           string                 `json:"name"`
	Enabled        bool                   `json:"enabled"`
	SemanticType   proxy.SemanticType     `json:"semantic_type"`
	DisplayList    string                 `json:"display_list,omitempty"`
	ProxyProfileID string                 `json:"proxy_profile_id,omitempty"`
	UnderlayWANID  string                 `json:"underlay_wan_id,omitempty"`
	RuntimeProfile proxy.RuntimeProfile   `json:"runtime_profile,omitempty"`
	CapturePath    proxy.CapturePath      `json:"capture_path,omitempty"`
	Engine         proxy.Engine           `json:"engine,omitempty"`
	Handoff        proxy.DataplaneHandoff `json:"handoff,omitempty"`
	Capabilities   []CapabilityState      `json:"capabilities,omitempty"`
}

type FlowIntentResource struct {
	ID           string             `json:"id"`
	Rules        []FlowRuleResource `json:"rules"`
	Capabilities []CapabilityState  `json:"capabilities,omitempty"`
}

type FlowRuleResource struct {
	ID          string               `json:"id"`
	Granularity flow.Granularity     `json:"granularity"`
	Actions     []FlowActionResource `json:"actions"`
}

type FlowActionResource struct {
	Kind         flow.ActionKind `json:"kind"`
	TrafficClass string          `json:"traffic_class,omitempty"`
	DSCP         string          `json:"dscp,omitempty"`
	Policer      *flow.Policer   `json:"policer,omitempty"`
}

func ProxyEgress(egress proxy.Egress, name string, enabled bool, capabilities ...RuntimeCapability) ProxyEgressResource {
	resource, err := ProxyEgressWANRow(egress, name, enabled, capabilities...)
	if err != nil {
		panic(err)
	}
	return resource
}

func ProxyEgressWANRow(egress proxy.Egress, name string, enabled bool, capabilities ...RuntimeCapability) (ProxyEgressResource, error) {
	row, err := egress.LogicalWANRow()
	if err != nil {
		return ProxyEgressResource{}, err
	}

	return ProxyEgressResource{
		ID:             row.ID,
		Kind:           ResourceKindProxyEgress,
		Name:           name,
		Enabled:        enabled,
		SemanticType:   row.SemanticType,
		DisplayList:    row.DisplayList,
		ProxyProfileID: string(row.RuntimeProfile),
		RuntimeProfile: row.RuntimeProfile,
		CapturePath:    row.CapturePath,
		Engine:         row.Engine,
		Handoff:        row.Handoff,
		Capabilities:   capabilityStates(capabilities),
	}, nil
}

func FlowIntent(intent flow.Intent, capabilities ...RuntimeCapability) FlowIntentResource {
	if err := flow.ValidateIntent(intent); err != nil {
		panic(err)
	}

	rules := make([]FlowRuleResource, 0, len(intent.Rules))
	for _, rule := range intent.Rules {
		actions := make([]FlowActionResource, 0, len(rule.Actions))
		for _, action := range rule.Actions {
			actions = append(actions, FlowActionResource{
				Kind:         action.Kind,
				TrafficClass: action.TrafficClass,
				DSCP:         action.DSCP,
				Policer:      action.Policer,
			})
		}
		rules = append(rules, FlowRuleResource{ID: rule.ID, Granularity: rule.Granularity, Actions: actions})
	}

	return FlowIntentResource{ID: intent.ID, Rules: rules, Capabilities: capabilityStates(capabilities)}
}

func capabilityStates(capabilities []RuntimeCapability) []CapabilityState {
	states := make([]CapabilityState, 0, len(capabilities))
	for _, capability := range capabilities {
		name := strings.TrimSpace(capability.Name)
		if name == "" {
			continue
		}

		state := CapabilityAvailable
		if !capability.Available {
			state = CapabilityDegraded
		}
		states = append(states, CapabilityState{
			Name:      name,
			Available: capability.Available,
			State:     state,
			Reason:    strings.TrimSpace(capability.Reason),
		})
	}

	return states
}
