package vpp

import (
	"context"
	"os"
	"testing"

	"ly-route/backend/internal/orchestrator"
)

func TestVPPCTLServiceChainLifecycleIntegration(t *testing.T) {
	binary := os.Getenv("LY_ROUTE_VPPCTL_INTEGRATION_BINARY")
	if binary == "" {
		t.Skip("LY_ROUTE_VPPCTL_INTEGRATION_BINARY is not set")
	}
	chain := integrationRangeServiceChain(t)
	attachments := make([]NativeAttachment, 0, 4)
	for _, name := range []string{"wan0", "lan0", "a-wan", "a-lan"} {
		attachments = append(attachments, proveNativeAttachment(NativeAttachment{LinuxInterface: name, VPPInterface: "lyroute-" + name, Hook: NativeHookAFXDP, Mode: NativeModeZeroCopy}))
	}
	adapter := Adapter{Client: NewVPPCTLClient(binary)}
	ctx := context.Background()
	switch os.Getenv("LY_ROUTE_SERVICE_CHAIN_ACTION") {
	case "apply":
		for range 2 {
			result, err := adapter.ApplyServiceChain(ctx, "integration-apply", chain, attachments)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Readback.Policies) != 2 || len(result.Readback.Interfaces) != 2 {
				t.Fatalf("incomplete apply readback: %#v", result.Readback)
			}
		}
	case "bypass":
		for range 2 {
			if _, err := adapter.ApplyServiceChainBypass(ctx, "integration-bypass", chain); err != nil {
				t.Fatal(err)
			}
		}
	default:
		t.Fatal("LY_ROUTE_SERVICE_CHAIN_ACTION must be apply or bypass")
	}
}

func integrationRangeServiceChain(t *testing.T) orchestrator.ServiceChain {
	t.Helper()
	topology, err := orchestrator.ParseTopology(orchestrator.TopologyInput{
		SchemaVersion: orchestrator.SchemaVersion, ManagementInterface: "mgmt0",
		Interfaces: []orchestrator.InterfaceInput{
			{Name: "lan", Role: orchestrator.RoleLAN, Port: "lan0"},
			{Name: "wan", Role: orchestrator.RoleWAN, Port: "wan0"},
		},
		Groups: []orchestrator.GroupInput{{Name: "inline-a", Ports: []orchestrator.DirectedPortInput{
			{Interface: "a-lan", Direction: orchestrator.DirectionLANFacing},
			{Interface: "a-wan", Direction: orchestrator.DirectionWANFacing},
		}}},
	})
	if err != nil {
		t.Fatalf("parse integration topology: %v", err)
	}
	policy, err := orchestrator.ParsePolicy(topology, orchestrator.PolicyInput{
		SchemaVersion: orchestrator.PolicySchemaVersion,
		IPObjects: []orchestrator.IPObjectInput{
			{ID: "client-range", Prefixes: []string{"10.0.0.1-10.0.0.10"}},
			{ID: "server", Prefixes: []string{"10.0.1.2"}},
		},
		Groups: []orchestrator.PolicyGroupInput{{ID: "service", Position: 10, Rules: []orchestrator.PolicyRuleInput{{
			ID: "range-via", Sequence: 10,
			Match:  orchestrator.PolicyMatchInput{Sources: []string{"client-range"}, Destinations: []string{"server"}, Protocol: orchestrator.ProtocolICMP},
			Action: orchestrator.ActionInput{Kind: orchestrator.ActionVia, Group: "inline-a"},
		}}}},
		Default: orchestrator.ActionInput{Kind: orchestrator.ActionDirect},
	})
	if err != nil {
		t.Fatalf("parse range policy: %v", err)
	}
	flow, err := orchestrator.ParseFlow(orchestrator.FlowInput{SourceIP: "10.0.0.2", DestinationIP: "10.0.1.2", Protocol: orchestrator.ProtocolICMP})
	if err != nil {
		t.Fatalf("parse integration flow: %v", err)
	}
	path, err := orchestrator.CompilePolicy(policy, flow, orchestrator.Prelude{})
	if err != nil {
		t.Fatalf("compile range policy: %v", err)
	}
	chain, err := orchestrator.CompileServiceChain(topology, flow, path, []orchestrator.ServiceChainBindingInput{{Group: "inline-a", WANFacingNextHop: "198.18.1.2", LANFacingNextHop: "198.18.2.2"}})
	if err != nil {
		t.Fatalf("compile integration service chain: %v", err)
	}
	chain.ID = "integration-chain"
	if chain.Direct || len(chain.Forward.Hops) != 1 || chain.Forward.Match.SourceIP != "10.0.0.2" {
		t.Fatalf("range policy did not produce expected service chain: %#v", chain)
	}
	return chain
}
