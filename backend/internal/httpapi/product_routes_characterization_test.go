package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestGatewayCompatibilityConstructorRegistersEveryExistingRoute(t *testing.T) {
	t.Parallel()

	// Given
	server := New()
	paths := []string{
		"/api/v1/health",
		"/api/v1/mode",
		"/api/v1/mode/initialize",
		"/api/v1/capabilities",
		"/api/v1/auth/login",
		"/api/v1/auth/logout",
		"/api/v1/auth/session",
		"/api/v1/auth/change-password",
		"/api/v1/auth/users",
		"/api/v1/auth/users/example",
		"/api/v1/management/network",
		"/api/v1/interfaces",
		"/api/v1/interfaces/example",
		"/api/v1/interface-bonds",
		"/api/v1/interface-bonds/example",
		"/api/v1/objects/groups",
		"/api/v1/objects/groups/example",
		"/api/v1/gateway/wan-links",
		"/api/v1/gateway/wan-links/example",
		"/api/v1/gateway/wan-groups",
		"/api/v1/gateway/wan-groups/example",
		"/api/v1/gateway/pppoe/status",
		"/api/v1/gateway/pppoe/connect",
		"/api/v1/gateway/pppoe/disconnect",
		"/api/v1/gateway/policies/routes",
		"/api/v1/gateway/policies/routes/example",
		"/api/v1/gateway/nat/static",
		"/api/v1/gateway/nat/static/example",
		"/api/v1/gateway/nat/port-maps",
		"/api/v1/gateway/nat/port-maps/example",
		"/api/v1/proxy/egress",
		"/api/v1/proxy/egresses",
		"/api/v1/proxy/egresses/example",
		"/api/v1/proxy/nodes",
		"/api/v1/proxy/nodes/example",
		"/api/v1/proxy/subscriptions",
		"/api/v1/proxy/subscriptions/example",
		"/api/v1/proxy/xray/status",
		"/api/v1/proxy/xray/restart",
		"/api/v1/proxy/xray/logs",
		"/api/v1/proxy/groups",
		"/api/v1/proxy/groups/example",
		"/api/v1/dns/policies",
		"/api/v1/dns/policies/example",
		"/api/v1/dns/resolve?domain=example.test",
		"/api/v1/dns/rule-updates",
		"/api/v1/dns/domain-ip-sets",
		"/api/v1/dns/domain-ip-sets/example",
		"/api/v1/dns/upstreams",
		"/api/v1/dns/upstreams/example",
		"/api/v1/dhcp/servers",
		"/api/v1/dhcp/servers/example",
		"/api/v1/dhcp/leases",
		"/api/v1/dhcp/leases/example",
		"/api/v1/dhcp/static-bindings",
		"/api/v1/dhcp/static-bindings/example",
		"/api/v1/security/acls",
		"/api/v1/security/acls/example",
		"/api/v1/security/ip-mac-bindings",
		"/api/v1/security/ip-mac-bindings/example",
		"/api/v1/security/threat-intel",
		"/api/v1/security/threat-intel/example",
		"/api/v1/security/attack-rules",
		"/api/v1/security/attack-rules/example",
		"/api/v1/flow-control/runtime",
		"/api/v1/flow-control/intents/default",
		"/api/v1/flow-control/policies",
		"/api/v1/flow-control/policies/example",
		"/api/v1/flow-control/smart-qos",
		"/api/v1/gateway/traffic-control",
		"/api/v1/gateway/traffic-control/example",
		"/api/v1/runtime/preview",
		"/api/v1/runtime/apply",
		"/api/v1/runtime/status",
		"/api/v1/config/apply",
		"/api/v1/config/export",
		"/api/v1/config/import",
		"/api/v1/config/snapshots",
		"/api/v1/config/snapshots/example",
		"/api/v1/config/factory-reset",
		"/api/v1/firmware/update/status",
		"/api/v1/firmware/update/stage",
		"/api/v1/firmware/update/install",
		"/api/v1/dashboard/summary",
		"/api/v1/telemetry/audit-events",
		"/api/v1/telemetry/dashboard",
		"/api/v1/telemetry/interfaces",
		"/api/v1/telemetry/traffic-trend",
		"/api/v1/telemetry/top-sessions",
		"/api/v1/telemetry/top-domains",
		"/api/v1/telemetry/online-users",
		"/api/v1/telemetry/policy-hits",
	}

	for _, path := range paths {
		path := path
		t.Run(path, func(t *testing.T) {
			// When
			response := request(t, server, http.MethodGet, path)

			// Then
			if strings.Contains(response.Body.String(), `"message":"unknown API route"`) {
				t.Fatalf("GET %s reached the API fallback: %s", path, response.Body.String())
			}
		})
	}
}

func TestGatewayCompatibilityConstructorPreservesRepresentativeResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		path         string
		wantStatus   int
		wantFragment string
	}{
		{name: "health remains available", path: "/api/v1/health", wantStatus: http.StatusOK, wantFragment: `"status":"degraded"`},
		{name: "traffic control remains available", path: "/api/v1/gateway/traffic-control", wantStatus: http.StatusOK, wantFragment: "classify-default"},
		{name: "built-in smart QoS remains read-only", path: "/api/v1/flow-control/smart-qos", wantStatus: http.StatusOK, wantFragment: `"mutable":false`},
		{name: "proxy egress remains available", path: "/api/v1/proxy/egress", wantStatus: http.StatusOK, wantFragment: "proxy_egress"},
		{name: "dns resolution remains available", path: "/api/v1/dns/resolve?domain=example.test", wantStatus: http.StatusOK, wantFragment: `"answer":"NODATA"`},
		{name: "unknown route remains typed", path: "/api/v1/characterization-missing", wantStatus: http.StatusNotFound, wantFragment: `"code":"not_found"`},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			// Given
			server := New()

			// When
			response := request(t, server, http.MethodGet, test.path)

			// Then
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantFragment) {
				t.Fatalf("GET %s = %d %s, want status %d containing %q", test.path, response.Code, response.Body.String(), test.wantStatus, test.wantFragment)
			}
		})
	}
}
