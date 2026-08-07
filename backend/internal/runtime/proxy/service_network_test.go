package proxy

import "testing"

func TestBindServiceNetworkUsesEffectiveUnderlayMTU(t *testing.T) {
	compiled, err := CompileEgress(NewProxyEgress("proxy-test", "xray-vpp-service"))
	if err != nil {
		t.Fatal(err)
	}
	if err := BindServiceNetwork(&compiled, "pppoe_session0", []string{"10.0.0.0/24"}, 1460); err != nil {
		t.Fatal(err)
	}
	if compiled.ServiceNetwork.MTU != 1460 || compiled.LinuxPolicyRouting.Network.MTU != 1460 || compiled.VPPSteering[0].ServiceNetwork.MTU != 1460 {
		t.Fatalf("effective MTU was not propagated: %#v", compiled.ServiceNetwork)
	}
}

func TestBindServiceNetworkRejectsInvalidMTU(t *testing.T) {
	compiled, err := CompileEgress(NewProxyEgress("proxy-test", "xray-vpp-service"))
	if err != nil {
		t.Fatal(err)
	}
	if err := BindServiceNetwork(&compiled, "pppoe_session0", nil, 500); err == nil {
		t.Fatal("undersized service MTU was accepted")
	}
}
