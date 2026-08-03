package vpp

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/proxy"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestGatewayDeleteReadbackBuildsCommandsFromPriorLiveIdentities(t *testing.T) {
	// Given
	route := trafficpolicy.RoutePolicy{ID: "route-office", Action: "route", Egress: "wan-primary"}
	group := trafficpolicy.WANGroup{ID: "wan-primary", Members: []string{"wan0"}}
	acl := trafficpolicy.SecurityACL{ID: "acl-guest", Action: "deny"}
	qos := flow.VPPObjectGroup{Kind: "vpp.qos.mark", Objects: []flow.VPPObject{{RuleID: "voice", Action: flow.ActionRemark, DSCP: "46"}}}

	// When
	routeRequest := routeWANGroupSnapshotRequestForPlan(RouteWANGroupPlan{TransactionID: "txn-delete", Routes: []trafficpolicy.RoutePolicy{route}, WANGroups: []trafficpolicy.WANGroup{group}, DeleteRoutes: []string{route.ID}, DeleteWANGroups: []string{group.ID}})
	aclRequest := aclQoSSnapshotRequestForPlan(ACLQoSPlan{TransactionID: "txn-delete", ACLs: []trafficpolicy.SecurityACL{acl}, QoS: []flow.VPPObjectGroup{qos}, DeleteACLs: []string{acl.ID}, DeleteQoS: []string{qos.Kind}})

	// Then
	for name, operation := range map[string]Operation{
		"route": snapshotOperation(routeRequest, SnapshotCapabilityRoutePolicies),
		"wan":   snapshotOperation(routeRequest, SnapshotCapabilityWANGroups),
		"acl":   snapshotOperation(aclRequest, SnapshotCapabilityACLs),
		"qos":   snapshotOperation(aclRequest, SnapshotCapabilityQoS),
	} {
		if len(operation.VPPCtlCommands) == 0 {
			t.Fatalf("%s deletion emitted no live vppctl readback: %#v", name, operation)
		}
	}
}

func TestGatewayDeleteCommandsUseExactPriorCIDRAndNATTuples(t *testing.T) {
	// Given
	interfaceState := InterfaceState{Name: "lyroute-eth1", Addresses: []string{"192.0.2.2/24", "198.51.100.2/24"}}
	portMap := nat.PortMapping{ID: "web", Protocol: "tcp", InternalHost: "192.168.88.20", InternalPort: 443, ExternalAddress: "203.0.113.10", ExternalPort: 8443}

	// When
	interfaceOperations, interfaceErr := BuildInterfaceBondOperations(InterfaceBondPlan{TransactionID: "txn-identities", Interfaces: []InterfaceState{interfaceState}, DeleteInterfaces: []string{interfaceState.Name}})
	natOperations, natErr := BuildNAT44Operations(NAT44Plan{TransactionID: "txn-identities", ReadbackPortMappings: []nat.PortMapping{portMap}, DeletePortMappings: []string{portMap.ID}})

	// Then
	if interfaceErr != nil || natErr != nil {
		t.Fatalf("compile deletion commands: interface=%v nat=%v", interfaceErr, natErr)
	}
	interfaceTrace := operationCommands(interfaceOperations, interfaceState.Name)
	for _, cidr := range interfaceState.Addresses {
		if !strings.Contains(interfaceTrace, "set interface ip address "+interfaceState.Name+" "+cidr+" del") {
			t.Fatalf("interface delete trace lacks prior CIDR %q: %s", cidr, interfaceTrace)
		}
	}
	natTrace := operationCommands(natOperations, portMap.ID)
	wantNAT := "nat44 add static mapping tcp local 192.168.88.20 443 external 203.0.113.10 8443 del"
	if !strings.Contains(natTrace, wantNAT) {
		t.Fatalf("NAT delete trace = %q, want exact tuple %q", natTrace, wantNAT)
	}
}

func TestGatewayRouteAndSecurityACLDeletesDetachAndRemoveOwnedACLs(t *testing.T) {
	// Given
	route := trafficpolicy.RoutePolicy{ID: "route-office"}
	acl := trafficpolicy.SecurityACL{ID: "acl-guest", Match: trafficpolicy.Match{Direction: "input"}}

	// When
	routeOperations, routeErr := BuildRouteWANGroupOperations(RouteWANGroupPlan{TransactionID: "txn-cleanup", Routes: []trafficpolicy.RoutePolicy{route}, DeleteRoutes: []string{route.ID}})
	aclOperations, aclErr := BuildACLQoSOperations(ACLQoSPlan{TransactionID: "txn-cleanup", ACLs: []trafficpolicy.SecurityACL{acl}, DeleteACLs: []string{acl.ID}})

	// Then
	if routeErr != nil || aclErr != nil {
		t.Fatalf("compile cleanup commands: route=%v acl=%v", routeErr, aclErr)
	}
	routeTrace := operationCommands(routeOperations, route.ID)
	if !strings.Contains(routeTrace, "delete acl-plugin acl index") {
		t.Fatalf("route cleanup leaves generated ACL: %s", routeTrace)
	}
	aclTrace := operationCommands(aclOperations, acl.ID)
	if !strings.Contains(aclTrace, "set interface input acl") || !strings.Contains(aclTrace, "del") {
		t.Fatalf("security ACL cleanup does not detach before delete: %s", aclTrace)
	}
}

func TestGatewayCompiledOperationsAreAllTransactionOwnedOrTypedUnsupported(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	plan := Plan{
		RequestID: "txn-coverage",
		NativePath: NativePathRequest{ManagementInterface: "eth0", Now: now, Assignments: []NativeAssignment{{
			LinuxInterface: "eth1", Explicit: true, Proof: provenAFXDPProof(now),
		}}},
		Proxy: proxy.CompiledEgress{},
		Flow:  flow.CompiledIntent{Targets: []flow.Target{{Kind: "vpp.future.unsupported", RuleID: "future"}}},
	}
	plan.NativePath.ReportPrerequisites = []PrerequisiteResult{{Name: "native", Passed: true}}

	// When
	operations, err := BuildOperations(plan)

	// Then
	if err != nil {
		var unsupported interface {
			error
			UnsupportedOperation() string
		}
		if !reflect.ValueOf(err).IsValid() || !asUnsupportedOperation(err, &unsupported) {
			t.Fatalf("operation rejection is not typed: %T %v", err, err)
		}
		return
	}
	transactionOwned := map[string]bool{
		"vpp.interface.address": true, "vpp.interface-bond": true,
		"vpp.pbr.next-hop-group": true, "vpp.route-policy": true,
		"vpp.security-acl": true, "vpp.nat44-ed.static-mapping": true,
		"vpp.nat44-ed.port-map": true,
		"vpp.acl.drop":          true, "vpp.behavior.rate": true, "vpp.qos.classify": true,
		"vpp.qos.record": true, "vpp.qos.store": true, "vpp.qos.egress-map": true,
		"vpp.qos.mark": true, "vpp.policer": true,
	}
	for _, operation := range operations {
		if !transactionOwned[operation.Name] {
			t.Fatalf("compiled operation %q is neither transaction-owned nor rejected", operation.Name)
		}
	}
}

func operationCommands(operations []Operation, resource string) string {
	var commands []string
	for _, operation := range operations {
		if operation.Resource == resource {
			commands = append(commands, operation.VPPCtlCommands...)
		}
	}
	return strings.Join(commands, "\n")
}

func asUnsupportedOperation(err error, target *interface {
	error
	UnsupportedOperation() string
}) bool {
	for err != nil {
		if typed, ok := err.(interface {
			error
			UnsupportedOperation() string
		}); ok {
			*target = typed
			return true
		}
		type unwrapper interface{ Unwrap() error }
		wrapped, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = wrapped.Unwrap()
	}
	return false
}
