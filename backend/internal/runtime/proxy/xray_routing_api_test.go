package proxy

import "testing"

func TestEnableXrayRoutingAPIIsLoopbackOnlyAndRoutesToInternalService(t *testing.T) {
	payload := XrayConfigPayload{Routing: &XrayRouting{Rules: []XrayRoutingRule{{Type: "field", InboundTags: []string{"proxy-in"}, BalancerTag: "fastest"}}}}
	if err := EnableXrayRoutingAPI(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.API == nil || payload.API.Tag != XrayRoutingAPITag || len(payload.API.Services) != 1 || payload.API.Services[0] != "RoutingService" {
		t.Fatalf("unexpected Xray API config: %#v", payload.API)
	}
	if len(payload.Inbounds) != 1 || payload.Inbounds[0].Listen != "127.0.0.1" || payload.Inbounds[0].Port != 10085 || payload.Inbounds[0].Settings.Address != "127.0.0.1" {
		t.Fatalf("routing API is not loopback-only: %#v", payload.Inbounds)
	}
	if len(payload.Routing.Rules) != 2 || payload.Routing.Rules[0].OutboundTag != XrayRoutingAPITag || payload.Routing.Rules[1].BalancerTag != "fastest" {
		t.Fatalf("unexpected routing API rules: %#v", payload.Routing.Rules)
	}
}

func TestEnableXrayRoutingAPIRejectsListenerConflict(t *testing.T) {
	payload := XrayConfigPayload{Inbounds: []XrayInbound{{Tag: "existing", Listen: "127.0.0.1", Port: 10085}}, Routing: &XrayRouting{}}
	if err := EnableXrayRoutingAPI(&payload); err == nil {
		t.Fatal("routing API listener conflict was accepted")
	}
}
