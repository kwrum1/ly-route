package httpapi

import (
	"net/http"
	"os"
	"strings"
	"testing"

	serviceRuntime "ly-route/backend/internal/runtime/service"
)

func TestKeaLeaseHTTPIntegration(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("LY_ROUTE_KEA_LEASE_INTEGRATION_FILE"))
	if path == "" {
		t.Skip("LY_ROUTE_KEA_LEASE_INTEGRATION_FILE is not set")
	}
	server := New(WithDHCPLeases(serviceRuntime.KeaMemfileLeaseCollector{Path: path}))
	response := request(t, server, http.MethodGet, "/api/v1/dhcp/leases")
	if os.Getenv("LY_ROUTE_KEA_LEASE_EXPECT_UNAVAILABLE") == "1" {
		if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "dhcp_leases_failed") || strings.Contains(response.Body.String(), "192.0.2.50") {
			t.Fatalf("missing Kea database did not fail explicitly without stale leases: %d %s", response.Code, response.Body.String())
		}
		return
	}
	if response.Code != http.StatusOK {
		t.Fatalf("Kea lease API status = %d: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, required := range []string{"192.0.2.50", "02:00:00:00:00:02", `"runtime_state":"running"`, `"name":"kea_leases"`, `"available":true`} {
		if !strings.Contains(body, required) {
			t.Fatalf("Kea lease API response missing %q: %s", required, body)
		}
	}
	for _, forbidden := range []string{"client_id", "user_context"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("Kea lease API leaked %q: %s", forbidden, body)
		}
	}
}
