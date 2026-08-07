package vpp

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/nat"
)

func TestNATReturnGuardUsesExternalAddressAndOriginalWANPath(t *testing.T) {
	guard := natReturnGuardForPortMapping(nat.PortMapping{
		ID:              "public-web",
		ExternalAddress: "10.67.0.11",
		WANInterface:    "pppoe_session0",
		WANNextHop:      "10.67.0.1",
	})
	prefix, err := guard.sourcePrefix()
	if err != nil {
		t.Fatal(err)
	}
	if prefix != "10.67.0.11/32" {
		t.Fatalf("source prefix = %q", prefix)
	}
	if guard.via() != "10.67.0.1 pppoe_session0" {
		t.Fatalf("return path = %q", guard.via())
	}
}

func TestNATSnapshotPreservesMappingAndMarksMissingReturnGuardAsDrift(t *testing.T) {
	t.Setenv("LY_ROUTE_LAN_VPP_INTERFACE", "lyroute-ens192")
	mapping := nat.PortMapping{ID: "public-web", Protocol: "tcp", ExternalAddress: "10.67.0.11", ExternalPort: 18080, InternalHost: "192.168.88.100", InternalPort: 18080, WANInterface: "pppoe_session0"}
	guard := natReturnGuardForPortMapping(mapping)
	request := SnapshotRequest{
		AllowMissing:          true,
		VerifyNATReturnGuards: true,
		NATPortMappings:       []string{mapping.ID},
		Candidates:            SnapshotCandidates{NATPortMappings: []nat.PortMapping{mapping}},
	}
	results := []VPPCTLCommandResult{
		{Command: "show nat44 static mappings", Stdout: "NAT44 static mappings:\n  TCP local 192.168.88.100:18080 external 10.67.0.11:18080 vrf 0\n"},
		{Command: "show acl-plugin acl", Stdout: ""},
		{Command: "show abf policy " + itoa(guard.policyID()), Stdout: "Invalid policy"},
		{Command: "show abf attach lyroute-ens192", Stdout: ""},
	}
	readback, err := decodeVPPCTLNAT44(request, results)
	if err != nil {
		t.Fatal(err)
	}
	if len(readback.PortMappings) != 1 || readback.PortMappings[0].ReturnPathGuard {
		t.Fatalf("missing guard readback = %#v, want mapping with guard=false", readback.PortMappings)
	}
}

func TestNATSnapshotFailsClosedWhenReturnGuardIsRequired(t *testing.T) {
	t.Setenv("LY_ROUTE_LAN_VPP_INTERFACE", "lyroute-ens192")
	mapping := nat.PortMapping{ID: "public-web", Protocol: "tcp", ExternalAddress: "10.67.0.11", ExternalPort: 18080, InternalHost: "192.168.88.100", InternalPort: 18080, WANInterface: "pppoe_session0"}
	guard := natReturnGuardForPortMapping(mapping)
	request := SnapshotRequest{
		VerifyNATReturnGuards: true,
		NATPortMappings:       []string{mapping.ID},
		Candidates:            SnapshotCandidates{NATPortMappings: []nat.PortMapping{mapping}},
	}
	results := []VPPCTLCommandResult{
		{Command: "show nat44 static mappings", Stdout: "NAT44 static mappings:\n  TCP local 192.168.88.100:18080 external 10.67.0.11:18080 vrf 0\n"},
		{Command: "show acl-plugin acl", Stdout: ""},
		{Command: "show abf policy " + itoa(guard.policyID()), Stdout: "Invalid policy"},
		{Command: "show abf attach lyroute-ens192", Stdout: ""},
	}
	_, err := decodeVPPCTLNAT44(request, results)
	if err == nil || !strings.Contains(err.Error(), "ACL is missing") {
		t.Fatalf("missing guard error = %v", err)
	}
}

func TestNATSnapshotTreatsMismatchedReturnGuardAsRepairableDrift(t *testing.T) {
	t.Setenv("LY_ROUTE_LAN_VPP_INTERFACE", "lyroute-ens192")
	mapping := nat.PortMapping{ID: "public-web", Protocol: "tcp", ExternalAddress: "10.67.0.11", ExternalPort: 18080, InternalHost: "192.168.88.100", InternalPort: 18080, WANInterface: "pppoe_session0", WANNextHop: "10.67.0.1"}
	guard := natReturnGuardForPortMapping(mapping)
	request := SnapshotRequest{
		AllowMissing:          true,
		VerifyNATReturnGuards: true,
		NATPortMappings:       []string{mapping.ID},
		Candidates:            SnapshotCandidates{NATPortMappings: []nat.PortMapping{mapping}},
	}
	results := []VPPCTLCommandResult{
		{Command: "show nat44 static mappings", Stdout: "NAT44 static mappings:\n  TCP local 192.168.88.100:18080 external 10.67.0.11:18080 vrf 0\n"},
		{Command: "show acl-plugin acl", Stdout: "acl-index 6 count 1 tag {" + guard.tag() + "}\n  0: ipv4 permit src 10.67.0.12/32 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535\n"},
		{Command: "show abf policy " + itoa(guard.policyID()), Stdout: "abf:[0]: policy:" + itoa(guard.policyID()) + " acl:6\n"},
		{Command: "show abf attach lyroute-ens192", Stdout: "policy:" + itoa(guard.policyID()) + "\n"},
	}

	readback, err := decodeVPPCTLNAT44(request, results)
	if err != nil {
		t.Fatal(err)
	}
	if len(readback.PortMappings) != 1 || readback.PortMappings[0].ReturnPathGuard {
		t.Fatalf("mismatched guard readback = %#v, want mapping with guard=false", readback.PortMappings)
	}
}

func itoa(value int) string {
	return fmt.Sprintf("%d", value)
}

func TestNATReturnGuardKeepsEthernetNextHop(t *testing.T) {
	guard := natReturnGuardForStaticMapping(nat.StaticMapping{
		ID:              "public-host",
		ExternalAddress: "203.0.113.10",
		WANInterface:    "GigabitEthernet0/1/0",
		WANNextHop:      "203.0.113.1",
	})
	if guard.via() != "203.0.113.1 GigabitEthernet0/1/0" {
		t.Fatalf("return path = %q", guard.via())
	}
}

func TestObservedABFDeleteViaParsesPPPoEAttachedNextHop(t *testing.T) {
	output := `abf:[6]: policy:22371 acl:6
     path-list:[124] locks:1 flags:shared,no-uRPF, uRPF-list: None
      path:[135] pl-index:124 ip4 weight=1 pref=0 attached-nexthop:  oper-flags:resolved,
        10.67.0.1 pppoe_session0 (p2p)
      [@0]: ipv4 [features] via 0.0.0.0 pppoe_session0: mtu:9000 next:6 flags:[features]`

	if got, want := observedABFDeleteVia(output), "10.67.0.1 pppoe_session0"; got != want {
		t.Fatalf("ABF delete path = %q, want %q", got, want)
	}
}

func TestNATMappingDeleteOperationsRetainPayloadForGuardCleanup(t *testing.T) {
	mapping := nat.PortMapping{ID: "public-web", Protocol: "tcp", ExternalAddress: "203.0.113.10", ExternalPort: 8443, InternalHost: "192.168.88.20", InternalPort: 443}
	operations, err := BuildNAT44Operations(NAT44Plan{
		TransactionID:        "txn-delete",
		DeletePortMappings:   []string{mapping.ID},
		ReadbackPortMappings: []nat.PortMapping{mapping},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 {
		t.Fatalf("operations = %#v", operations)
	}
	if got, ok := operations[0].Payload.(nat.PortMapping); !ok || got != mapping {
		t.Fatalf("delete payload = %#v", operations[0].Payload)
	}
	if !nat44MappingOperationDeletes(operations[0]) {
		t.Fatal("delete operation was classified as apply")
	}
}

func TestNATMappingApplyIsNotClassifiedAsDelete(t *testing.T) {
	mapping := nat.PortMapping{ID: "public-web", Protocol: "tcp", ExternalAddress: "203.0.113.10", ExternalPort: 8443, InternalHost: "192.168.88.20", InternalPort: 443}
	operations, err := BuildNAT44Operations(NAT44Plan{TransactionID: "txn-apply", PortMappings: []nat.PortMapping{mapping}})
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 || nat44MappingOperationDeletes(operations[0]) {
		t.Fatalf("apply operation classification = %#v", operations)
	}
	if !slices.Contains(operations[0].VPPCtlCommands, "?nat44 add static mapping tcp local 192.168.88.20 443 external 203.0.113.10 8443") {
		t.Fatalf("mapping commands = %#v", operations[0].VPPCtlCommands)
	}
}
