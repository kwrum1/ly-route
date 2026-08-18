package httpapi

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/apply"
	"ly-route/backend/internal/runtime/trafficpolicy"
	"ly-route/backend/internal/runtime/vpp"
)

func TestRetiredRoutePolicyIDsReturnsOnlyDisabledPolicies(t *testing.T) {
	items := []map[string]any{
		{"id": "route-z", "enabled": false},
		{"id": "route-active", "enabled": true},
		{"id": "route-a", "enabled": false},
		{"id": "route-z", "enabled": false},
		{"enabled": false},
	}
	if got := retiredRoutePolicyIDs(items); !reflect.DeepEqual(got, []string{"route-a", "route-z"}) {
		t.Fatalf("retired route IDs = %#v", got)
	}
}

func TestRoutePolicyRuntimeStateTracksCommittedVPPPlan(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-route-runtime-state-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	cookie := login.Result().Cookies()[0]

	created := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/policies/routes", `{"id":"route-status","enabled":true,"priority":10,"action":"deny","match":{"src_ip":"192.0.2.0/24"}}`, cookie)
	if created.Code != http.StatusOK {
		t.Fatalf("create route = %d %s", created.Code, created.Body.String())
	}
	compiled, err := server.currentTrafficPolicyConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	transactionID := "txn-route-status"
	plan := vpp.Plan{Policy: compiled}
	server.runtimeMu.Lock()
	server.lastRuntime = &RuntimeApplyResult{
		Status:        RuntimeStatusCommitted,
		RuntimeState:  RuntimeStateRunning,
		TransactionID: transactionID,
		Receipt:       apply.ApplyReceipt{TransactionID: transactionID, Capability: "runtime", Status: apply.ReceiptApplied, AppliedAt: fixedClock()()},
		GatewayPlan:   &plan,
		GatewayEvidence: []apply.GatewayResourceEvidence{{
			Resource:      "routes",
			Capability:    "routes",
			TransactionID: transactionID,
			ApplyReceipt:  apply.ApplyReceipt{TransactionID: transactionID, Capability: "routes", Status: apply.ReceiptApplied, AppliedAt: fixedClock()()},
			After:         vpp.Snapshot{RoutePolicies: compiled.RoutePolicies},
		}},
	}
	server.runtimeMu.Unlock()

	list := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/gateway/policies/routes", "", cookie)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"id":"route-status"`) || !strings.Contains(list.Body.String(), `"runtime_state":"applied"`) {
		t.Fatalf("applied route list = %d %s", list.Code, list.Body.String())
	}
	item := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/gateway/policies/routes/route-status", "", cookie)
	if item.Code != http.StatusOK || !strings.Contains(item.Body.String(), `"runtime_state":"applied"`) {
		t.Fatalf("applied route item = %d %s", item.Code, item.Body.String())
	}

	changed := authenticatedJSONRequest(t, server, http.MethodPatch, "/api/v1/gateway/policies/routes/route-status", `{"id":"route-status","enabled":true,"priority":20,"action":"deny","match":{"src_ip":"192.0.2.0/24"}}`, cookie)
	if changed.Code != http.StatusOK {
		t.Fatalf("change route = %d %s", changed.Code, changed.Body.String())
	}
	list = authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/gateway/policies/routes", "", cookie)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"runtime_state":"desired_not_applied"`) || strings.Contains(list.Body.String(), `"runtime_state":"applied"`) {
		t.Fatalf("changed route list = %d %s", list.Code, list.Body.String())
	}
}

func TestRoutePolicyRuntimeStateAcceptsCompleteReadbackAfterDeletion(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-route-runtime-state-delete-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	cookie := login.Result().Cookies()[0]
	created := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/policies/routes", `{"id":"route-remaining","enabled":true,"priority":10,"action":"deny","match":{"src_ip":"192.0.2.0/24"}}`, cookie)
	if created.Code != http.StatusOK {
		t.Fatalf("create route = %d %s", created.Code, created.Body.String())
	}
	compiled, err := server.currentTrafficPolicyConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	transactionID := "txn-route-delete"
	plan := vpp.Plan{Policy: compiled}
	server.runtimeMu.Lock()
	server.lastRuntime = &RuntimeApplyResult{
		Status: RuntimeStatusCommitted, RuntimeState: RuntimeStateRunning, TransactionID: transactionID,
		Receipt:     apply.ApplyReceipt{TransactionID: transactionID, Capability: "runtime", Status: apply.ReceiptApplied, AppliedAt: fixedClock()()},
		GatewayPlan: &plan,
		GatewayEvidence: []apply.GatewayResourceEvidence{{
			Resource: "routes", Deleted: true, TransactionID: transactionID,
			ApplyReceipt: apply.ApplyReceipt{TransactionID: transactionID, Capability: "routes", Status: apply.ReceiptApplied, AppliedAt: fixedClock()()},
			After:        vpp.Snapshot{RoutePolicies: compiled.RoutePolicies},
		}},
	}
	server.runtimeMu.Unlock()

	list := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/gateway/policies/routes", "", cookie)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"runtime_state":"applied"`) {
		t.Fatalf("route list after deletion = %d %s", list.Code, list.Body.String())
	}
}

func TestRoutePolicyRuntimeStateKeepsUserPoliciesAppliedAcrossGeneratedDNSRouteChanges(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-route-runtime-state-dns-change-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	cookie := login.Result().Cookies()[0]
	created := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/policies/routes", `{"id":"route-user","enabled":true,"priority":10,"action":"deny","match":{"src_ip":"192.0.2.0/24"}}`, cookie)
	if created.Code != http.StatusOK {
		t.Fatalf("create route = %d %s", created.Code, created.Body.String())
	}
	compiled, err := server.currentTrafficPolicyConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observed := append([]trafficpolicy.RoutePolicy(nil), compiled.RoutePolicies...)
	observed = append(observed, trafficpolicy.RoutePolicy{ID: "dns-generated-old", Priority: 1, Action: "nat"})
	transactionID := "txn-route-dns-change"
	plan := vpp.Plan{Policy: compiled}
	server.runtimeMu.Lock()
	server.lastRuntime = &RuntimeApplyResult{
		Status: RuntimeStatusCommitted, RuntimeState: RuntimeStateRunning, TransactionID: transactionID,
		Receipt:     apply.ApplyReceipt{TransactionID: transactionID, Capability: "runtime", Status: apply.ReceiptApplied, AppliedAt: fixedClock()()},
		GatewayPlan: &plan,
		GatewayEvidence: []apply.GatewayResourceEvidence{{
			Resource: "routes", TransactionID: transactionID,
			ApplyReceipt: apply.ApplyReceipt{TransactionID: transactionID, Capability: "routes", Status: apply.ReceiptApplied, AppliedAt: fixedClock()()},
			After:        vpp.Snapshot{RoutePolicies: observed},
		}},
	}
	server.runtimeMu.Unlock()

	list := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/gateway/policies/routes", "", cookie)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"runtime_state":"applied"`) {
		t.Fatalf("route list after generated DNS change = %d %s", list.Code, list.Body.String())
	}
}
