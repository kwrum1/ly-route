package proxy

import (
	"encoding/json"
	"reflect"
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
	if !reflect.DeepEqual(compiled.NftablesCapture, NftablesCapturePlan{}) || !reflect.DeepEqual(compiled.LinuxPolicyRouting, LinuxPolicyRoutingPlan{}) {
		t.Fatalf("legacy host interception plans must be empty: %#v %#v", compiled.NftablesCapture, compiled.LinuxPolicyRouting)
	}
	if len(compiled.VPPSteering) != 3 || compiled.VPPSteering[0].Action != "handoff.to-vpp-service" || compiled.VPPSteering[0].AttachmentTarget != "vpp.proxy-service" {
		t.Fatalf("VPP steering = %#v", compiled.VPPSteering)
	}
	if compiled.XrayRuntime.ListenAddress != "127.0.0.1" || compiled.XrayRuntime.ListenPort != 12345 || compiled.XrayRuntime.ConfigPayload.Inbounds[0].Settings.FollowRedirect {
		t.Fatalf("Xray service listener = %#v", compiled.XrayRuntime)
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
	data, err := json.Marshal(NewProxyEgress("proxy-media", "xray-vpp-service"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "physical_interface") || strings.Contains(string(data), "pppoe_password") {
		t.Fatalf("proxy JSON leaked physical WAN fields: %s", data)
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
