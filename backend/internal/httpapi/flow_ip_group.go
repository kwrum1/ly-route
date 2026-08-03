package httpapi

import (
	"context"
	"fmt"
	"strings"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

// expandFlowIntentAddressGroups keeps object-group IDs in desired configuration
// while compiling only canonical IPv4 selectors into the VPP runtime plan.
func (server *Server) expandFlowIntentAddressGroups(ctx context.Context, intent flow.Intent) (flow.Intent, error) {
	items, err := server.desiredItems(ctx, "object_group")
	if err != nil {
		return flow.Intent{}, err
	}
	expanded := intent
	expanded.Rules = append([]flow.Rule(nil), intent.Rules...)
	for index := range expanded.Rules {
		rule := &expanded.Rules[index]
		rule.Match.Sources, err = expandFlowAddressSelectors(rule.Match.Sources, items)
		if err != nil {
			return flow.Intent{}, fmt.Errorf("traffic-control rule %q sources: %w", rule.ID, err)
		}
		rule.Match.Destinations, err = expandFlowAddressSelectors(rule.Match.Destinations, items)
		if err != nil {
			return flow.Intent{}, fmt.Errorf("traffic-control rule %q destinations: %w", rule.ID, err)
		}
	}
	return expanded, nil
}

func expandFlowAddressSelectors(selectors []string, items []map[string]any) ([]string, error) {
	if len(selectors) == 0 {
		return nil, nil
	}
	expanded, err := trafficpolicy.ExpandAddressSelectors(selectors, items)
	if err != nil {
		return nil, err
	}
	for index, selector := range expanded {
		if strings.EqualFold(strings.TrimSpace(selector), "any") {
			expanded[index] = "0.0.0.0/0"
		}
	}
	return expanded, nil
}
