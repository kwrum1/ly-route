package product

import "testing"

func TestProfileConfigResourcesSeparateProducts(t *testing.T) {
	// Given
	shared := []string{"interface", "management_network", "interface_bond", "object_group", "security_acl", "security_ip_mac_binding", "security_threat_intel", "security_attack_rule", "traffic_control"}
	gatewayOnly := []string{
		"system_mode", "wan_link", "wan_group", "route_policy", "nat_static", "port_map",
		"proxy_egress", "proxy_group", "proxy_node", "proxy_subscription",
		"dns_policy", "dns_upstream", "domain_ip_set", "dns_rule_update_rollback",
		"dhcp_lease", "dhcp_server", "dhcp_static_binding",
	}
	orchestratorOnly := []string{"orchestrator_policy", "orchestrator_topology", "orchestrator_service_chain_intent"}

	// When / Then
	for _, resourceType := range shared {
		if !Orchestrator().AllowsConfigResource(resourceType) {
			t.Errorf("Orchestrator disallows shared config resource %q", resourceType)
		}
	}
	for _, resourceType := range gatewayOnly {
		if Orchestrator().AllowsConfigResource(resourceType) {
			t.Errorf("Orchestrator allows Gateway-only config resource %q", resourceType)
		}
		if !Gateway().AllowsConfigResource(resourceType) {
			t.Errorf("Gateway disallows existing config resource %q", resourceType)
		}
	}
	for _, resourceType := range orchestratorOnly {
		if Gateway().AllowsConfigResource(resourceType) {
			t.Errorf("Gateway allows Orchestrator-only config resource %q", resourceType)
		}
		if !Orchestrator().AllowsConfigResource(resourceType) {
			t.Errorf("Orchestrator disallows product-owned config resource %q", resourceType)
		}
	}
	for _, resourceType := range []string{"", " ", "unknown", "firmware", "orchestrator_runtime_cache"} {
		if Gateway().AllowsConfigResource(resourceType) || Orchestrator().AllowsConfigResource(resourceType) {
			t.Errorf("unknown config resource %q must be rejected by both products", resourceType)
		}
	}
}
