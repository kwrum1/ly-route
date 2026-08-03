package vpp

import (
	"strings"
	"testing"

	"ly-route/backend/internal/orchestrator"
)

func TestBuildServiceChainTransitionDeletesDesiredPoliciesForDirectBypass(t *testing.T) {
	desired := serviceChainFixture(t)
	active := desired
	active.Direct = true
	active.BypassedGroups = []string{"inline-a", "inline-b"}
	active.Forward.Hops = nil
	active.Reverse.Hops = nil
	attachments := serviceChainAttachments(t)
	deletes, adds, err := BuildServiceChainTransitionOperations("txn-bypass", desired, active, attachments)
	if err != nil {
		t.Fatal(err)
	}
	if len(deletes) != len(desired.Forward.Hops)+len(desired.Reverse.Hops) || len(adds) != 0 {
		t.Fatalf("delete/add operations = %d/%d", len(deletes), len(adds))
	}
	for _, operation := range deletes {
		commands := strings.Join(operation.VPPCtlCommands, "\n")
		if !strings.Contains(commands, "abf attach ip4 del policy") || !strings.Contains(commands, "abf policy del") || !strings.Contains(commands, "delete acl-plugin acl") || !strings.Contains(commands, "show abf policy") {
			t.Fatalf("delete commands = %s", commands)
		}
	}
}

func TestBuildServiceChainBypassDoesNotRequireCapabilityProof(t *testing.T) {
	operations, err := BuildServiceChainBypassOperations("txn-emergency-bypass", serviceChainFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 4 {
		t.Fatalf("bypass operations = %d", len(operations))
	}
	for _, operation := range operations {
		commands := strings.Join(operation.VPPCtlCommands, "\n")
		if !strings.Contains(commands, "lyroute-") || strings.Contains(commands, "abf policy add") {
			t.Fatalf("unsafe bypass commands = %s", commands)
		}
	}
}

func TestBuildServiceChainTransitionReinstallsOnlyHealthyPath(t *testing.T) {
	desired := serviceChainFixture(t)
	active := desired
	forward := desired.Forward.Hops[0]
	reverse := desired.Reverse.Hops[1]
	reverse.Position = 1
	reverse.IngressInterface = desired.Reverse.IngressInterface
	active.Forward.Hops = []orchestrator.ServiceChainHop{forward}
	active.Reverse.Hops = []orchestrator.ServiceChainHop{reverse}
	active.BypassedGroups = []string{"inline-b"}
	deletes, adds, err := BuildServiceChainTransitionOperations("txn-partial", desired, active, serviceChainAttachments(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(deletes) != 4 || len(adds) != 2 {
		t.Fatalf("delete/add operations = %d/%d", len(deletes), len(adds))
	}
	for _, operation := range adds {
		if operation.Payload.(ServiceChainPolicy).Group != "inline-a" {
			t.Fatalf("unhealthy group was reinstalled: %#v", operation.Payload)
		}
	}
}

func serviceChainFixture(t *testing.T) orchestrator.ServiceChain {
	t.Helper()
	topology, err := orchestrator.ParseTopology(orchestrator.TopologyInput{SchemaVersion: 1, ManagementInterface: "mgmt0", Interfaces: []orchestrator.InterfaceInput{{Name: "wan", Role: orchestrator.RoleWAN, Port: "wan0"}, {Name: "lan", Role: orchestrator.RoleLAN, Port: "lan0"}}, Groups: []orchestrator.GroupInput{
		{Name: "inline-a", Ports: []orchestrator.DirectedPortInput{{Interface: "a-lan", Direction: orchestrator.DirectionLANFacing}, {Interface: "a-wan", Direction: orchestrator.DirectionWANFacing}}},
		{Name: "inline-b", Ports: []orchestrator.DirectedPortInput{{Interface: "b-lan", Direction: orchestrator.DirectionLANFacing}, {Interface: "b-wan", Direction: orchestrator.DirectionWANFacing}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	flow, err := orchestrator.ParseFlow(orchestrator.FlowInput{SourceIP: "192.0.2.10", DestinationIP: "198.51.100.20", Protocol: orchestrator.ProtocolTCP, SourcePort: 41000, DestinationPort: 443})
	if err != nil {
		t.Fatal(err)
	}
	chain, err := orchestrator.CompileServiceChain(topology, flow, orchestrator.CompiledPath{Traversal: []string{"inline-a", "inline-b"}, Exit: orchestrator.PathExitLAN}, []orchestrator.ServiceChainBindingInput{{Group: "inline-a", WANFacingNextHop: "198.18.1.2", LANFacingNextHop: "198.18.2.2"}, {Group: "inline-b", WANFacingNextHop: "198.18.3.2", LANFacingNextHop: "198.18.4.2"}})
	if err != nil {
		t.Fatal(err)
	}
	return chain
}

func serviceChainAttachments(t *testing.T) []NativeAttachment {
	t.Helper()
	attachments := []NativeAttachment{}
	for _, name := range []string{"wan0", "lan0", "a-lan", "a-wan", "b-lan", "b-wan"} {
		attachments = append(attachments, proveNativeAttachment(NativeAttachment{LinuxInterface: name, VPPInterface: "vpp-" + name, Hook: NativeHookAFXDP, Mode: NativeModeZeroCopy}))
	}
	return attachments
}
