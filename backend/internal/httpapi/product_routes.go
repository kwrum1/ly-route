package httpapi

import (
	"net/http"
	"strings"

	"ly-route/backend/internal/product"
)

type serverHandler func(*Server, http.ResponseWriter, *http.Request)
type handlerFactory func(*Server) http.HandlerFunc

type productRoute struct {
	pattern    string
	capability product.Capability
	handler    handlerFactory
}

func productRoutes() []productRoute {
	return []productRoute{
		{pattern: "/api/v1/product", capability: product.CapabilityProduct, handler: bindHandler((*Server).handleProduct)},
		{pattern: "/api/v1/health", capability: product.CapabilityHealth, handler: bindHandler((*Server).handleHealth)},
		{pattern: "/api/v1/mode", capability: product.CapabilityGatewayMode, handler: bindHandler((*Server).handleMode)},
		{pattern: "/api/v1/mode/initialize", capability: product.CapabilityGatewayMode, handler: bindHandler((*Server).handleModeInitialize)},
		{pattern: "/api/v1/capabilities", capability: product.CapabilityHealth, handler: bindHandler((*Server).handleCapabilities)},
		{pattern: "/api/v1/auth/login", capability: product.CapabilityAuth, handler: bindHandler((*Server).handleLogin)},
		{pattern: "/api/v1/auth/logout", capability: product.CapabilityAuth, handler: bindHandler((*Server).handleLogout)},
		{pattern: "/api/v1/auth/session", capability: product.CapabilityAuth, handler: bindHandler((*Server).handleSession)},
		{pattern: "/api/v1/auth/change-password", capability: product.CapabilityAuth, handler: bindHandler((*Server).handleChangePassword)},
		{pattern: "/api/v1/auth/users", capability: product.CapabilityAuth, handler: bindHandler((*Server).handleUsers)},
		{pattern: "/api/v1/auth/users/", capability: product.CapabilityAuth, handler: bindHandler((*Server).handleUserItem)},
		{pattern: "/api/v1/management/network", capability: product.CapabilityManagement, handler: bindHandler((*Server).handleManagementNetwork)},
		{pattern: "/api/v1/interfaces", capability: product.CapabilityInterfaces, handler: desiredCollection("interface")},
		{pattern: "/api/v1/interfaces/", capability: product.CapabilityInterfaces, handler: desiredItem("interface")},
		{pattern: "/api/v1/interface-bonds", capability: product.CapabilityInterfaces, handler: bindHandler((*Server).handleInterfaceBonds)},
		{pattern: "/api/v1/interface-bonds/", capability: product.CapabilityInterfaces, handler: desiredItem("interface_bond")},
		{pattern: "/api/v1/objects/ip-groups", capability: product.CapabilityObjectGroups, handler: desiredCollection("object_group")},
		{pattern: "/api/v1/objects/ip-groups/", capability: product.CapabilityObjectGroups, handler: desiredItem("object_group")},
		{pattern: "/api/v1/objects/groups", capability: product.CapabilityGatewayRouting, handler: desiredCollection("object_group")},
		{pattern: "/api/v1/objects/groups/", capability: product.CapabilityGatewayRouting, handler: desiredItem("object_group")},
		{pattern: "/api/v1/gateway/wan-links", capability: product.CapabilityGatewayWAN, handler: desiredCollection("wan_link")},
		{pattern: "/api/v1/gateway/wan-links/", capability: product.CapabilityGatewayWAN, handler: desiredItem("wan_link")},
		{pattern: "/api/v1/gateway/wan-groups", capability: product.CapabilityGatewayWAN, handler: desiredCollection("wan_group")},
		{pattern: "/api/v1/gateway/wan-groups/", capability: product.CapabilityGatewayWAN, handler: desiredItem("wan_group")},
		{pattern: "/api/v1/gateway/pppoe/status", capability: product.CapabilityGatewayPPPoE, handler: bindHandler((*Server).handlePPPoEStatus)},
		{pattern: "/api/v1/gateway/pppoe/connect", capability: product.CapabilityGatewayPPPoE, handler: pppoeLifecycle("connect")},
		{pattern: "/api/v1/gateway/pppoe/disconnect", capability: product.CapabilityGatewayPPPoE, handler: pppoeLifecycle("disconnect")},
		{pattern: "/api/v1/gateway/policies/routes", capability: product.CapabilityGatewayRouting, handler: desiredCollection("route_policy")},
		{pattern: "/api/v1/gateway/policies/routes/", capability: product.CapabilityGatewayRouting, handler: desiredItem("route_policy")},
		{pattern: "/api/v1/gateway/nat/static", capability: product.CapabilityGatewayNAT, handler: desiredCollection("nat_static")},
		{pattern: "/api/v1/gateway/nat/static/", capability: product.CapabilityGatewayNAT, handler: desiredItem("nat_static")},
		{pattern: "/api/v1/gateway/nat/port-maps", capability: product.CapabilityGatewayNAT, handler: desiredCollection("port_map")},
		{pattern: "/api/v1/gateway/nat/port-maps/", capability: product.CapabilityGatewayNAT, handler: desiredItem("port_map")},
		{pattern: "/api/v1/proxy/egress", capability: product.CapabilityProxy, handler: bindHandler((*Server).handleProxyEgresses)},
		{pattern: "/api/v1/proxy/egresses", capability: product.CapabilityProxy, handler: bindHandler((*Server).handleProxyEgresses)},
		{pattern: "/api/v1/proxy/egresses/", capability: product.CapabilityProxy, handler: bindHandler((*Server).handleProxyEgressItem)},
		{pattern: "/api/v1/proxy/nodes", capability: product.CapabilityProxy, handler: desiredCollection("proxy_node")},
		{pattern: "/api/v1/proxy/nodes/", capability: product.CapabilityProxy, handler: desiredItem("proxy_node")},
		{pattern: "/api/v1/proxy/subscriptions", capability: product.CapabilityProxy, handler: desiredCollection("proxy_subscription")},
		{pattern: "/api/v1/proxy/subscriptions/", capability: product.CapabilityProxy, handler: desiredItem("proxy_subscription")},
		{pattern: "/api/v1/proxy/xray/status", capability: product.CapabilityProxy, handler: proxyXrayRuntime("status")},
		{pattern: "/api/v1/proxy/xray/restart", capability: product.CapabilityProxy, handler: proxyXrayRuntime("restart")},
		{pattern: "/api/v1/proxy/xray/logs", capability: product.CapabilityProxy, handler: proxyXrayRuntime("logs")},
		{pattern: "/api/v1/proxy/groups", capability: product.CapabilityProxy, handler: desiredCollection("proxy_group")},
		{pattern: "/api/v1/proxy/groups/", capability: product.CapabilityProxy, handler: desiredItem("proxy_group")},
		{pattern: "/api/v1/dns/policies", capability: product.CapabilityDNS, handler: bindHandler((*Server).handleDNSPolicies)},
		{pattern: "/api/v1/dns/policies/", capability: product.CapabilityDNS, handler: bindHandler((*Server).handleDNSPolicyItem)},
		{pattern: "/api/v1/dns/resolve", capability: product.CapabilityDNS, handler: bindHandler((*Server).handleDNSResolve)},
		{pattern: "/api/v1/dns/rule-updates", capability: product.CapabilityDNS, handler: bindHandler((*Server).handleDNSRuleUpdates)},
		{pattern: "/api/v1/internal/dns/ipset-observations", capability: product.CapabilityDNS, handler: bindHandler((*Server).handleDNSIPSetObservations)},
		{pattern: "/api/v1/dns/domain-ip-sets", capability: product.CapabilityDNS, handler: desiredCollection("domain_ip_set")},
		{pattern: "/api/v1/dns/domain-ip-sets/", capability: product.CapabilityDNS, handler: desiredItem("domain_ip_set")},
		{pattern: "/api/v1/dns/upstreams", capability: product.CapabilityDNS, handler: desiredCollection("dns_upstream")},
		{pattern: "/api/v1/dns/upstreams/", capability: product.CapabilityDNS, handler: desiredItem("dns_upstream")},
		{pattern: "/api/v1/dhcp/servers", capability: product.CapabilityDHCP, handler: desiredCollection("dhcp_server")},
		{pattern: "/api/v1/dhcp/servers/", capability: product.CapabilityDHCP, handler: desiredItem("dhcp_server")},
		{pattern: "/api/v1/dhcp/leases", capability: product.CapabilityDHCP, handler: bindHandler((*Server).handleDHCPLeases)},
		{pattern: "/api/v1/dhcp/leases/", capability: product.CapabilityDHCP, handler: bindHandler((*Server).handleDHCPLeaseItem)},
		{pattern: "/api/v1/dhcp/static-bindings", capability: product.CapabilityDHCP, handler: desiredCollection("dhcp_static_binding")},
		{pattern: "/api/v1/dhcp/static-bindings/", capability: product.CapabilityDHCP, handler: desiredItem("dhcp_static_binding")},
		{pattern: "/api/v1/security/acls", capability: product.CapabilitySecurity, handler: desiredCollection("security_acl")},
		{pattern: "/api/v1/security/acls/", capability: product.CapabilitySecurity, handler: desiredItem("security_acl")},
		{pattern: "/api/v1/security/ip-mac-bindings", capability: product.CapabilitySecurity, handler: desiredCollection("security_ip_mac_binding")},
		{pattern: "/api/v1/security/ip-mac-bindings/", capability: product.CapabilitySecurity, handler: desiredItem("security_ip_mac_binding")},
		{pattern: "/api/v1/security/threat-intel", capability: product.CapabilitySecurity, handler: desiredCollection("security_threat_intel")},
		{pattern: "/api/v1/security/threat-intel/", capability: product.CapabilitySecurity, handler: desiredItem("security_threat_intel")},
		{pattern: "/api/v1/security/attack-rules", capability: product.CapabilitySecurity, handler: desiredCollection("security_attack_rule")},
		{pattern: "/api/v1/security/attack-rules/", capability: product.CapabilitySecurity, handler: desiredItem("security_attack_rule")},
		{pattern: "/api/v1/security/runtime", capability: product.CapabilityGatewayRouting, handler: bindHandler((*Server).handleSecurityRuntimeStatus)},
		{pattern: "/api/v1/flow-control/runtime", capability: product.CapabilityTrafficControl, handler: bindHandler((*Server).handleDefaultFlowIntent)},
		{pattern: "/api/v1/flow-control/intents/default", capability: product.CapabilityTrafficControl, handler: bindHandler((*Server).handleDefaultFlowIntent)},
		{pattern: "/api/v1/flow-control/policies", capability: product.CapabilityTrafficControl, handler: desiredCollection("traffic_control")},
		{pattern: "/api/v1/flow-control/policies/", capability: product.CapabilityTrafficControl, handler: desiredItem("traffic_control")},
		{pattern: "/api/v1/flow-control/smart-qos", capability: product.CapabilityGatewayRouting, handler: bindHandler((*Server).handleSmartQoSStatus)},
		{pattern: "/api/v1/gateway/traffic-control", capability: product.CapabilityGatewayRouting, handler: desiredCollection("traffic_control")},
		{pattern: "/api/v1/gateway/traffic-control/", capability: product.CapabilityGatewayRouting, handler: desiredItem("traffic_control")},
		{pattern: "/api/v1/runtime/preview", capability: product.CapabilityRuntime, handler: bindHandler((*Server).handleRuntimePreview)},
		{pattern: "/api/v1/runtime/apply", capability: product.CapabilityRuntime, handler: bindHandler((*Server).handleRuntimeApply)},
		{pattern: "/api/v1/runtime/status", capability: product.CapabilityRuntime, handler: bindHandler((*Server).handleRuntimeStatus)},
		{pattern: "/api/v1/config/apply", capability: product.CapabilityConfig, handler: bindHandler((*Server).handleConfigApply)},
		{pattern: "/api/v1/config/export", capability: product.CapabilityConfig, handler: bindHandler((*Server).handleConfigExport)},
		{pattern: "/api/v1/config/import", capability: product.CapabilityConfig, handler: bindHandler((*Server).handleConfigImport)},
		{pattern: "/api/v1/config/snapshots", capability: product.CapabilityConfig, handler: bindHandler((*Server).handleConfigSnapshots)},
		{pattern: "/api/v1/config/snapshots/", capability: product.CapabilityConfig, handler: bindHandler((*Server).handleConfigSnapshotRestore)},
		{pattern: "/api/v1/config/factory-reset", capability: product.CapabilityConfig, handler: bindHandler((*Server).handleConfigFactoryReset)},
		{pattern: "/api/v1/firmware/update/status", capability: product.CapabilityFirmware, handler: bindHandler((*Server).handleFirmwareUpdateStatus)},
		{pattern: "/api/v1/firmware/update/stage", capability: product.CapabilityFirmware, handler: bindHandler((*Server).handleFirmwareUpdateStage)},
		{pattern: "/api/v1/firmware/update/install", capability: product.CapabilityFirmware, handler: bindHandler((*Server).handleFirmwareUpdateInstall)},
		{pattern: "/api/v1/dashboard/summary", capability: product.CapabilityDashboard, handler: bindHandler((*Server).handleDashboardSummaryTelemetry)},
		{pattern: "/api/v1/telemetry/audit-events", capability: product.CapabilityTelemetry, handler: bindHandler((*Server).handleAuditEvents)},
		{pattern: "/api/v1/telemetry/dashboard", capability: product.CapabilityTelemetry, handler: telemetry("dashboard")},
		{pattern: "/api/v1/telemetry/interfaces", capability: product.CapabilityTelemetry, handler: telemetry("interfaces")},
		{pattern: "/api/v1/telemetry/traffic-trend", capability: product.CapabilityTelemetry, handler: bindHandler((*Server).handleTrafficTrend)},
		{pattern: "/api/v1/telemetry/top-sessions", capability: product.CapabilityTelemetry, handler: bindHandler((*Server).handleTopSessionsTelemetry)},
		{pattern: "/api/v1/telemetry/top-domains", capability: product.CapabilityTopDomains, handler: telemetry("top_domains")},
		{pattern: "/api/v1/telemetry/online-users", capability: product.CapabilityTelemetry, handler: telemetry("online_users")},
		{pattern: "/api/v1/telemetry/policy-hits", capability: product.CapabilityTelemetry, handler: telemetry("policy_hits")},
	}
}

func bindHandler(handler serverHandler) handlerFactory {
	return func(server *Server) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			handler(server, w, r)
		}
	}
}

func desiredCollection(resourceType string) handlerFactory {
	return func(server *Server) http.HandlerFunc { return server.handleDesiredCollection(resourceType) }
}

func desiredItem(resourceType string) handlerFactory {
	return func(server *Server) http.HandlerFunc { return server.handleDesiredItem(resourceType) }
}

func pppoeLifecycle(action string) handlerFactory {
	return func(server *Server) http.HandlerFunc { return server.handlePPPoELifecycle(action) }
}

func proxyXrayRuntime(action string) handlerFactory {
	return func(server *Server) http.HandlerFunc { return server.handleProxyXrayRuntime(action) }
}

func telemetry(kind string) handlerFactory {
	return func(server *Server) http.HandlerFunc { return server.handleTelemetry(kind) }
}

func routeCapability(path string) (product.Capability, bool) {
	for _, route := range productRoutes() {
		if route.pattern == path || strings.HasSuffix(route.pattern, "/") && strings.HasPrefix(path, route.pattern) {
			return route.capability, true
		}
	}
	return "", false
}
