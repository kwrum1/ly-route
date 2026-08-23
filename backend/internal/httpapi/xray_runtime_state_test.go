package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"ly-route/backend/internal/persistence"
	serviceRuntime "ly-route/backend/internal/runtime/service"
)

func TestXrayStatusReadsLiveFastestSubscriptionSelection(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:xray-live-selection?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SaveConfig(ctx, configDocument(t, "proxy_subscription", "main", map[string]any{"id": "main", "enabled": true, "selection": "fastest", "node_refs": []string{"a", "b"}}, fixedClock()())); err != nil {
		t.Fatal(err)
	}
	controller := &httpServiceController{
		health:     map[serviceRuntime.ServiceName]serviceRuntime.Health{serviceRuntime.Xray: {Service: serviceRuntime.Xray, Available: true}},
		xrayStates: []serviceRuntime.XrayBalancerState{{Tag: "subscription-main-fastest", SelectedOutboundTags: []string{"subscription-main-node-b"}}},
	}
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	status := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/proxy/xray/status", "", login.Result().Cookies()[0])
	for _, required := range []string{`"available":true`, `"state":"available"`, `"subscription_id":"main"`, `"selected_node_ids":["b"]`, `"live_verified":true`} {
		if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), required) {
			t.Fatalf("Xray live status missing %q: %d %s", required, status.Code, status.Body.String())
		}
	}
}

func TestXrayStatusDegradesWhenBalancerHasNoLiveSelection(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:xray-missing-selection?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SaveConfig(ctx, configDocument(t, "proxy_subscription", "main", map[string]any{"id": "main", "enabled": true, "selection": "fastest", "node_refs": []string{"a"}}, fixedClock()())); err != nil {
		t.Fatal(err)
	}
	controller := &httpServiceController{
		health:       map[serviceRuntime.ServiceName]serviceRuntime.Health{serviceRuntime.Xray: {Service: serviceRuntime.Xray, Available: true}},
		xrayStateErr: errors.New("Xray balancer main has no healthy selected outbound"),
	}
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	status := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/proxy/xray/status", "", login.Result().Cookies()[0])
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"available":false`) || !strings.Contains(status.Body.String(), `"state":"degraded"`) || !strings.Contains(status.Body.String(), "no healthy selected outbound") {
		t.Fatalf("Xray missing selection did not degrade explicitly: %d %s", status.Code, status.Body.String())
	}
}

func TestXrayStatusReportsAdaptiveFiveTuplePoolWithoutBalancerAPI(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:xray-adaptive-pool?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SaveConfig(ctx, configDocument(t, "proxy_subscription", "main", map[string]any{
		"id": "main", "enabled": true, "selection": "adaptive", "strategy": "adaptive_topn_weighted", "top_n": 2, "node_refs": []string{"a", "b", "c"},
	}, fixedClock()())); err != nil {
		t.Fatal(err)
	}
	controller := &httpServiceController{
		health: map[serviceRuntime.ServiceName]serviceRuntime.Health{serviceRuntime.Xray: {Service: serviceRuntime.Xray, Available: true}},
		xrayObservations: []serviceRuntime.XrayObservatoryState{
			{OutboundTag: "subscription-main-node-a", Alive: true, DelayMilliseconds: 10},
			{OutboundTag: "subscription-main-node-b", Alive: true, DelayMilliseconds: 20},
			{OutboundTag: "subscription-main-node-c", Alive: true, DelayMilliseconds: 100},
		},
	}
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}))
	selections, err := server.xrayRuntimeSelections(ctx)
	if err != nil || len(selections) != 1 {
		t.Fatalf("adaptive selections = %#v, err=%v", selections, err)
	}
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	status := authenticatedJSONRequest(t, server, http.MethodGet, "/api/v1/proxy/xray/status", "", login.Result().Cookies()[0])
	for _, required := range []string{
		`"available":true`, `"algorithm":"rtt_weighted_five_tuple"`,
		`"candidate_node_ids":["a","b","c"]`, `"selected_node_ids":["a","b"]`,
		`"node_id":"a","rtt_ms":10,"weight":67`, `"node_id":"b","rtt_ms":20,"weight":33`,
		`"live_verified":true`,
	} {
		if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), required) {
			t.Fatalf("adaptive Xray status missing %q: %d %s", required, status.Code, status.Body.String())
		}
	}
}
