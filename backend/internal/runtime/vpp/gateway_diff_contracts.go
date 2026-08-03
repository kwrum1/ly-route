package vpp

import (
	"maps"
	"reflect"
	"slices"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

func interfaceContract() resourceContract[InterfaceState] {
	return resourceContract[InterfaceState]{
		kind:     "interface",
		identity: func(state InterfaceState) string { return state.Name },
		equal: func(left, right InterfaceState) bool {
			return left.Name == right.Name && left.AdminState == right.AdminState && left.LinkState == right.LinkState && slices.Equal(left.Addresses, right.Addresses)
		},
		repairInPlace: func(observed, wanted InterfaceState) (InterfaceState, bool) {
			if !slices.Equal(observed.Addresses, wanted.Addresses) {
				return InterfaceState{}, false
			}
			// Admin-state drift can be repaired without removing the interface or
			// its LCP address. Omitting unchanged addresses also avoids relying on
			// duplicate-address CLI return codes; the final typed readback still
			// verifies the complete desired address set.
			wanted.Addresses = nil
			return wanted, true
		},
	}
}

func bondContract() resourceContract[BondState] {
	return resourceContract[BondState]{
		kind:     "bond",
		identity: func(state BondState) string { return state.Name },
		equal: func(left, right BondState) bool {
			return left.Name == right.Name && left.Mode == right.Mode && slices.Equal(left.Members, right.Members)
		},
	}
}

func wanGroupContract() resourceContract[trafficpolicy.WANGroup] {
	return resourceContract[trafficpolicy.WANGroup]{
		kind:     "WAN group",
		identity: func(group trafficpolicy.WANGroup) string { return group.ID },
		equal: func(left, right trafficpolicy.WANGroup) bool {
			return left.ID == right.ID && left.Mode == right.Mode && slices.Equal(left.Members, right.Members) && maps.Equal(left.Weights, right.Weights) && maps.Equal(left.Paths, right.Paths)
		},
	}
}

func routeContract() resourceContract[trafficpolicy.RoutePolicy] {
	return resourceContract[trafficpolicy.RoutePolicy]{
		kind:     "route policy",
		identity: func(route trafficpolicy.RoutePolicy) string { return route.ID },
		equal: func(left, right trafficpolicy.RoutePolicy) bool {
			return left.ID == right.ID && left.Priority == right.Priority && left.Action == right.Action && left.Egress == right.Egress && left.NextHop == right.NextHop && reflect.DeepEqual(left.Path, right.Path) && equalPolicyMatch(left.Match, right.Match)
		},
	}
}

func aclContract() resourceContract[trafficpolicy.SecurityACL] {
	return resourceContract[trafficpolicy.SecurityACL]{
		kind:     "ACL",
		identity: func(acl trafficpolicy.SecurityACL) string { return acl.ID },
		equal: func(left, right trafficpolicy.SecurityACL) bool {
			return left.ID == right.ID && left.Priority == right.Priority && left.Action == right.Action && equalPolicyMatch(left.Match, right.Match)
		},
	}
}

func qosContract() resourceContract[flow.VPPObjectGroup] {
	return resourceContract[flow.VPPObjectGroup]{
		kind:     "QoS group",
		identity: func(group flow.VPPObjectGroup) string { return group.Kind },
		equal: func(left, right flow.VPPObjectGroup) bool {
			return left.Kind == right.Kind && reflect.DeepEqual(left.Objects, right.Objects)
		},
	}
}

func natStaticContract() resourceContract[nat.StaticMapping] {
	return resourceContract[nat.StaticMapping]{
		kind:     "NAT44 static mapping",
		identity: func(mapping nat.StaticMapping) string { return mapping.ID },
		equal: func(left, right nat.StaticMapping) bool {
			return left.ID == right.ID && left.ExternalAddress == right.ExternalAddress && left.InternalAddress == right.InternalAddress && left.WANInterface == right.WANInterface
		},
	}
}

func portMapContract() resourceContract[nat.PortMapping] {
	return resourceContract[nat.PortMapping]{
		kind:     "NAT44 port mapping",
		identity: func(mapping nat.PortMapping) string { return mapping.ID },
		equal: func(left, right nat.PortMapping) bool {
			return left.ID == right.ID && left.Protocol == right.Protocol && left.ExternalAddress == right.ExternalAddress && left.ExternalPort == right.ExternalPort && left.InternalHost == right.InternalHost && left.InternalPort == right.InternalPort && left.WANInterface == right.WANInterface && left.Hairpin == right.Hairpin
		},
	}
}

func equalPolicyMatch(left, right trafficpolicy.Match) bool {
	return slices.Equal(left.Sources, right.Sources) && slices.Equal(left.Destinations, right.Destinations) && slices.Equal(left.Protocols, right.Protocols) && slices.Equal(left.SourcePorts, right.SourcePorts) && slices.Equal(left.DestPorts, right.DestPorts) && left.Direction == right.Direction
}
