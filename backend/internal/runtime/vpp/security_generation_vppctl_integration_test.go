package vpp

import (
	"context"
	"os"
	"testing"
)

func TestSecurityGenerationVPPCTLIntegration(t *testing.T) {
	binary := os.Getenv("LY_ROUTE_SECURITY_GENERATION_VPPCTL")
	if binary == "" {
		t.Skip("LY_ROUTE_SECURITY_GENERATION_VPPCTL is not configured")
	}
	bindings := []SecurityMACIPACL{{Interface: "lyroute-lan0", Mode: "enforce", UnboundBehavior: "block", Bindings: []SecurityMACIPRule{
		{IP: "10.0.0.2", MAC: "02:00:00:00:00:02"},
		{IP: "10.0.0.9", MAC: "02:00:00:00:00:02"},
	}}}
	threats := []SecurityThreatList{{ID: "sec-integration-threat", Interface: "lyroute-lan0", Priority: 10, ListType: "blacklist", Direction: "input", Entries: []string{"10.0.0.9/32"}}}
	var attacks []SecurityAttackRule
	if os.Getenv("LY_ROUTE_SECURITY_GENERATION_PROFILE") == "ipv6" {
		bindings = nil
		threats = []SecurityThreatList{{ID: "sec-integration-threat-v6", Interface: "lyroute-lan0", Priority: 10, ListType: "blacklist", Direction: "input", Entries: []string{"2001:db8:0::9/128"}}}
	}
	if os.Getenv("LY_ROUTE_SECURITY_GENERATION_PROFILE") == "attack" {
		bindings = nil
		threats = nil
		attacks = []SecurityAttackRule{{ID: "integration-syn", Interface: "lyroute-lan0", AttackType: "syn_flood", ThresholdPPS: 5, BurstPackets: 2, EnforcementMode: "enforce", SourcePrefix: "10.0.0.0/24"}}
	}
	generation, err := CompileSecurityGeneration("integration", "lyroute-lan0", nil, bindings, threats, attacks)
	if err != nil {
		t.Fatal(err)
	}
	commands, err := securityGenerationCommands(generation)
	if err != nil {
		t.Fatal(err)
	}
	client := NewProductionVPPCTLClient(binary)
	channel, err := client.OpenChannel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	operation := Operation{Name: "vpp.security-generation", RequestID: "security-generation-integration", Resource: generation.ID, Payload: generation, VPPCtlCommands: commands}
	if _, err := channel.Do(context.Background(), operation); err != nil {
		t.Fatalf("apply security generation: %v", err)
	}
	action := os.Getenv("LY_ROUTE_SECURITY_GENERATION_ACTION")
	if action == "move" {
		replacement, compileErr := CompileSecurityGeneration("integration", "lyroute-wan0", nil, nil, []SecurityThreatList{{ID: "sec-moved-threat", Interface: "lyroute-wan0", Priority: 10, ListType: "blacklist", Direction: "output", Entries: []string{"10.0.0.0/24"}}}, nil)
		if compileErr != nil {
			t.Fatal(compileErr)
		}
		replacementCommands, commandErr := securityGenerationCommands(replacement)
		if commandErr != nil {
			t.Fatal(commandErr)
		}
		operation = Operation{Name: "vpp.security-generation", RequestID: "security-generation-move", Resource: replacement.ID, Payload: replacement, VPPCtlCommands: replacementCommands}
		if _, err := channel.Do(context.Background(), operation); err != nil {
			t.Fatalf("move security generation to changed interface: %v", err)
		}
		generation = replacement
	}
	if action == "fault" {
		failed, compileErr := CompileSecurityGeneration("integration", "lyroute-lan0", nil, nil, nil, []SecurityAttackRule{{ID: "invalid-update", Interface: "missing0", AttackType: "syn_flood", ThresholdPPS: 5, BurstPackets: 2, EnforcementMode: "enforce", SourcePrefix: "10.0.0.0/24"}})
		if compileErr != nil {
			t.Fatal(compileErr)
		}
		failedCommands, commandErr := securityGenerationCommands(failed)
		if commandErr != nil {
			t.Fatal(commandErr)
		}
		failedOperation := Operation{Name: "vpp.security-generation", RequestID: "security-generation-fault", Resource: failed.ID, Payload: failed, VPPCtlCommands: failedCommands}
		if _, applyErr := channel.Do(context.Background(), failedOperation); applyErr == nil {
			t.Fatal("invalid security update unexpectedly applied")
		}
	}
	if action == "delete" || action == "move" {
		operation.Name += ".rollback-delete"
		operation.Payload = generation
		if _, err := channel.Do(context.Background(), operation); err != nil {
			t.Fatalf("delete security generation: %v", err)
		}
	}
}
