package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/apply"
	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/vpp"
)

func TestTrafficControlRuntimeStateTracksCommittedVPPQoS(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-traffic-control-runtime-state-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	cookie := login.Result().Cookies()[0]

	created := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/gateway/traffic-control", `{"id":"flow-status","rules":[{"id":"limit-https","granularity":"rule","match":{"sources":["192.0.2.0/24"],"protocols":["tcp"],"dest_ports":["443"],"direction":"both"},"actions":[{"kind":"policer","policer":{"rate_bps":20000000,"burst_bps":2000000}}]}]}`, cookie)
	if created.Code != http.StatusOK {
		t.Fatalf("create traffic control = %d %s", created.Code, created.Body.String())
	}
	desired, err := server.currentFlowIntent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := flow.CompileIntent(desired)
	if err != nil {
		t.Fatal(err)
	}
	transactionID := "txn-flow-status"
	plan := vpp.Plan{Flow: compiled}
	server.runtimeMu.Lock()
	server.lastRuntime = &RuntimeApplyResult{
		Status: RuntimeStatusCommitted, RuntimeState: RuntimeStateRunning, TransactionID: transactionID,
		Receipt:     apply.ApplyReceipt{TransactionID: transactionID, Capability: "runtime", Status: apply.ReceiptApplied, AppliedAt: fixedClock()()},
		GatewayPlan: &plan,
		GatewayEvidence: []apply.GatewayResourceEvidence{{
			Resource: "qos", TransactionID: transactionID,
			ApplyReceipt: apply.ApplyReceipt{TransactionID: transactionID, Capability: "qos", Status: apply.ReceiptApplied, AppliedAt: fixedClock()()},
			After:        vpp.Snapshot{QoS: compiled.VPPGroups},
		}},
	}
	server.runtimeMu.Unlock()

	list := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/gateway/traffic-control", "", cookie)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"id":"flow-status"`) || !strings.Contains(list.Body.String(), `"runtime_state":"applied"`) || !strings.Contains(list.Body.String(), `"dataplane_observed":true`) {
		t.Fatalf("applied traffic-control list = %d %s", list.Code, list.Body.String())
	}
	item := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/gateway/traffic-control/flow-status", "", cookie)
	if item.Code != http.StatusOK || !strings.Contains(item.Body.String(), `"runtime_state":"applied"`) {
		t.Fatalf("applied traffic-control item = %d %s", item.Code, item.Body.String())
	}

	changed := authenticatedJSONRequest(t, server, http.MethodPatch, "/api/v1/gateway/traffic-control/flow-status", `{"id":"flow-status","rules":[{"id":"limit-https","granularity":"rule","match":{"sources":["192.0.2.0/24"],"protocols":["tcp"],"dest_ports":["443"],"direction":"both"},"actions":[{"kind":"policer","policer":{"rate_bps":30000000,"burst_bps":3000000}}]}]}`, cookie)
	if changed.Code != http.StatusOK {
		t.Fatalf("change traffic control = %d %s", changed.Code, changed.Body.String())
	}
	list = authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/gateway/traffic-control", "", cookie)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"runtime_state":"desired_not_applied"`) || strings.Contains(list.Body.String(), `"runtime_state":"applied"`) {
		t.Fatalf("changed traffic-control list = %d %s", list.Code, list.Body.String())
	}
}
