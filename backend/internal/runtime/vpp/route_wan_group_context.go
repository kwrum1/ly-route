package vpp

import "ly-route/backend/internal/runtime/trafficpolicy"

// WANGroupsContext is immutable route-compilation input containing current and desired groups.
type WANGroupsContext struct {
	current []trafficpolicy.WANGroup
	desired []trafficpolicy.WANGroup
}

func NewWANGroupsContext(current, desired []trafficpolicy.WANGroup) WANGroupsContext {
	return WANGroupsContext{
		current: append([]trafficpolicy.WANGroup(nil), current...),
		desired: append([]trafficpolicy.WANGroup(nil), desired...),
	}
}

func (context WANGroupsContext) RouteGroups() []trafficpolicy.WANGroup {
	groups := make(map[string]trafficpolicy.WANGroup, len(context.current)+len(context.desired))
	for _, group := range context.current {
		groups[group.ID] = group
	}
	for _, group := range context.desired {
		groups[group.ID] = group
	}
	result := make([]trafficpolicy.WANGroup, 0, len(groups))
	for _, group := range groups {
		result = append(result, group)
	}
	return result
}
