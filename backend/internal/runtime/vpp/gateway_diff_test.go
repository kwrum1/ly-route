package vpp

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestGatewayDesiredLiveDiffClassifiesEveryResourceClass(t *testing.T) {
	// Given
	prior, desired, live := gatewayDiffFixture()
	tests := []struct {
		name    string
		changed func(GatewayDiff) int
		deleted func(GatewayDiff) []string
	}{
		{name: "interfaces", changed: func(diff GatewayDiff) int { return len(diff.Interfaces.Interfaces) }, deleted: func(diff GatewayDiff) []string { return diff.Interfaces.DeleteInterfaces }},
		{name: "bonds", changed: func(diff GatewayDiff) int { return len(diff.Bonds.Bonds) }, deleted: func(diff GatewayDiff) []string { return diff.Bonds.DeleteBonds }},
		{name: "wan groups", changed: func(diff GatewayDiff) int { return len(diff.WANGroups.WANGroups) }, deleted: func(diff GatewayDiff) []string { return diff.WANGroups.DeleteWANGroups }},
		{name: "routes", changed: func(diff GatewayDiff) int { return len(diff.Routes.Routes) }, deleted: func(diff GatewayDiff) []string { return diff.Routes.DeleteRoutes }},
		{name: "ACLs", changed: func(diff GatewayDiff) int { return len(diff.ACLs.ACLs) }, deleted: func(diff GatewayDiff) []string { return diff.ACLs.DeleteACLs }},
		{name: "QoS", changed: func(diff GatewayDiff) int { return len(diff.QoS.QoS) }, deleted: func(diff GatewayDiff) []string { return diff.QoS.DeleteQoS }},
		{name: "NAT44", changed: func(diff GatewayDiff) int { return len(diff.NAT44.StaticMappings) }, deleted: func(diff GatewayDiff) []string { return diff.NAT44.DeleteStaticMappings }},
		{name: "port maps", changed: func(diff GatewayDiff) int { return len(diff.PortMaps.PortMappings) }, deleted: func(diff GatewayDiff) []string { return diff.PortMaps.DeletePortMappings }},
	}

	// When
	diff, err := ReconcileGatewayPlan(GatewayReconciliationInput{TransactionID: "txn-diff", Prior: prior, Desired: desired, Live: live})

	// Then
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.changed(diff); got != 2 {
				t.Fatalf("changed/new count = %d, want 2", got)
			}
			if got := test.deleted(diff); !reflect.DeepEqual(got, []string{resourceID(test.name, "changed"), resourceID(test.name, "removed")}) {
				t.Fatalf("deletes = %#v, want changed then removed identities", got)
			}
		})
	}
}

func TestGatewayDesiredLiveDiffCarriesLANVPPIngressIntoRouteLifecycle(t *testing.T) {
	desired := Plan{AddressAssignments: []AddressAssignment{
		{ID: "lan", Role: "lan", LinuxInterface: "ens192", VPPInterface: "lyroute-ens192", CIDR: "192.168.50.1/24"},
		{ID: "wan", Role: "wan", LinuxInterface: "ens224", VPPInterface: "lyroute-ens224"},
	}}

	diff, err := ReconcileGatewayPlan(GatewayReconciliationInput{
		TransactionID: "txn-route-ingress",
		Desired:       desired,
	})
	if err != nil {
		t.Fatal(err)
	}
	if diff.Routes.IngressVPPInterface != "lyroute-ens192" {
		t.Fatalf("route ingress = %q, want LAN VPP interface", diff.Routes.IngressVPPInterface)
	}
	if !reflect.DeepEqual(diff.Routes.LocalDestinations, []string{"192.168.50.0/24"}) {
		t.Fatalf("route local destinations = %#v, want LAN prefix", diff.Routes.LocalDestinations)
	}
}

func TestGatewayReconcileDeletesRetiredRoutePolicyWithoutPriorSnapshot(t *testing.T) {
	active := trafficpolicy.RoutePolicy{ID: "route-active", Action: "route", Egress: "wan0"}
	desired := Plan{
		Policy:                trafficpolicy.Config{RoutePolicies: []trafficpolicy.RoutePolicy{active}},
		RetiredRoutePolicyIDs: []string{"route-disabled", "route-active", "route-disabled", ""},
	}

	diff, err := ReconcileGatewayPlan(GatewayReconciliationInput{
		TransactionID:       "txn-retired-route",
		Desired:             desired,
		RepairVerifiedDrift: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(diff.Routes.DeleteRoutes, []string{"route-disabled"}) {
		t.Fatalf("retired route deletes = %#v", diff.Routes.DeleteRoutes)
	}
}

func TestGatewayReconcileCarriesWANGroupContextIntoRetiredRouteDelete(t *testing.T) {
	group := trafficpolicy.WANGroup{ID: "wan-weighted", Members: []string{"wan-a", "wan-b"}}
	route := trafficpolicy.RoutePolicy{ID: "route-active", Priority: 50, Action: "nat", Egress: group.ID}
	prior := Plan{Policy: trafficpolicy.Config{
		RoutePolicies: []trafficpolicy.RoutePolicy{route},
		WANGroups:     []trafficpolicy.WANGroup{group},
	}}
	desired := prior
	desired.RetiredRoutePolicyIDs = []string{"route-disabled"}
	live := Snapshot{
		RoutePolicies: []trafficpolicy.RoutePolicy{route},
		WANGroups:     []trafficpolicy.WANGroup{group},
	}

	diff, err := ReconcileGatewayPlan(GatewayReconciliationInput{
		TransactionID:       "txn-retired-group-context",
		Prior:               prior,
		Desired:             desired,
		Live:                live,
		RepairVerifiedDrift: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(diff.Routes.DeleteRoutes, "route-disabled") {
		t.Fatalf("route deletes = %#v, want retired route", diff.Routes.DeleteRoutes)
	}
	groups := diff.Routes.WANGroupsContext.RouteGroups()
	if len(groups) != 1 || groups[0].ID != group.ID {
		t.Fatalf("route WAN-group context = %#v, want %q", groups, group.ID)
	}
}

func TestGatewayReconcileRebuildsRoutesThatReferenceChangedWANGroup(t *testing.T) {
	priorGroup := trafficpolicy.WANGroup{
		ID:      "wan-pool",
		Mode:    trafficpolicy.WANGroupWeighted,
		Members: []string{"wan-a", "wan-b"},
		Weights: map[string]int{"wan-a": 7, "wan-b": 3},
	}
	desiredGroup := priorGroup
	desiredGroup.Mode = trafficpolicy.WANGroupFiveTuple
	desiredGroup.Weights = map[string]int{"wan-a": 1, "wan-b": 1}
	route := trafficpolicy.RoutePolicy{ID: "route-via-pool", Priority: 50, Action: "nat", Egress: priorGroup.ID}
	prior := Plan{Policy: trafficpolicy.Config{
		WANGroups:     []trafficpolicy.WANGroup{priorGroup},
		RoutePolicies: []trafficpolicy.RoutePolicy{route},
	}}
	desired := Plan{Policy: trafficpolicy.Config{
		WANGroups:     []trafficpolicy.WANGroup{desiredGroup},
		RoutePolicies: []trafficpolicy.RoutePolicy{route},
	}}
	live := Snapshot{
		WANGroups:     []trafficpolicy.WANGroup{priorGroup},
		RoutePolicies: []trafficpolicy.RoutePolicy{route},
	}

	diff, err := ReconcileGatewayPlan(GatewayReconciliationInput{
		TransactionID:       "txn-wan-group-dependent-route",
		Prior:               prior,
		Desired:             desired,
		Live:                live,
		RepairVerifiedDrift: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(diff.WANGroups.DeleteWANGroups, []string{priorGroup.ID}) || len(diff.WANGroups.WANGroups) != 1 {
		t.Fatalf("WAN-group diff = %#v, want replace", diff.WANGroups)
	}
	if !slices.Equal(diff.Routes.DeleteRoutes, []string{route.ID}) || len(diff.Routes.Routes) != 1 || diff.Routes.Routes[0].ID != route.ID {
		t.Fatalf("route diff = %#v, want dependent delete and replay", diff.Routes)
	}
}

func TestGatewayVerifiedReconcileAlwaysRebuildsPPPoERuntimePath(t *testing.T) {
	route := trafficpolicy.RoutePolicy{
		ID:       "route-pppoe",
		Priority: 10,
		Action:   "route",
		Egress:   "wan-pppoe",
		Path: &trafficpolicy.WANPath{
			VPPInterface: "pppoe_session0",
			NextHop:      "10.67.0.1",
			RuntimeToken: "pppoe:2:10.67.0.10:10.67.0.1",
		},
	}
	plan := Plan{Policy: trafficpolicy.Config{RoutePolicies: []trafficpolicy.RoutePolicy{route}}}
	diff, err := ReconcileGatewayPlan(GatewayReconciliationInput{
		TransactionID:       "txn-pppoe-reconnect",
		Prior:               plan,
		Desired:             plan,
		Live:                Snapshot{RoutePolicies: []trafficpolicy.RoutePolicy{route}},
		RepairVerifiedDrift: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Routes.Routes) != 1 || diff.Routes.Routes[0].ID != route.ID {
		t.Fatalf("PPPoE route diff = %#v, want runtime-token reconciliation without a drift signal", diff.Routes.Routes)
	}
}

func TestGatewayDesiredLiveDiffTreatsLinkStateOnlyChangeAsChanged(t *testing.T) {
	// Given
	priorInterface := InterfaceState{Name: "if-link", AdminState: "up", LinkState: "down", Addresses: []string{"192.0.2.1/24"}}
	desiredInterface := priorInterface
	desiredInterface.LinkState = "up"
	input := GatewayReconciliationInput{
		TransactionID: "txn-link-state-diff",
		Prior:         Plan{Interfaces: []InterfaceState{priorInterface}},
		Desired:       Plan{Interfaces: []InterfaceState{desiredInterface}},
		Live:          Snapshot{Interfaces: []InterfaceState{priorInterface}},
	}

	// When
	diff, err := ReconcileGatewayPlan(input)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(diff.Interfaces.DeleteInterfaces, []string{priorInterface.Name}) || !reflect.DeepEqual(diff.Interfaces.Interfaces, []InterfaceState{desiredInterface}) {
		t.Fatalf("interface diff = %#v, want LinkState-only delete+apply", diff.Interfaces)
	}
}

func TestGatewayDesiredLiveDiffFailsClosedForIncompleteLiveState(t *testing.T) {
	// Given
	prior, desired, live := gatewayDiffFixture()
	tests := []struct {
		name   string
		remove func(*Snapshot)
	}{
		{name: "interfaces", remove: func(state *Snapshot) { state.Interfaces = state.Interfaces[1:] }},
		{name: "bonds", remove: func(state *Snapshot) { state.Bonds = state.Bonds[1:] }},
		{name: "wan groups", remove: func(state *Snapshot) { state.WANGroups = state.WANGroups[1:] }},
		{name: "routes", remove: func(state *Snapshot) { state.RoutePolicies = state.RoutePolicies[1:] }},
		{name: "ACLs", remove: func(state *Snapshot) { state.ACLs = state.ACLs[1:] }},
		{name: "QoS", remove: func(state *Snapshot) { state.QoS = state.QoS[1:] }},
		{name: "NAT44", remove: func(state *Snapshot) { state.NAT.StaticMappings = state.NAT.StaticMappings[1:] }},
		{name: "port maps", remove: func(state *Snapshot) { state.NAT.PortMappings = state.NAT.PortMappings[1:] }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			incomplete := live
			test.remove(&incomplete)

			// When
			_, err := ReconcileGatewayPlan(GatewayReconciliationInput{TransactionID: "txn-incomplete", Prior: prior, Desired: desired, Live: incomplete})

			// Then
			if !errors.Is(err, ErrGatewayLiveMismatch) {
				t.Fatalf("error = %v, want ErrGatewayLiveMismatch", err)
			}
		})
	}
}

func TestGatewayDesiredLiveDiffRepairsDriftOnlyAfterVerifiedReadback(t *testing.T) {
	prior := InterfaceState{Name: "wan0", AdminState: "up", LinkState: "up", Addresses: []string{"198.51.100.2/24"}}
	live := prior
	live.AdminState = "down"

	diff, err := ReconcileGatewayPlan(GatewayReconciliationInput{
		TransactionID: "txn-repair-verified-drift",
		Prior:         Plan{Interfaces: []InterfaceState{prior}}, Desired: Plan{Interfaces: []InterfaceState{prior}},
		Live: Snapshot{Interfaces: []InterfaceState{live}}, RepairVerifiedDrift: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	repair := prior
	repair.Addresses = nil
	if len(diff.Interfaces.DeleteInterfaces) != 0 || !reflect.DeepEqual(diff.Interfaces.Interfaces, []InterfaceState{repair}) {
		t.Fatalf("verified drift diff = %#v, want in-place admin repair", diff.Interfaces)
	}
}

func TestGatewayDesiredLiveDiffReplacesVerifiedInterfaceAddressDrift(t *testing.T) {
	prior := InterfaceState{Name: "wan0", AdminState: "up", LinkState: "up", Addresses: []string{"198.51.100.2/24"}}
	live := prior
	live.Addresses = []string{"203.0.113.2/24"}
	diff, err := ReconcileGatewayPlan(GatewayReconciliationInput{
		TransactionID: "txn-replace-address-drift",
		Prior:         Plan{Interfaces: []InterfaceState{prior}}, Desired: Plan{Interfaces: []InterfaceState{prior}},
		Live: Snapshot{Interfaces: []InterfaceState{live}}, RepairVerifiedDrift: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(diff.Interfaces.DeleteInterfaces, []string{"wan0"}) || !reflect.DeepEqual(diff.Interfaces.Interfaces, []InterfaceState{prior}) {
		t.Fatalf("verified address drift diff = %#v, want replace", diff.Interfaces)
	}
}

func TestGatewayDesiredLiveDiffKeepsRuntimeOwnedIPv6OnStaticInterface(t *testing.T) {
	static := InterfaceState{Name: "lyroute-ens192", AdminState: "up", LinkState: "up", Addresses: []string{"10.1.18.1/24"}}
	live := static
	live.Addresses = []string{"10.1.18.1/24", "2001:db8:100::1/64"}

	diff, err := ReconcileGatewayPlan(GatewayReconciliationInput{
		TransactionID:       "txn-runtime-ipv6",
		Prior:               Plan{Interfaces: []InterfaceState{static}},
		Desired:             Plan{Interfaces: []InterfaceState{static}},
		Live:                Snapshot{Interfaces: []InterfaceState{live}},
		RepairVerifiedDrift: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Interfaces.DeleteInterfaces) != 0 || len(diff.Interfaces.Interfaces) != 0 {
		t.Fatalf("runtime IPv6 was treated as static drift: %#v", diff.Interfaces)
	}
}

func TestInterfaceStateMatchesDesiredRejectsUnexpectedIPv4AndStaticIPv6Drift(t *testing.T) {
	wanted := InterfaceState{Name: "lan", AdminState: "up", LinkState: "up", Addresses: []string{"192.0.2.1/24"}}
	if InterfaceStateMatchesDesired(InterfaceState{Name: "lan", AdminState: "up", LinkState: "up", Addresses: []string{"192.0.2.1/24", "198.51.100.1/24"}}, wanted) {
		t.Fatal("unexpected IPv4 address was accepted")
	}
	wanted.Addresses = []string{"192.0.2.1/24", "2001:db8::1/64"}
	if InterfaceStateMatchesDesired(InterfaceState{Name: "lan", AdminState: "up", LinkState: "up", Addresses: []string{"192.0.2.1/24", "2001:db8::2/64"}}, wanted) {
		t.Fatal("unexpected static global IPv6 address was accepted")
	}
}

func TestGatewaySnapshotRequestCarriesPriorDesiredCandidateUnion(t *testing.T) {
	// Given
	prior, desired, _ := gatewayDiffFixture()

	// When
	request := GatewayDiffSnapshotRequest("txn-candidates", prior, desired)

	// Then
	if len(request.Interfaces) != 3 || len(request.Candidates.Interfaces) != 4 || len(request.NATStaticMappings) != 3 || len(request.Candidates.NATStaticMappings) != 4 {
		t.Fatalf("request = %#v, want required prior identities and prior/desired candidate unions", request)
	}
	if len(request.Capabilities) != 7 {
		t.Fatalf("capabilities = %#v, want all typed Gateway readbacks", request.Capabilities)
	}
}
