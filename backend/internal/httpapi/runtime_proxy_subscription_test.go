package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/proxy"
)

func TestProxySubscriptionPatchPreservesImportedNodeReferences(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-proxy-subscription-patch?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	payload := map[string]any{
		"id": "main", "name": "Main", "enabled": true,
		"node_refs": []string{"main-a", "main-b"}, "node_count": 2,
		"selection": "fastest", "top_n": 1,
	}
	if err := store.SaveConfig(ctx, configDocument(t, "proxy_subscription", "main", payload, fixedClock()())); err != nil {
		t.Fatal(err)
	}
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK || len(login.Result().Cookies()) == 0 {
		t.Fatalf("login status = %d body=%s", login.Code, login.Body.String())
	}
	response := authenticatedJSONRequest(t, server, http.MethodPatch, "/api/v1/proxy/subscriptions/main", `{"id":"main","selection":"adaptive","strategy":"adaptive_topn_weighted","top_n":2}`, login.Result().Cookies()[0])
	if response.Code != http.StatusOK {
		t.Fatalf("patch status = %d body=%s", response.Code, response.Body.String())
	}
	updated, exists, err := server.desiredItem(ctx, "proxy_subscription", "main")
	if err != nil || !exists {
		t.Fatalf("patched subscription exists=%v err=%v", exists, err)
	}
	if stringField(updated, "name") != "Main" || len(stringSliceField(updated, "node_refs")) != 2 || intField(updated, "node_count") != 2 {
		t.Fatalf("patch discarded subscription fields: %#v", updated)
	}
}

func TestProxySubscriptionRejectsAdaptiveTopNOutsideTwoOrThree(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-proxy-subscription-topn?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(WithStore(store), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(fixedClock()))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)
	if login.Code != http.StatusOK || len(login.Result().Cookies()) == 0 {
		t.Fatalf("login status = %d body=%s", login.Code, login.Body.String())
	}
	response := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/proxy/subscriptions", `{"id":"main","name":"Main","enabled":true,"url":"https://provider.example/sub","selection":"adaptive","strategy":"adaptive_topn_weighted","top_n":4}`, login.Result().Cookies()[0])
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("create status = %d body=%s", response.Code, response.Body.String())
	}
}

func TestCompileProxySubscriptionUsesConfiguredDirectNode(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-proxy-node-binding?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	node := map[string]any{
		"id":       "node-direct",
		"name":     "Direct node",
		"enabled":  true,
		"protocol": "vless",
		// This test verifies the configured direct-node binding. DNS hostname
		// resolution has its own fixed-bootstrap boundary test in runtime/proxy;
		// using an IP keeps this unit test deterministic and offline.
		"address": "203.0.113.5",
		"port":    443,
		"settings": map[string]any{
			"network":  "tcp",
			"security": "reality",
			"realitySettings": map[string]any{
				"publicKey":   "public-key",
				"shortId":     "98",
				"serverName":  "www.example.com",
				"fingerprint": "chrome",
			},
		},
	}
	if err := store.SaveConfigWithSecrets(ctx, configDocument(t, "proxy_node", "node-direct", node, fixedClock()()), map[string]string{"secret": "11111111-1111-1111-1111-111111111111"}); err != nil {
		t.Fatal(err)
	}

	egress := proxy.NewProxyEgress("proxy-egress-direct", "xray-tproxy-outbound")
	egress.UnderlayWANID = "wan-pppoe"
	egress.NodeID = "node-direct"
	compiled, err := proxy.CompileEgress(egress)
	if err != nil {
		t.Fatal(err)
	}
	server := New(WithStore(store), WithClock(fixedClock()))

	if warning := server.compileProxySubscription(ctx, egress, &compiled); warning != "" {
		t.Fatalf("direct node compilation warning: %s", warning)
	}
	if len(compiled.XrayRuntime.ConfigPayload.Outbounds) != 1 {
		t.Fatalf("outbounds = %#v, want one direct node outbound", compiled.XrayRuntime.ConfigPayload.Outbounds)
	}
	outbound := compiled.XrayRuntime.ConfigPayload.Outbounds[0]
	if outbound.Protocol != "vless" || outbound.Tag != "node-node-direct" {
		t.Fatalf("outbound = %#v, want configured VLESS node", outbound)
	}
	if strings.Contains(outbound.Protocol, "freedom") {
		t.Fatal("direct node unexpectedly compiled as freedom")
	}
	if outbound.StreamSettings["realitySettings"] == nil {
		t.Fatalf("Reality settings missing from direct node outbound: %#v", outbound.StreamSettings)
	}
}

func TestCompileProxySubscriptionRejectsAmbiguousProxySources(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-proxy-node-ambiguity?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(WithStore(store), WithClock(fixedClock()))
	egress := proxy.NewProxyEgress("proxy-egress-ambiguous", "xray-tproxy-outbound")
	egress.UnderlayWANID = "wan-pppoe"
	egress.NodeID = "node-direct"
	egress.SubscriptionID = "subscription-direct"
	if _, err := proxy.CompileEgress(egress); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("ambiguous egress validation error = %v", err)
	}

	compiled := proxy.CompiledEgress{}
	if warning := server.compileProxySubscription(ctx, proxy.NewProxyEgress("proxy-egress-ambiguous", "xray-tproxy-outbound"), &compiled); !strings.Contains(warning, "node_id or subscription_id") {
		t.Fatalf("empty source warning = %q", warning)
	}
}
