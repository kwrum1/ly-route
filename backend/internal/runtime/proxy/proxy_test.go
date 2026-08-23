package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProxyEgressUsesVPPServiceHandoff(t *testing.T) {
	egress := NewProxyEgress("proxy-media", "xray-vpp-service")
	if err := ValidateEgress(egress); err != nil {
		t.Fatal(err)
	}
	if egress.CapturePath != VPPService || egress.Handoff != VPPToService || egress.ListenerMode != VPPServiceListener {
		t.Fatalf("runtime path = %#v", egress)
	}
	compiled, err := CompileEgress(egress)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.NftablesCapture.Table != "ly_route_proxy_capture" || compiled.NftablesCapture.IngressInterface != compiled.ServiceNetwork.IngressHostInterface {
		t.Fatalf("proxy capture is not scoped to the VPP service TAP: %#v", compiled.NftablesCapture)
	}
	if compiled.LinuxPolicyRouting.Network.EgressID != egress.ID || compiled.LinuxPolicyRouting.DefaultRoute.Device != compiled.ServiceNetwork.EgressHostInterface {
		t.Fatalf("proxy return path is not bound to the VPP service network: %#v", compiled.LinuxPolicyRouting)
	}
	if len(compiled.VPPSteering) != 1 || compiled.VPPSteering[0].TargetKind != "vpp.proxy-service.network" || compiled.VPPSteering[0].Action != "handoff.to-vpp-service" || compiled.VPPSteering[0].AttachmentTarget != "vpp.proxy-service" {
		t.Fatalf("VPP steering = %#v", compiled.VPPSteering)
	}
	if compiled.XrayRuntime.ListenAddress != "0.0.0.0" || compiled.XrayRuntime.ListenPort != compiled.ServiceNetwork.ListenerPort || !compiled.XrayRuntime.ConfigPayload.Inbounds[0].Settings.FollowRedirect {
		t.Fatalf("Xray service listener = %#v", compiled.XrayRuntime)
	}
	if compiled.XrayRuntime.ConfigPayload.Routing != nil {
		t.Fatalf("fixed-node Xray runtime contains business routing or DNS: %#v", compiled.XrayRuntime.ConfigPayload)
	}
	payload, err := json.Marshal(compiled.XrayRuntime.ConfigPayload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), `"routing"`) || strings.Contains(string(payload), `"dns"`) {
		t.Fatalf("fixed-node Xray JSON contains business routing or DNS: %s", payload)
	}
	if got := compiled.XrayRuntime.ConfigPayload.Inbounds[0].Settings.Network; got != "tcp,udp" {
		t.Fatalf("proxy service interface is not full-proxy TCP/UDP: %q", got)
	}
	sockopt, _ := compiled.XrayRuntime.ConfigPayload.Outbounds[0].StreamSettings["sockopt"].(map[string]any)
	if sockopt["mark"] != compiled.ServiceNetwork.OutboundMark {
		t.Fatalf("Xray outbound mark = %#v", sockopt)
	}
}

func TestProxyEgressRejectsLegacyHostPaths(t *testing.T) {
	for name, mutate := range map[string]func(*Egress){
		"tproxy":            func(e *Egress) { e.CapturePath = TProxy },
		"host handoff":      func(e *Egress) { e.Handoff = VPPToHost },
		"tap handoff":       func(e *Egress) { e.Handoff = VPPToTap },
		"dokodemo listener": func(e *Egress) { e.ListenerMode = DokodemoDoor },
	} {
		t.Run(name, func(t *testing.T) {
			egress := NewProxyEgress("proxy-media", "xray-vpp-service")
			mutate(&egress)
			if err := ValidateEgress(egress); err == nil {
				t.Fatal("legacy host interception path was accepted")
			}
		})
	}
}

func TestProxyEgressJSONDoesNotExposePhysicalWAN(t *testing.T) {
	egress := NewProxyEgress("proxy-media", "xray-vpp-service")
	egress.UnderlayWANID = "wan-pppoe"
	data, err := json.Marshal(egress)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "physical_interface") || strings.Contains(string(data), "pppoe_password") {
		t.Fatalf("proxy JSON leaked physical WAN fields: %s", data)
	}
	var decoded Egress
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	compiled, err := CompileEgress(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.UnderlayWANID != "wan-pppoe" || compiled.UnderlayWANID != "wan-pppoe" || compiled.VPPSteering[0].UnderlayWANID != "wan-pppoe" {
		t.Fatalf("proxy underlay was not preserved through runtime compilation: decoded=%#v compiled=%#v", decoded, compiled)
	}
}

func TestCompileNodeOutboundSupportsVLESSReality(t *testing.T) {
	outbound, err := CompileNodeOutbound(Node{ID: "kurumi", Protocol: "vless", Address: "example.test", Port: 443, Secret: "redacted-secret", Settings: map[string]any{
		"encryption": "none", "flow": "xtls-rprx-vision", "network": "tcp", "security": "reality",
		"realitySettings": map[string]any{"publicKey": "key", "shortId": "98", "serverName": "www.oracle.com", "fingerprint": "chrome"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if outbound.Protocol != "vless" || outbound.StreamSettings["security"] != "reality" {
		t.Fatalf("compiled outbound = %#v", outbound)
	}
}

func TestServiceNetworkTapIDsFitVPPExplicitTapRange(t *testing.T) {
	network := ServiceNetworkForEgressID("proxy-egress-acceptance")
	if network.IngressTapID < proxyIngressTapIDBase || network.IngressTapID >= proxyIngressTapIDBase+proxyTapIDSpan {
		t.Fatalf("ingress TAP id %d is outside the VPP-safe range", network.IngressTapID)
	}
	if network.EgressTapID < proxyEgressTapIDBase || network.EgressTapID >= proxyEgressTapIDBase+proxyTapIDSpan {
		t.Fatalf("egress TAP id %d is outside the VPP-safe range", network.EgressTapID)
	}
	if network.IngressTapID == network.EgressTapID {
		t.Fatalf("TAP peers share id %d", network.IngressTapID)
	}
}

func TestRedactSubscriptionPreservesAdaptiveSelection(t *testing.T) {
	subscription := Subscription{
		ID:        "main",
		URL:       "https://user:secret@example.test/subscription",
		Enabled:   true,
		Selection: SelectionMode("adaptive"),
		Strategy:  AdaptiveSubscriptionStrategy,
		TopN:      3,
	}

	redacted, err := RedactSubscription(subscription)
	if err != nil {
		t.Fatal(err)
	}
	if redacted.Selection != subscription.Selection || redacted.Strategy != subscription.Strategy || redacted.TopN != subscription.TopN {
		t.Fatalf("adaptive selection lost during redaction: %#v", redacted)
	}
	if strings.Contains(redacted.URL, "secret") {
		t.Fatalf("subscription credential leaked: %q", redacted.URL)
	}
}
