package httpapi

import (
	"context"
	"testing"

	"ly-route/backend/internal/product"
)

func TestOrchestratorRuntimePlanNeverReadsOrBuildsGatewayResources(t *testing.T) {
	server, _, _ := productTestServer(t, product.Orchestrator())

	plan, err := server.buildRuntimePlan(context.Background(), "orchestrator-runtime-plan")
	if err != nil {
		t.Fatalf("build Orchestrator runtime plan: %v", err)
	}
	if plan.ProxyEgress.ID != "" || len(plan.CompiledNAT.StaticMappings) != 0 || len(plan.CompiledNAT.PortMappings) != 0 || len(plan.DNSPolicies) != 0 || len(plan.DHCPServers) != 0 || len(plan.PPPoEPeers) != 0 || len(plan.NftablesCapture.Rules) != 0 || plan.LinuxPolicyRouting.EgressID != "" {
		t.Fatalf("Orchestrator runtime plan contains Gateway resources: %#v", plan)
	}
	for _, artifact := range plan.RuntimeArtifacts {
		switch string(artifact.Service) {
		case "smartdns", "kea", "xray", "pppoe", "pppd", "nftables", "linux-routing":
			t.Fatalf("Orchestrator runtime plan contains Gateway artifact %q", artifact.Service)
		}
	}
}
