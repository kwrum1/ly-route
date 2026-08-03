package vpp

import (
	"strings"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

func gatewayDiffFixture() (Plan, Plan, Snapshot) {
	prior := Plan{
		Interfaces: interfaceStates("old"),
		Bonds:      bondStates("old"),
		Policy: trafficpolicy.Config{
			WANGroups:     wanGroups("old"),
			RoutePolicies: routePolicies("old"),
			SecurityACLs:  securityACLs("old"),
		},
		Flow: flow.CompiledIntent{VPPGroups: qosGroups("old")},
		NAT: nat.CompiledConfig{
			StaticMappings: staticMappings("old"),
			PortMappings:   portMappings("old"),
		},
	}
	desired := Plan{
		Interfaces: interfaceStates("new"),
		Bonds:      bondStates("new"),
		Policy: trafficpolicy.Config{
			WANGroups:     wanGroups("new"),
			RoutePolicies: routePolicies("new"),
			SecurityACLs:  securityACLs("new"),
		},
		Flow: flow.CompiledIntent{VPPGroups: qosGroups("new")},
		NAT: nat.CompiledConfig{
			StaticMappings: staticMappings("new"),
			PortMappings:   portMappings("new"),
		},
	}
	live := Snapshot{
		Interfaces:    append([]InterfaceState(nil), prior.Interfaces...),
		Bonds:         append([]BondState(nil), prior.Bonds...),
		WANGroups:     append([]trafficpolicy.WANGroup(nil), prior.Policy.WANGroups...),
		RoutePolicies: append([]trafficpolicy.RoutePolicy(nil), prior.Policy.RoutePolicies...),
		ACLs:          append([]trafficpolicy.SecurityACL(nil), prior.Policy.SecurityACLs...),
		QoS:           append([]flow.VPPObjectGroup(nil), prior.Flow.VPPGroups...),
		NAT: nat.CompiledConfig{
			StaticMappings: append([]nat.StaticMapping(nil), prior.NAT.StaticMappings...),
			PortMappings:   append([]nat.PortMapping(nil), prior.NAT.PortMappings...),
		},
	}
	return prior, desired, live
}

func resourceID(resource, transition string) string {
	prefix := map[string]string{"interfaces": "if", "bonds": "bond", "wan groups": "wan", "routes": "route", "ACLs": "acl", "QoS": "qos", "NAT44": "nat", "port maps": "port"}[resource]
	return prefix + "-" + transition
}

func transitionIDs(prefix string) []string {
	return []string{prefix + "-unchanged", prefix + "-changed", prefix + "-removed"}
}

func desiredIDs(prefix string) []string {
	return []string{prefix + "-unchanged", prefix + "-changed", prefix + "-new"}
}

func interfaceStates(version string) []InterfaceState {
	ids := transitionIDs("if")
	if version == "new" {
		ids = desiredIDs("if")
	}
	return []InterfaceState{{Name: ids[0], AdminState: "up", LinkState: "up", Addresses: []string{"192.0.2.1/24"}}, {Name: ids[1], AdminState: "up", LinkState: "up", Addresses: []string{"192.0.2." + map[string]string{"old": "2", "new": "22"}[version] + "/24"}}, {Name: ids[2], AdminState: "down", LinkState: "down"}}
}

func bondStates(version string) []BondState {
	ids := transitionIDs("bond")
	if version == "new" {
		ids = desiredIDs("bond")
	}
	return []BondState{{Name: ids[0], Mode: "xor", Members: []string{"if-unchanged"}}, {Name: ids[1], Mode: map[string]string{"old": "xor", "new": "active-backup"}[version], Members: []string{"if-changed"}}, {Name: ids[2], Mode: "xor", Members: []string{"if-removed"}}}
}

func wanGroups(version string) []trafficpolicy.WANGroup {
	ids := transitionIDs("wan")
	if version == "new" {
		ids = desiredIDs("wan")
	}
	return []trafficpolicy.WANGroup{{ID: ids[0], Members: []string{"wan0"}}, {ID: ids[1], Members: []string{"wan1", map[string]string{"old": "wan2", "new": "wan3"}[version]}}, {ID: ids[2], Members: []string{"wan4"}}}
}

func routePolicies(version string) []trafficpolicy.RoutePolicy {
	ids := transitionIDs("route")
	if version == "new" {
		ids = desiredIDs("route")
	}
	return []trafficpolicy.RoutePolicy{{ID: ids[0], Priority: 10, Action: "route", Egress: "wan-unchanged"}, {ID: ids[1], Priority: map[string]int{"old": 20, "new": 25}[version], Action: "route", Egress: "wan-changed"}, {ID: ids[2], Priority: 30, Action: "deny"}}
}

func securityACLs(version string) []trafficpolicy.SecurityACL {
	ids := transitionIDs("acl")
	if version == "new" {
		ids = desiredIDs("acl")
	}
	return []trafficpolicy.SecurityACL{{ID: ids[0], Priority: 10, Action: "permit"}, {ID: ids[1], Priority: 20, Action: map[string]string{"old": "permit", "new": "deny"}[version]}, {ID: ids[2], Priority: 30, Action: "deny"}}
}

func qosGroups(version string) []flow.VPPObjectGroup {
	ids := transitionIDs("qos")
	if version == "new" {
		ids = desiredIDs("qos")
	}
	groups := make([]flow.VPPObjectGroup, 0, len(ids))
	for index, id := range ids {
		dscp := "10"
		if index == 1 && version == "new" {
			dscp = "46"
		}
		groups = append(groups, flow.VPPObjectGroup{Kind: id, Objects: []flow.VPPObject{{RuleID: strings.TrimPrefix(id, "qos-"), Action: flow.ActionRemark, DSCP: dscp}}})
	}
	return groups
}

func staticMappings(version string) []nat.StaticMapping {
	ids := transitionIDs("nat")
	if version == "new" {
		ids = desiredIDs("nat")
	}
	return []nat.StaticMapping{{ID: ids[0], ExternalAddress: "203.0.113.1", InternalAddress: "192.0.2.1"}, {ID: ids[1], ExternalAddress: "203.0.113.2", InternalAddress: map[string]string{"old": "192.0.2.2", "new": "192.0.2.22"}[version]}, {ID: ids[2], ExternalAddress: "203.0.113.3", InternalAddress: "192.0.2.3"}}
}

func portMappings(version string) []nat.PortMapping {
	ids := transitionIDs("port")
	if version == "new" {
		ids = desiredIDs("port")
	}
	return []nat.PortMapping{{ID: ids[0], Protocol: "tcp", ExternalAddress: "203.0.113.1", ExternalPort: 8001, InternalHost: "192.0.2.1", InternalPort: 80}, {ID: ids[1], Protocol: "tcp", ExternalAddress: "203.0.113.2", ExternalPort: map[string]int{"old": 8002, "new": 9002}[version], InternalHost: "192.0.2.2", InternalPort: 80}, {ID: ids[2], Protocol: "udp", ExternalAddress: "203.0.113.3", ExternalPort: 8003, InternalHost: "192.0.2.3", InternalPort: 53}}
}
