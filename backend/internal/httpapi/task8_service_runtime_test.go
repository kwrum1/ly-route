package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/proxy"
	serviceRuntime "ly-route/backend/internal/runtime/service"
)

func TestRuntimeApply_xray_failure_degrades_only_proxy_capability(t *testing.T) {
	// Given
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-task8-xray-failure?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	saveTestProxyNode(t, store)
	controller := &httpServiceController{
		applyErrs: map[serviceRuntime.ServiceName]error{serviceRuntime.Xray: errors.New("xray daemon missing")},
		health: map[serviceRuntime.ServiceName]serviceRuntime.Health{
			serviceRuntime.SmartDNS: {Service: serviceRuntime.SmartDNS, Available: true},
			serviceRuntime.Xray:     {Service: serviceRuntime.Xray, Available: false, Reason: "xray daemon missing"},
		},
	}
	server := New(
		WithStore(store),
		WithClock(fixedClock()),
		WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}),
		WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}),
		WithProxyEgress(testProxyEgressWithNode()),
		WithFlowIntent(flow.NewIntent("default", []flow.Rule{flow.NewRule("classify-default", flow.RuleGranularity, flow.Classify("best-effort"))})),
	)
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)

	// When
	response := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/runtime/apply", `{}`, login.Result().Cookies()[0])

	// Then
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"status":"degraded"`) || strings.Contains(response.Body.String(), `"status":"apply_failed"`) {
		t.Fatalf("runtime apply = %d %s", response.Code, response.Body.String())
	}
	if got, want := strings.Join(controller.applied, ","), "smartdns,xray"; got != want {
		t.Fatalf("attempted services = %s, want %s", got, want)
	}
	if len(controller.receiptArtifacts) != 2 || controller.receiptArtifacts[0].Service != serviceRuntime.SmartDNS || controller.receiptArtifacts[1].Service != serviceRuntime.SmartDNS || controller.receiptArtifacts[1].Path != "/etc/ly-route/dns-source-routes.conf" {
		t.Fatalf("receipt artifacts = %#v, want SmartDNS config and source route map", controller.receiptArtifacts)
	}
	restartedController := &httpServiceController{health: map[serviceRuntime.ServiceName]serviceRuntime.Health{
		serviceRuntime.SmartDNS: {Service: serviceRuntime.SmartDNS, Available: true},
		serviceRuntime.Xray:     {Service: serviceRuntime.Xray, Available: true},
	}}
	restarted := New(WithStore(store), WithClock(fixedClock()), WithServiceRuntime(serviceRuntime.Runtime{Controller: restartedController}))
	status := request(t, restarted, http.MethodGet, "/api/v1/runtime/status")
	if !strings.Contains(status.Body.String(), `"capability":"xray"`) || !strings.Contains(status.Body.String(), "xray daemon missing") {
		t.Fatalf("restarted status lost xray failure evidence: %s", status.Body.String())
	}
}

func TestRuntimePlan_prefix_too_small_blocks_only_IPv6_RA(t *testing.T) {
	// Given
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-task8-ipv6-prefix?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := fixedClock()()
	if err := store.SaveConfig(ctx, configDocument(t, "interface", "lan0", map[string]any{"id": "lan0", "interface_id": "lan0", "gateway_role": "lan"}, now)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConfig(ctx, configDocument(t, "wan_link", "wan0", map[string]any{"id": "wan0", "type": "dhcp", "ipv6": map[string]any{"delegated_prefix": "2001:db8:100::/65"}}, now)); err != nil {
		t.Fatal(err)
	}
	server := New(WithStore(store), WithClock(func() time.Time { return now }))

	// When
	plan, err := server.buildRuntimePlan(ctx, "txn-ipv6-small")

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Warnings) == 0 || !strings.Contains(strings.Join(plan.Warnings, " "), "too small") {
		t.Fatalf("runtime warnings = %#v, want prefix-too-small cause", plan.Warnings)
	}
	for _, artifact := range plan.RuntimeArtifacts {
		if artifact.Service == serviceRuntime.IPv6RA {
			t.Fatalf("invalid prefix emitted RA artifact: %#v", artifact)
		}
	}
	found := false
	for _, component := range plan.Components {
		if component.Name == string(serviceRuntime.IPv6RA) {
			found = component.State == "degraded" && !component.Available
		}
	}
	if !found {
		t.Fatalf("IPv6 RA degradation missing: %#v", plan.Components)
	}
}

func TestRuntimePlan_bad_subscription_degrades_only_proxy_runtime(t *testing.T) {
	// Given
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-task8-bad-subscription?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	subscription := map[string]any{"id": "sub-1", "url": "https://provider.example/sub", "enabled": true, "node_refs": []string{"missing"}}
	if err := store.SaveConfig(ctx, configDocument(t, "proxy_subscription", "sub-1", subscription, now)); err != nil {
		t.Fatal(err)
	}
	server := New(WithStore(store), WithClock(func() time.Time { return now }), WithProxyEgress(proxy.NewProxyEgress("proxy-egress-default", "xray-tproxy-outbound")))

	// When
	plan, err := server.buildRuntimePlan(ctx, "txn-bad-subscription")

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan.Warnings, " "), "active node \"missing\" is missing") {
		t.Fatalf("runtime warnings = %#v", plan.Warnings)
	}
	hasSmartDNS := false
	for _, artifact := range plan.RuntimeArtifacts {
		switch artifact.Service {
		case serviceRuntime.SmartDNS:
			hasSmartDNS = true
		case serviceRuntime.Xray, serviceRuntime.Nftables, serviceRuntime.LinuxRouting:
			t.Fatalf("bad subscription emitted proxy artifact: %#v", artifact)
		}
	}
	if !hasSmartDNS {
		t.Fatal("bad subscription removed unrelated SmartDNS artifact")
	}
	xrayComponents := 0
	for _, component := range plan.Components {
		if component.Name == string(serviceRuntime.Xray) {
			xrayComponents++
			if component.State != "degraded" || !strings.Contains(component.Reason, "active node") {
				t.Fatalf("xray component = %#v", component)
			}
		}
	}
	if xrayComponents != 1 {
		t.Fatalf("xray component count = %d, want 1: %#v", xrayComponents, plan.Components)
	}
}

func TestRuntimeApply_failed_PPPoE_preserves_unrelated_service_receipt(t *testing.T) {
	// Given
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-task8-pppoe-failure?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := fixedClock()()
	wan := map[string]any{"id": "wan0", "type": "pppoe", "interface_id": "eth1", "username": "subscriber", "password": "secret"}
	if err := store.SaveConfig(ctx, configDocument(t, "wan_link", "wan0", wan, now)); err != nil {
		t.Fatal(err)
	}
	controller := &httpServiceController{
		applyErrs: map[serviceRuntime.ServiceName]error{serviceRuntime.PPPd: errors.New("PPPoE authentication failed")},
		health: map[serviceRuntime.ServiceName]serviceRuntime.Health{
			serviceRuntime.SmartDNS: {Service: serviceRuntime.SmartDNS, Available: true},
			serviceRuntime.PPPd:     {Service: serviceRuntime.PPPd, Reason: "PPPoE authentication failed"},
		},
	}
	server := New(WithStore(store), WithClock(fixedClock()), WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithServiceRuntime(serviceRuntime.Runtime{Controller: controller}))
	login := requestBody(t, server, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"secret"}`)

	// When
	response := authenticatedJSONRequest(t, server, http.MethodPost, "/api/v1/runtime/apply", `{}`, login.Result().Cookies()[0])

	// Then
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"status":"degraded"`) || strings.Contains(response.Body.String(), `"status":"apply_failed"`) {
		t.Fatalf("runtime apply = %d %s", response.Code, response.Body.String())
	}
	// PPPoE must establish the selected WAN before dependent DNS source routes
	// are applied; an authentication failure does not discard SmartDNS evidence.
	if got, want := strings.Join(controller.applied, ","), "pppd,smartdns"; got != want {
		t.Fatalf("attempted services = %s, want %s", got, want)
	}
	if len(controller.receiptArtifacts) != 2 || controller.receiptArtifacts[0].Service != serviceRuntime.SmartDNS || controller.receiptArtifacts[1].Service != serviceRuntime.SmartDNS || controller.receiptArtifacts[1].Path != "/etc/ly-route/dns-source-routes.conf" {
		t.Fatalf("receipt artifacts = %#v, want SmartDNS config and source route map", controller.receiptArtifacts)
	}
}
