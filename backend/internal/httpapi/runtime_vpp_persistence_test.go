package httpapi

import (
	"testing"

	serviceRuntime "ly-route/backend/internal/runtime/service"
)

func TestRuntimeServiceArtifacts_typedGatewayPersistsVPPWithoutReapply(t *testing.T) {
	artifacts := []serviceRuntime.RenderedArtifact{
		serviceRuntime.NewArtifact(serviceRuntime.VPP, "/var/lib/ly-route/vpp/operations.json", `{"operations":[]}`, "restart"),
		serviceRuntime.NewArtifact(serviceRuntime.SmartDNS, "/etc/smartdns/conf.d/ly-route-active.conf", "server 1.1.1.1", "restart"),
	}

	result := runtimeServiceArtifacts(artifacts, true)
	if len(result) != len(artifacts)+1 {
		t.Fatalf("service artifacts = %d, want %d", len(result), len(artifacts)+1)
	}
	if result[0].Service != serviceRuntime.VPP || result[0].ReloadMode != serviceRuntime.ReloadModePersistOnly {
		t.Fatalf("typed VPP artifact = %#v", result[0])
	}
	if result[1].ReloadMode != "restart" {
		t.Fatalf("non-VPP artifact reload mode = %q, want restart", result[1].ReloadMode)
	}
	routing := artifactsForService(result, serviceRuntime.LinuxRouting)
	if len(routing) != 1 || routing[0].ReloadMode != "restart" || routing[0].Path != "/var/lib/ly-route/policy-routing/apply.sh" {
		t.Fatalf("Linux routing reconciliation artifact = %#v", routing)
	}
}
