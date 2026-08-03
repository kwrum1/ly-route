package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/apply"
	serviceRuntime "ly-route/backend/internal/runtime/service"
	"ly-route/backend/internal/runtime/trafficpolicy"
	"ly-route/backend/internal/runtime/vpp"
)

// TestDNSIPSetObservationVPPIntegration proves the production control path:
// a local SmartDNS observation creates the TTL-bound override and the gateway
// transaction programs its VPP route policy. The caller supplies a vppctl
// wrapper connected to the isolated VPP container.
func TestDNSIPSetObservationVPPIntegration(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("LY_ROUTE_VPPCTL_INTEGRATION_BINARY"))
	if binary == "" {
		t.Skip("LY_ROUTE_VPPCTL_INTEGRATION_BINARY is not set")
	}
	// The package TestMain deliberately defaults to eth0 for ordinary unit tests.
	// This real-VPP fixture exposes lan0 instead, so pin the control-plane render
	// and the VPPCTL readback to the interface that actually exists here.
	t.Setenv("LY_ROUTE_LAN_INTERFACE", "lan0")
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-dns-vpp-integration?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	controller := &httpServiceController{health: map[serviceRuntime.ServiceName]serviceRuntime.Health{
		serviceRuntime.SmartDNS: {Service: serviceRuntime.SmartDNS, Available: true},
	}}
	transaction := &dnsVPPRouteTransaction{adapter: vpp.Adapter{Client: vpp.NewProductionVPPCTLClient(binary)}}
	server := New(
		WithStore(store),
		WithDNSSyncToken("integration-sync-token"),
		WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}),
		WithClock(fixedClock()),
		WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}),
		WithGatewayTransaction(transaction),
	)
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	for _, write := range []struct {
		path string
		body string
	}{
		{"/api/v1/gateway/wan-links", `{"id":"wan-primary","interface_id":"wan0","enabled":true,"type":"static","gateway":"10.0.1.2","ipv4":{"mode":"static","address":"10.0.0.1/24","gateway":"10.0.1.2"}}`},
		{"/api/v1/dns/upstreams", `{"id":"dns-primary","enabled":true,"servers":["9.9.9.9"],"wan_egress_id":"wan-primary","interface":"wan0","cache_size":32768,"ttl_min_seconds":60,"ttl_max_seconds":600,"prefetch":true}`},
		{"/api/v1/dns/policies", `{"id":"fixed-wan","name":"Fixed WAN","enabled":true,"policy":{"engine":"smartdns","miss":{"kind":"reject"},"rules":[{"id":"updates","source_prefixes":["10.0.0.2/32"],"domains":["updates.example"],"outcome":{"kind":"direct","wan_egress_id":"wan-primary"}}]}}`},
		{"/api/v1/gateway/policies/routes", `{"id":"ordinary-pbr","priority":10,"action":"route","egress":"wan-primary","match":{"src_ip":"10.0.0.2/32","dst_ip":"10.0.1.2/32","protocol":"any"}}`},
	} {
		if response := authenticatedJSONRequest(t, server, http.MethodPost, write.path, write.body, cookie); response.Code != http.StatusOK {
			t.Fatalf("write %s status=%d body=%s", write.path, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/internal/dns/ipset-observations", strings.NewReader(`{"rule_id":"updates","set_name":"lyroute_dns_updates","members":[{"set_name":"lyroute_dns_updates","ip":"10.0.1.2","expires_at":"2099-06-12T13:00:00Z"}]}`))
	request.RemoteAddr = "127.0.0.1:4000"
	request.Header.Set(HeaderDNSSyncToken, "integration-sync-token")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted && response.Code != http.StatusOK {
		t.Fatalf("DNS observation status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"members_applied":1`) || !strings.Contains(response.Body.String(), `"status":"committed"`) && !strings.Contains(response.Body.String(), `"status":"degraded"`) {
		t.Fatalf("DNS observation did not commit its runtime transaction: %s", response.Body.String())
	}
	if !transaction.hasRoutes("ordinary-pbr", "dns-override-updates") {
		t.Fatalf("DNS priority route was not applied: %#v", transaction.routes)
	}

	// A stale SmartDNS member must remove the VPP override rather than leave a
	// traffic-steering rule behind after its TTL has elapsed.
	expired := httptest.NewRequest(http.MethodPost, "/api/v1/internal/dns/ipset-observations", strings.NewReader(`{"rule_id":"updates","set_name":"lyroute_dns_updates","members":[{"set_name":"lyroute_dns_updates","ip":"10.0.1.2","expires_at":"2000-01-01T00:00:00Z"}]}`))
	expired.RemoteAddr = "127.0.0.1:4000"
	expired.Header.Set(HeaderDNSSyncToken, "integration-sync-token")
	expiredResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(expiredResponse, expired)
	if expiredResponse.Code != http.StatusAccepted && expiredResponse.Code != http.StatusOK {
		t.Fatalf("expired DNS observation status=%d body=%s", expiredResponse.Code, expiredResponse.Body.String())
	}
	if !strings.Contains(expiredResponse.Body.String(), `"members_applied":0`) || !transaction.hasRoutes("ordinary-pbr") || transaction.hasRoutes("dns-override-updates") {
		t.Fatalf("expired DNS override was not removed: %s routes=%#v", expiredResponse.Body.String(), transaction.routes)
	}
}

// The integration fixture isolates the route/WAN-group owner so unrelated
// default security resources cannot hide the DNS-to-VPP assertion.
type dnsVPPRouteTransaction struct {
	adapter vpp.Adapter
	mu      sync.Mutex
	routes  []trafficpolicy.RoutePolicy
}

func (transaction *dnsVPPRouteTransaction) Run(ctx context.Context, plan apply.Plan) (apply.GatewayTransactionResult, error) {
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	desired := append([]trafficpolicy.RoutePolicy(nil), plan.GatewayPlan.Policy.RoutePolicies...)
	desiredIDs := make(map[string]struct{}, len(desired))
	for _, route := range desired {
		desiredIDs[route.ID] = struct{}{}
	}
	deleted := make([]string, 0)
	for _, route := range transaction.routes {
		if _, retained := desiredIDs[route.ID]; !retained {
			deleted = append(deleted, route.ID)
		}
	}
	_, err := transaction.adapter.ApplyRouteWANGroup(ctx, vpp.RouteWANGroupPlan{
		TransactionID:    plan.Request.TransactionID,
		Routes:           desired,
		DeleteRoutes:     deleted,
		DeleteRouteState: append([]trafficpolicy.RoutePolicy(nil), transaction.routes...),
	}, vpp.Snapshot{RoutePolicies: append([]trafficpolicy.RoutePolicy(nil), transaction.routes...)})
	if err != nil {
		return apply.GatewayTransactionResult{}, err
	}
	transaction.routes = desired
	now := fixedClock()()
	return apply.GatewayTransactionResult{
		Order:     []string{"routes"},
		Receipts:  []apply.ApplyReceipt{{TransactionID: plan.Request.TransactionID, Capability: "routes", Status: apply.ReceiptApplied, AppliedAt: now}},
		Readbacks: []apply.Readback{{TransactionID: plan.Request.TransactionID, Capability: "routes", Timestamp: now, Fresh: true}},
	}, nil
}

func (transaction *dnsVPPRouteTransaction) hasRoutes(ids ...string) bool {
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	for _, route := range transaction.routes {
		delete(wanted, route.ID)
	}
	return len(wanted) == 0
}

func (*dnsVPPRouteTransaction) Rollback(context.Context, apply.Plan) error { return nil }
