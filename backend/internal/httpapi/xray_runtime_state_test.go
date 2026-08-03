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
