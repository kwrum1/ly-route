package api

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/proxy"
)

func TestProxyEgressResourceKeepsProxySemanticsInWANPresentation(t *testing.T) {
	resource := ProxyEgress(proxy.NewProxyEgress("proxy-media", "xray-tproxy-outbound"), "Media proxy", true)

	if resource.SemanticType != proxy.ProxyEgress {
		t.Fatalf("semantic type = %q, want %q", resource.SemanticType, proxy.ProxyEgress)
	}
	if resource.DisplayList != "wan" {
		t.Fatalf("display list = %q, want wan", resource.DisplayList)
	}
	if resource.Kind != ResourceKindProxyEgress {
		t.Fatalf("kind = %q, want %q", resource.Kind, ResourceKindProxyEgress)
	}

	payload := mustMarshal(t, resource)
	for _, required := range []string{"proxy_egress", "wan", "xray", "vpp_service_interface", "vpp_to_service"} {
		if !strings.Contains(payload, required) {
			t.Fatalf("proxy egress resource missing %q: %s", required, payload)
		}
	}
}

func TestProxyEgressWANRowRejectsPhysicalWANPresentation(t *testing.T) {
	physical := proxy.Egress{
		ID:             "wan0",
		SemanticType:   proxy.PhysicalWAN,
		DisplayList:    proxy.WANDisplayList,
		RuntimeProfile: "xray-tproxy-outbound",
		CapturePath:    proxy.TProxy,
		Engine:         proxy.Xray,
		Handoff:        proxy.VPPToHost,
	}

	if _, err := ProxyEgressWANRow(physical, "Physical WAN", true); !errors.Is(err, proxy.ErrInvalidEgress) {
		t.Fatalf("ProxyEgressWANRow error = %v, want ErrInvalidEgress", err)
	}
}

func TestProxyEgressResourceOmitsHiddenAndUnsupportedFields(t *testing.T) {
	payload := mustMarshal(t, ProxyEgress(proxy.NewProxyEgress("proxy-media", "xray-tproxy-outbound"), "Media proxy", true))

	assertForbiddenFieldsAbsent(t, payload)
}

func TestFlowIntentResourceOnlyExposesV1Actions(t *testing.T) {
	resource := FlowIntent(flow.NewIntent("default", []flow.Rule{
		flow.NewRule("classify-video", flow.RuleGranularity, flow.Classify("video")),
		flow.NewClassRule("remark-bulk", "bulk", flow.Remark("AF11")),
		flow.NewClassRule("police-bulk", "bulk", flow.Police(10_000_000, 1_000_000)),
	}))
	payload := mustMarshal(t, resource)

	for _, required := range []string{"classify", "remark", "policer", "rule", "class"} {
		if !strings.Contains(payload, required) {
			t.Fatalf("flow resource missing %q: %s", required, payload)
		}
	}
	assertForbiddenFieldsAbsent(t, payload)
}

func TestFlowIntentResourceRejectsHiddenAndUnsupportedActions(t *testing.T) {
	for name, kind := range map[string]flow.ActionKind{
		"connection limit":  "connection_limit",
		"connection-limit":  "connection-limit",
		"max connections":   "max_connections",
		"max-connections":   "max-connections",
		"queue placeholder": "queue",
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered == nil {
					t.Fatal("FlowIntent accepted hidden or unsupported action")
				}
			}()

			_ = FlowIntent(flow.NewIntent("default", []flow.Rule{
				flow.NewRule("classify-video", flow.RuleGranularity, flow.Classify("video"), flow.Action{Kind: kind}),
				flow.NewClassRule("remark-bulk", "bulk", flow.Remark("AF11")),
				flow.NewClassRule("police-bulk", "bulk", flow.Police(10_000_000, 1_000_000)),
			}))
		})
	}
}

func TestUnavailableRuntimeCapabilitySurfacesDegradedState(t *testing.T) {
	resource := FlowIntent(flow.NewIntent("default", []flow.Rule{
		flow.NewRule("classify-video", flow.RuleGranularity, flow.Classify("video")),
	}), RuntimeCapability{Name: "vpp.qos.policer", Available: false, Reason: "missing runtime capability"})

	if len(resource.Capabilities) != 1 {
		t.Fatalf("capability count = %d, want 1", len(resource.Capabilities))
	}
	capability := resource.Capabilities[0]
	if capability.Available || capability.State != CapabilityDegraded || capability.Reason == "" {
		t.Fatalf("capability = %#v, want explicit degraded unsupported state", capability)
	}

	payload := mustMarshal(t, resource)
	for _, required := range []string{"capabilities", "degraded", "missing runtime capability"} {
		if !strings.Contains(payload, required) {
			t.Fatalf("degraded capability payload missing %q: %s", required, payload)
		}
	}
	assertForbiddenFieldsAbsent(t, payload)
}

func mustMarshal(t *testing.T, value any) string {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func assertForbiddenFieldsAbsent(t *testing.T, payload string) {
	t.Helper()
	for _, forbidden := range []string{"physical_interface_id", "physical_interface_identity", "interface_id", "interface_name", "mac", "mac_address", "mac_negotiation", "link_speed", "speed", "dhcp_client", "pppoe", "pppoe_username", "pppoe_password", "connection_limit", "connection-limit", "max_connections", "max-connections", "bridge", "bridge_mode", "queue", "sqm", "cake", "fq_codel", "fq-codel"} {
		if strings.Contains(payload, `"`+forbidden+`"`) {
			t.Fatalf("payload leaked forbidden field %q: %s", forbidden, payload)
		}
	}
}
