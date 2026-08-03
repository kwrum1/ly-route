package vpp

import "ly-route/backend/internal/runtime/trafficpolicy"

func appendRoutesByID(existing []trafficpolicy.RoutePolicy, ids []string, prior []trafficpolicy.RoutePolicy) []trafficpolicy.RoutePolicy {
	result := append([]trafficpolicy.RoutePolicy(nil), existing...)
	seen := make(map[string]struct{}, len(result))
	for _, route := range result {
		seen[route.ID] = struct{}{}
	}
	for _, id := range ids {
		if _, found := seen[id]; found {
			continue
		}
		for _, route := range prior {
			if route.ID == id {
				result = append(result, route)
				seen[id] = struct{}{}
				break
			}
		}
	}
	return result
}

func appendWANGroupsByID(existing []trafficpolicy.WANGroup, ids []string, prior []trafficpolicy.WANGroup) []trafficpolicy.WANGroup {
	result := append([]trafficpolicy.WANGroup(nil), existing...)
	seen := make(map[string]struct{}, len(result))
	for _, group := range result {
		seen[group.ID] = struct{}{}
	}
	for _, id := range ids {
		if _, found := seen[id]; found {
			continue
		}
		for _, group := range prior {
			if group.ID == id {
				result = append(result, group)
				seen[id] = struct{}{}
				break
			}
		}
	}
	return result
}
