package vpp

import (
	"maps"
	"net/netip"
	"reflect"
	"slices"
	"strings"

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
		liveMatchesDesired: InterfaceStateMatchesDesired,
		repairInPlace: func(observed, wanted InterfaceState) (InterfaceState, bool) {
			if !interfaceAddressesMatchDesired(observed.Addresses, wanted.Addresses) {
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

// InterfaceStateMatchesDesired compares a verified live VPP interface with
// the static interface plan.  The PPPoE/IPv6-PD runtime owns dynamically
// learned IPv6 addresses and RA state, so those addresses may be present in a
// live snapshot even though they are not part of the static interface plan.
// IPv4 remains exact: an unexpected IPv4 address is configuration drift and
// must not be hidden by this exception.
func InterfaceStateMatchesDesired(observed, wanted InterfaceState) bool {
	return observed.Name == wanted.Name &&
		observed.AdminState == wanted.AdminState &&
		observed.LinkState == wanted.LinkState &&
		interfaceAddressesMatchDesired(observed.Addresses, wanted.Addresses)
}

// InterfaceStatesMatchDesired is the evidence-level slice comparison.  VPP
// readback order is not a configuration semantic, so compare by interface
// identity and apply the runtime IPv6 ownership rule per interface.
func InterfaceStatesMatchDesired(observed, wanted []InterfaceState) bool {
	if len(observed) != len(wanted) {
		return false
	}
	observedByName := make(map[string]InterfaceState, len(observed))
	for _, state := range observed {
		name := strings.TrimSpace(state.Name)
		if name == "" {
			return false
		}
		if _, exists := observedByName[name]; exists {
			return false
		}
		observedByName[name] = state
	}
	for _, state := range wanted {
		name := strings.TrimSpace(state.Name)
		live, exists := observedByName[name]
		if !exists || !InterfaceStateMatchesDesired(live, state) {
			return false
		}
	}
	return true
}

func interfaceAddressesMatchDesired(observed, wanted []string) bool {
	wantedAddresses := normalizedInterfaceAddresses(wanted)
	observedAddresses := normalizedInterfaceAddresses(observed)

	// Every statically requested address must still be present.
	for address := range wantedAddresses {
		if _, present := observedAddresses[address]; !present {
			return false
		}
	}

	// IPv4 is exclusively owned by the static gateway plan.  Any unexpected
	// IPv4 address is therefore drift.  IPv6 is runtime-owned when the plan has
	// no static IPv6 address (the normal delegated-prefix case).  If a static
	// IPv6 address is explicitly present, require the global IPv6 set to match
	// while still allowing link-local addresses created by VPP.
	staticIPv6 := false
	for address := range wantedAddresses {
		if parsed, ok := parseInterfacePrefix(address); ok && parsed.Addr().Is6() {
			staticIPv6 = true
			break
		}
	}
	for address := range observedAddresses {
		if _, expected := wantedAddresses[address]; expected {
			continue
		}
		parsed, ok := parseInterfacePrefix(address)
		if !ok {
			return false
		}
		if !parsed.Addr().Is6() {
			return false
		}
		if staticIPv6 && !parsed.Addr().IsLinkLocalUnicast() {
			return false
		}
	}
	return true
}

func normalizedInterfaceAddresses(addresses []string) map[string]struct{} {
	result := make(map[string]struct{}, len(addresses))
	for _, address := range addresses {
		normalized := strings.TrimSpace(address)
		if prefix, ok := parseInterfacePrefix(normalized); ok {
			normalized = prefix.String()
		}
		if normalized != "" {
			result[normalized] = struct{}{}
		}
	}
	return result
}

func parseInterfacePrefix(address string) (netip.Prefix, bool) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(address))
	return prefix, err == nil
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
			return left.ID == right.ID && left.ExternalAddress == right.ExternalAddress && left.InternalAddress == right.InternalAddress && left.WANInterface == right.WANInterface && left.WANNextHop == right.WANNextHop && left.ReturnPathGuard == right.ReturnPathGuard
		},
	}
}

func portMapContract() resourceContract[nat.PortMapping] {
	return resourceContract[nat.PortMapping]{
		kind:     "NAT44 port mapping",
		identity: func(mapping nat.PortMapping) string { return mapping.ID },
		equal: func(left, right nat.PortMapping) bool {
			return left.ID == right.ID && left.Protocol == right.Protocol && left.ExternalAddress == right.ExternalAddress && left.ExternalPort == right.ExternalPort && left.InternalHost == right.InternalHost && left.InternalPort == right.InternalPort && left.WANInterface == right.WANInterface && left.WANNextHop == right.WANNextHop && left.Hairpin == right.Hairpin && left.ReturnPathGuard == right.ReturnPathGuard
		},
	}
}

func equalPolicyMatch(left, right trafficpolicy.Match) bool {
	return slices.Equal(left.Sources, right.Sources) && slices.Equal(left.Destinations, right.Destinations) && slices.Equal(left.Protocols, right.Protocols) && slices.Equal(left.SourcePorts, right.SourcePorts) && slices.Equal(left.DestPorts, right.DestPorts) && left.Direction == right.Direction
}
