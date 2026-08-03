package vpp

import (
	"reflect"
	"testing"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestGatewayLifecycleCharacterization_compilers_emit_current_operation_sequence(t *testing.T) {
	// Given
	compiledNAT, err := nat.CompileConfig(
		[]map[string]any{{"id": "nat-main", "external_address": "203.0.113.10", "internal_address": "192.168.88.10", "wan_link": "wan0"}},
		[]map[string]any{{"id": "port-web", "protocol": "tcp", "external_address": "203.0.113.10", "external_port": 8443, "internal_host": "192.168.88.20", "internal_port": 443, "wan_link": "wan0"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	compiledPolicy, err := trafficpolicy.CompileConfig(
		[]map[string]any{{"id": "route-office", "priority": 10, "action": "route", "wan_group": "wan-primary", "match": map[string]any{"src_ip": "192.168.88.0/24"}}},
		[]map[string]any{{"id": "acl-guest", "priority": 20, "action": "deny", "match": map[string]any{"src_ip": "192.168.20.0/24", "dst_ip": "10.0.0.0/8"}}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	compiledPolicy.WANGroups, err = trafficpolicy.CompileWANGroups([]map[string]any{{"id": "wan-primary", "wan_members": []any{"wan0", "wan1"}}})
	if err != nil {
		t.Fatal(err)
	}
	compiledFlow, err := flow.CompileIntent(flow.NewIntent("gateway-qos", []flow.Rule{
		flow.NewRule("classify-voice", flow.RuleGranularity, flow.Classify("voice")),
	}))
	if err != nil {
		t.Fatal(err)
	}
	plan := provenPlan(Plan{
		RequestID:          "gateway-lifecycle",
		AddressAssignments: []AddressAssignment{{ID: "interface-lan", LinuxInterface: "eth1", VPPInterface: "lyroute-eth1", CIDR: "192.168.88.1/24", Role: "lan"}},
		Flow:               compiledFlow,
		NAT:                compiledNAT,
		Policy:             compiledPolicy,
	}, "eth1")

	// When
	operations, err := BuildOperations(plan)
	if err != nil {
		t.Fatal(err)
	}

	// Then
	type identity struct {
		name     string
		resource string
	}
	got := make([]identity, 0, len(operations))
	for _, operation := range operations {
		got = append(got, identity{name: operation.Name, resource: operation.Resource})
	}
	want := []identity{
		{name: "vpp.dataplane.attach", resource: "eth1"},
		{name: "vpp.interface.address", resource: "interface-lan"},
		{name: "vpp.management-lcp", resource: "management-network"},
		{name: "vpp.qos.classify", resource: "vpp.qos.classify"},
		{name: "vpp.qos.record", resource: "vpp.qos.record"},
		{name: "vpp.qos.store", resource: "vpp.qos.store"},
		{name: "vpp.qos.classify", resource: "classify-voice"},
		{name: "vpp.nat44-ed.static-mapping", resource: "nat-main"},
		{name: "vpp.nat44-ed.static-mapping", resource: "port-web"},
		{name: "vpp.pbr.next-hop-group", resource: "wan-primary"},
		{name: "vpp.route-policy", resource: "route-office"},
		{name: "vpp.security-acl", resource: "acl-guest"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operation identities = %#v, want %#v", got, want)
	}
	for _, operation := range operations {
		if len(operation.VPPCtlCommands) == 0 {
			t.Fatalf("operation %s/%s has no executable or readback commands", operation.Name, operation.Resource)
		}
	}
}
