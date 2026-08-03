package apply

import (
	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

func wanGroupNames(items []trafficpolicy.WANGroup) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.ID)
	}
	return names
}

func routeNames(items []trafficpolicy.RoutePolicy) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.ID)
	}
	return names
}

func aclNames(items []trafficpolicy.SecurityACL) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.ID)
	}
	return names
}

func qosNames(items []flow.VPPObjectGroup) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Kind)
	}
	return names
}

func staticMappingNames(items []nat.StaticMapping) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.ID)
	}
	return names
}

func portMappingNames(items []nat.PortMapping) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.ID)
	}
	return names
}
