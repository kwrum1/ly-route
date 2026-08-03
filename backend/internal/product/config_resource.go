package product

import "strings"

type ConfigResource string

const (
	ConfigResourceInterface           ConfigResource = "interface"
	ConfigResourceManagementNetwork   ConfigResource = "management_network"
	ConfigResourceInterfaceBond       ConfigResource = "interface_bond"
	ConfigResourceObjectGroup         ConfigResource = "object_group"
	ConfigResourceSecurityACL         ConfigResource = "security_acl"
	ConfigResourceSecurityBinding     ConfigResource = "security_ip_mac_binding"
	ConfigResourceSecurityThreatIntel ConfigResource = "security_threat_intel"
	ConfigResourceSecurityAttackRule  ConfigResource = "security_attack_rule"
	ConfigResourceTrafficControl      ConfigResource = "traffic_control"

	ConfigResourceSystemMode            ConfigResource = "system_mode"
	ConfigResourceWANLink               ConfigResource = "wan_link"
	ConfigResourceWANGroup              ConfigResource = "wan_group"
	ConfigResourceRoutePolicy           ConfigResource = "route_policy"
	ConfigResourceNATStatic             ConfigResource = "nat_static"
	ConfigResourcePortMap               ConfigResource = "port_map"
	ConfigResourceProxyEgress           ConfigResource = "proxy_egress"
	ConfigResourceProxyGroup            ConfigResource = "proxy_group"
	ConfigResourceProxyNode             ConfigResource = "proxy_node"
	ConfigResourceProxySubscription     ConfigResource = "proxy_subscription"
	ConfigResourceDNSPolicy             ConfigResource = "dns_policy"
	ConfigResourceDNSUpstream           ConfigResource = "dns_upstream"
	ConfigResourceDNSDomainIPSet        ConfigResource = "domain_ip_set"
	ConfigResourceDNSRuleUpdateRollback ConfigResource = "dns_rule_update_rollback"
	ConfigResourceDHCPLease             ConfigResource = "dhcp_lease"
	ConfigResourceDHCPServer            ConfigResource = "dhcp_server"
	ConfigResourceDHCPStaticBinding     ConfigResource = "dhcp_static_binding"

	ConfigResourceOrchestratorPolicy             ConfigResource = "orchestrator_policy"
	ConfigResourceOrchestratorTopology           ConfigResource = "orchestrator_topology"
	ConfigResourceOrchestratorServiceChainIntent ConfigResource = "orchestrator_service_chain_intent"
)

func (profile Profile) AllowsConfigResource(raw string) bool {
	resource := ConfigResource(strings.TrimSpace(raw))
	if resource == "" {
		return false
	}
	switch resource {
	case ConfigResourceInterface,
		ConfigResourceManagementNetwork,
		ConfigResourceInterfaceBond,
		ConfigResourceObjectGroup,
		ConfigResourceSecurityACL,
		ConfigResourceSecurityBinding,
		ConfigResourceSecurityThreatIntel,
		ConfigResourceSecurityAttackRule,
		ConfigResourceTrafficControl:
		return true
	}
	if profile.ID() == Gateway().ID() {
		switch resource {
		case ConfigResourceSystemMode,
			ConfigResourceWANLink,
			ConfigResourceWANGroup,
			ConfigResourceRoutePolicy,
			ConfigResourceNATStatic,
			ConfigResourcePortMap,
			ConfigResourceProxyEgress,
			ConfigResourceProxyGroup,
			ConfigResourceProxyNode,
			ConfigResourceProxySubscription,
			ConfigResourceDNSPolicy,
			ConfigResourceDNSUpstream,
			ConfigResourceDNSDomainIPSet,
			ConfigResourceDNSRuleUpdateRollback,
			ConfigResourceDHCPLease,
			ConfigResourceDHCPServer,
			ConfigResourceDHCPStaticBinding:
			return true
		}
	}
	if profile.ID() == Orchestrator().ID() {
		switch resource {
		case ConfigResourceOrchestratorPolicy,
			ConfigResourceOrchestratorTopology,
			ConfigResourceOrchestratorServiceChainIntent:
			return true
		}
	}
	return false
}
