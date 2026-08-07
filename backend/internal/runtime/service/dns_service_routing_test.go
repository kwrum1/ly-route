package service

import (
	"fmt"
	"strings"
	"testing"

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
	artifacts, err := RenderDNSServiceRouting([]vpp.DNSServiceNetwork{network})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Service != LinuxRouting {
		t.Fatalf("DNS service routing artifacts = %#v", artifacts)
	}
	content := artifacts[0].Content
	rulePriority := 10000 + network.TapID
	for _, required := range []string{"wait_vpp_underlay lyroute-wan0", "ip table add", "ip route add table", "show ip fib table", "ip link set dev " + network.HostInterface + " mtu 1492 up", "ip address replace " + network.HostAddress + "/30 dev " + network.HostInterface, "ip route replace default via " + network.VPPAddress + " dev " + network.HostInterface + " table " + fmt.Sprint(network.TableID), "ip rule add from " + network.HostAddress + "/32 lookup " + fmt.Sprint(network.TableID) + " priority " + fmt.Sprint(rulePriority), "ip route replace 9.9.9.9/32 via " + network.VPPAddress + " dev " + network.HostInterface, "warm_vpp_resolver " + network.HostInterface + " 9.9.9.9", "while [ \"$attempt\" -le 5 ]", "DNS_STATE_FILE"} {
		if !strings.Contains(content, required) {
			t.Fatalf("DNS service routing is missing %q: %s", required, content)
		}
	}
	for _, forbidden := range []string{"ip rule add fwmark"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("DNS service routing leaked generic policy routing %q: %s", forbidden, content)
		}
	}
	expected, err := parsePolicyRoutingExpectation(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(expected.Rules) != 1 || expected.Rules[0].Source != network.HostAddress || expected.Rules[0].Table != network.TableID || expected.Rules[0].Priority != rulePriority || len(expected.Routes) != 2 || expected.Routes[0].Destination != "default" || expected.Routes[0].Table != network.TableID || expected.Routes[1].Destination != "9.9.9.9" || expected.Routes[1].Device != network.HostInterface {
		t.Fatalf("DNS service routing expectation = %#v", expected)
	}
}
