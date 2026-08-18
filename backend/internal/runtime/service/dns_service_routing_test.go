package service

import (
	"fmt"
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/vpp"
)

func TestRenderDNSServiceRoutingPinsBootstrapAndDoHEndpointsToWAN(t *testing.T) {
	network, err := vpp.DNSServiceNetworkForUpstreamID("dns-primary", "wan-primary", []string{"9.9.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	if err := vpp.BindDNSServiceNetwork(&network, "192.0.2.1 lyroute-wan0", 1492); err != nil {
		t.Fatal(err)
	}
	artifacts, err := RenderDNSServiceRouting(nat.BehaviorEndpointDependent, []vpp.DNSServiceNetwork{network})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Service != LinuxRouting {
		t.Fatalf("DNS service routing artifacts = %#v", artifacts)
	}
	content := artifacts[0].Content
	rulePriority := 10000 + network.TapID
	markPriority := 20000 + network.TapID
	mark := fmt.Sprintf("0x%x", network.SocketMark)
	for _, required := range []string{"wait_vpp_underlay lyroute-wan0", "ip table add", "ip route add table", "show ip fib table", "ip link set dev " + network.HostInterface + " mtu 1492 up", "ip address replace " + network.HostAddress + "/30 dev " + network.HostInterface, "ip route replace default via " + network.VPPAddress + " dev " + network.HostInterface + " table " + fmt.Sprint(network.TableID), "ip rule add from " + network.HostAddress + "/32 lookup " + fmt.Sprint(network.TableID) + " priority " + fmt.Sprint(rulePriority), "ip rule add fwmark " + mark + "/0xffffffff lookup " + fmt.Sprint(network.TableID) + " priority " + fmt.Sprint(markPriority), "ip route replace 9.9.9.9/32 via " + network.VPPAddress + " dev " + network.HostInterface + " table " + fmt.Sprint(network.TableID), "warm_vpp_resolver " + network.HostAddress + " 9.9.9.9", "while [ \"$attempt\" -le 5 ]", "DNS_STATE_FILE"} {
		if !strings.Contains(content, required) {
			t.Fatalf("DNS service routing is missing %q: %s", required, content)
		}
	}
	expected, err := parsePolicyRoutingExpectation(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(expected.Rules) != 2 || expected.Rules[0].Source != network.HostAddress || expected.Rules[0].Table != network.TableID || expected.Rules[0].Priority != rulePriority || expected.Rules[1].Mark != mark || expected.Rules[1].Mask != "0xffffffff" || expected.Rules[1].Table != network.TableID || expected.Rules[1].Priority != markPriority || len(expected.Routes) != 2 || expected.Routes[0].Destination != "default" || expected.Routes[0].Table != network.TableID || expected.Routes[1].Destination != "9.9.9.9" || expected.Routes[1].Device != network.HostInterface || expected.Routes[1].Table != network.TableID {
		t.Fatalf("DNS service routing expectation = %#v", expected)
	}
}

func TestRenderDNSServiceRoutingAllowsSharedResolverAcrossWANs(t *testing.T) {
	first, err := vpp.DNSServiceNetworkForUpstreamID("dns-domestic", "wan-domestic", []string{"223.5.5.5"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := vpp.DNSServiceNetworkForUpstreamID("dns-foreign", "wan-foreign", []string{"223.5.5.5"})
	if err != nil {
		t.Fatal(err)
	}
	if err := vpp.BindDNSServiceNetwork(&first, "192.0.2.1 lyroute-wan-domestic", 1492); err != nil {
		t.Fatal(err)
	}
	if err := vpp.BindDNSServiceNetwork(&second, "198.51.100.1 lyroute-wan-foreign", 1492); err != nil {
		t.Fatal(err)
	}
	artifacts, err := RenderDNSServiceRouting(nat.BehaviorEndpointDependent, []vpp.DNSServiceNetwork{first, second})
	if err != nil {
		t.Fatal(err)
	}
	content := artifacts[0].Content
	for _, network := range []vpp.DNSServiceNetwork{first, second} {
		want := "ip route replace 223.5.5.5/32 via " + network.VPPAddress + " dev " + network.HostInterface + " table " + fmt.Sprint(network.TableID)
		if !strings.Contains(content, want) {
			t.Fatalf("DNS resolver route missing source-policy table: %q\n%s", want, content)
		}
	}
	expected, err := parsePolicyRoutingExpectation(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(expected.Routes) != 4 {
		t.Fatalf("route expectations = %#v, want default and resolver route for each DNS service network", expected.Routes)
	}
}

func TestRenderDNSServiceRoutingFullConeUsesOnlyEINATAndAddsReturnRoute(t *testing.T) {
	network, err := vpp.DNSServiceNetworkForUpstreamID("dns-full-cone", "wan-primary", []string{"9.9.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	if err := vpp.BindDNSServiceNetwork(&network, "192.0.2.1 pppoe_session1", 1492); err != nil {
		t.Fatal(err)
	}
	artifacts, err := RenderDNSServiceRouting(nat.BehaviorFullCone, []vpp.DNSServiceNetwork{network})
	if err != nil {
		t.Fatal(err)
	}
	content := artifacts[0].Content
	for _, required := range []string{
		"nat44 plugin disable",
		"nat44 ei plugin enable",
		"set interface nat44 ei in " + network.VPPInterface + " out pppoe_session1",
		"ip route add " + network.HostAddress + "/32 via " + network.HostAddress + " " + network.VPPInterface,
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("full-cone DNS service routing is missing %q: %s", required, content)
		}
	}
	if strings.Contains(content, "set interface nat44 in "+network.VPPInterface+" out pppoe_session1") {
		t.Fatalf("full-cone DNS service routing must not enable NAT44-ED: %s", content)
	}
}
