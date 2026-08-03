package apply

import (
	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/trafficpolicy"
	"ly-route/backend/internal/runtime/vpp"
)

func productionGatewayPlans() (vpp.Plan, vpp.Plan, vpp.Snapshot) {
	prior := vpp.Plan{
		Interfaces: []vpp.InterfaceState{{Name: "if-unchanged", AdminState: "up"}, {Name: "if-changed", AdminState: "up", Addresses: []string{"192.0.2.2/24"}}, {Name: "if-removed", AdminState: "down"}},
		Bonds:      []vpp.BondState{{Name: "bond-unchanged", Mode: "xor", Members: []string{"if-unchanged"}}, {Name: "bond-changed", Mode: "xor", Members: []string{"if-changed"}}, {Name: "bond-removed", Mode: "xor", Members: []string{"if-removed"}}},
		Policy: trafficpolicy.Config{
			WANGroups:     []trafficpolicy.WANGroup{{ID: "wan-unchanged", Members: []string{"wan0"}}, {ID: "wan-changed", Members: []string{"wan1"}}, {ID: "wan-removed", Members: []string{"wan2"}}},
			RoutePolicies: []trafficpolicy.RoutePolicy{{ID: "route-unchanged", Action: "route", Egress: "wan-unchanged"}, {ID: "route-changed", Priority: 20, Action: "route", Egress: "wan-changed"}, {ID: "route-removed", Action: "deny"}},
			SecurityACLs:  []trafficpolicy.SecurityACL{{ID: "acl-unchanged", Action: "permit"}, {ID: "acl-changed", Action: "permit"}, {ID: "acl-removed", Action: "deny"}},
		},
		Flow: flow.CompiledIntent{VPPGroups: []flow.VPPObjectGroup{{Kind: "qos-unchanged", Objects: []flow.VPPObject{{RuleID: "unchanged"}}}, {Kind: "qos-changed", Objects: []flow.VPPObject{{RuleID: "changed", DSCP: "10"}}}, {Kind: "qos-removed", Objects: []flow.VPPObject{{RuleID: "removed"}}}}},
		NAT: nat.CompiledConfig{
			StaticMappings: []nat.StaticMapping{{ID: "nat-unchanged", ExternalAddress: "203.0.113.1", InternalAddress: "192.0.2.1"}, {ID: "nat-changed", ExternalAddress: "203.0.113.2", InternalAddress: "192.0.2.2"}, {ID: "nat-removed", ExternalAddress: "203.0.113.3", InternalAddress: "192.0.2.3"}},
			PortMappings:   []nat.PortMapping{{ID: "port-unchanged", Protocol: "tcp", ExternalAddress: "203.0.113.1", ExternalPort: 8001, InternalHost: "192.0.2.1", InternalPort: 80}, {ID: "port-changed", Protocol: "tcp", ExternalAddress: "203.0.113.2", ExternalPort: 8002, InternalHost: "192.0.2.2", InternalPort: 80}, {ID: "port-removed", Protocol: "udp", ExternalAddress: "203.0.113.3", ExternalPort: 8003, InternalHost: "192.0.2.3", InternalPort: 53}},
		},
	}
	desired := prior
	desired.Interfaces = []vpp.InterfaceState{prior.Interfaces[0], {Name: "if-changed", AdminState: "up", Addresses: []string{"192.0.2.22/24"}}, {Name: "if-new", AdminState: "up"}}
	desired.Bonds = []vpp.BondState{prior.Bonds[0], {Name: "bond-changed", Mode: "active-backup", Members: []string{"if-changed"}}, {Name: "bond-new", Mode: "xor", Members: []string{"if-new"}}}
	desired.Policy.WANGroups = []trafficpolicy.WANGroup{prior.Policy.WANGroups[0], {ID: "wan-changed", Members: []string{"wan3"}}, {ID: "wan-new", Members: []string{"wan4"}}}
	desired.Policy.RoutePolicies = []trafficpolicy.RoutePolicy{prior.Policy.RoutePolicies[0], {ID: "route-changed", Priority: 25, Action: "route", Egress: "wan-changed"}, {ID: "route-new", Action: "route", Egress: "wan-new"}}
	desired.Policy.SecurityACLs = []trafficpolicy.SecurityACL{prior.Policy.SecurityACLs[0], {ID: "acl-changed", Action: "deny"}, {ID: "acl-new", Action: "permit"}}
	desired.Flow.VPPGroups = []flow.VPPObjectGroup{prior.Flow.VPPGroups[0], {Kind: "qos-changed", Objects: []flow.VPPObject{{RuleID: "changed", DSCP: "46"}}}, {Kind: "qos-new", Objects: []flow.VPPObject{{RuleID: "new"}}}}
	desired.NAT.StaticMappings = []nat.StaticMapping{prior.NAT.StaticMappings[0], {ID: "nat-changed", ExternalAddress: "203.0.113.2", InternalAddress: "192.0.2.22"}, {ID: "nat-new", ExternalAddress: "203.0.113.4", InternalAddress: "192.0.2.4"}}
	desired.NAT.PortMappings = []nat.PortMapping{prior.NAT.PortMappings[0], {ID: "port-changed", Protocol: "tcp", ExternalAddress: "203.0.113.2", ExternalPort: 9002, InternalHost: "192.0.2.2", InternalPort: 80}, {ID: "port-new", Protocol: "tcp", ExternalAddress: "203.0.113.4", ExternalPort: 8004, InternalHost: "192.0.2.4", InternalPort: 80}}
	live := vpp.Snapshot{Interfaces: cloneSlice(prior.Interfaces), Bonds: cloneSlice(prior.Bonds), WANGroups: cloneSlice(prior.Policy.WANGroups), RoutePolicies: cloneSlice(prior.Policy.RoutePolicies), ACLs: cloneSlice(prior.Policy.SecurityACLs), QoS: cloneSlice(prior.Flow.VPPGroups), NAT: nat.CompiledConfig{StaticMappings: cloneSlice(prior.NAT.StaticMappings), PortMappings: cloneSlice(prior.NAT.PortMappings)}}
	return prior, desired, live
}

func unchangedGatewayPlan(prior vpp.Plan) vpp.Plan {
	prior.Interfaces = cloneSlice(prior.Interfaces[:1])
	prior.Bonds = cloneSlice(prior.Bonds[:1])
	prior.Policy.WANGroups = cloneSlice(prior.Policy.WANGroups[:1])
	prior.Policy.RoutePolicies = cloneSlice(prior.Policy.RoutePolicies[:1])
	prior.Policy.SecurityACLs = cloneSlice(prior.Policy.SecurityACLs[:1])
	prior.Flow.VPPGroups = cloneSlice(prior.Flow.VPPGroups[:1])
	prior.NAT.StaticMappings = cloneSlice(prior.NAT.StaticMappings[:1])
	prior.NAT.PortMappings = cloneSlice(prior.NAT.PortMappings[:1])
	return prior
}

func cloneSlice[T any](items []T) []T { return append([]T(nil), items...) }
