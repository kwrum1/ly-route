package vpp

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/nat"
)

func TestDNSServiceNetworkUsesStableDedicatedVPPHandoff(t *testing.T) {
	network, err := DNSServiceNetworkForUpstreamID("dns-media", "wan-primary", []string{"9.9.9.9", "1.1.1.1", "9.9.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := DNSServiceNetworkForUpstreamID("dns-media", "wan-primary", []string{"1.1.1.1", "9.9.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(network, again) {
		t.Fatalf("network is not stable: %#v != %#v", network, again)
	}
	if !strings.HasPrefix(network.VPPInterface, "lydns") || !strings.HasPrefix(network.HostInterface, "lydnsh") || network.TapID < dnsServiceTapIDBase || network.TapID >= dnsServiceTapIDBase+dnsServiceTapIDSpan {
		t.Fatalf("DNS service network identities are invalid: %#v", network)
	}
	if network.MTU != defaultDNSServiceMTU {
		t.Fatalf("DNS service network default MTU = %d", network.MTU)
	}
	if err := BindDNSServiceNetwork(&network, "192.0.2.1 lyroute-wan0", 1492); err != nil {
		t.Fatal(err)
	}
	plan := validPlan(t, "dns-service-network")
	plan.DNSServiceNetworks = []DNSServiceNetwork{network}
	operations := mustBuildOperations(t, plan)
	assertOperationCommand(t, operations, "vpp.dns-service.network", "create tap id")
	assertOperationCommand(t, operations, "vpp.dns-service.network", "set interface mtu packet 1492 "+network.VPPInterface)
	assertOperationCommand(t, operations, "vpp.dns-service.network", "ip route add table")
	assertOperationCommand(t, operations, "vpp.dns-service.network", "set interface nat44 in "+network.VPPInterface+" out lyroute-wan0")
	plan.NAT.Behavior = nat.BehaviorFullCone
	operations = mustBuildOperations(t, plan)
	assertOperationCommand(t, operations, "vpp.dns-service.network", "set interface nat44 ei in "+network.VPPInterface+" out lyroute-wan0")
}

func TestDNSServiceNetworkRejectsNonIPResolver(t *testing.T) {
	if _, err := DNSServiceNetworkForUpstreamID("dns-bad", "wan-primary", []string{"resolver.example"}); err == nil {
		t.Fatal("expected non-IP resolver rejection")
	}
}

func TestDNSServiceNetworkRejectsInvalidEffectiveMTU(t *testing.T) {
	network, err := DNSServiceNetworkForUpstreamID("dns-primary", "wan-primary", []string{"9.9.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	if err := BindDNSServiceNetwork(&network, "lyroute-wan0", 500); err == nil || !strings.Contains(err.Error(), "invalid MTU") {
		t.Fatalf("invalid MTU error = %v", err)
	}
}

func TestDNSServiceNetworkReadbackRequiresInputNAT(t *testing.T) {
	network, err := DNSServiceNetworkForUpstreamID("dns-primary", "wan-primary", []string{"9.9.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	results := []VPPCTLCommandResult{
		{Command: "show interface address " + network.VPPInterface, Stdout: network.VPPInterface + " " + network.VPPAddress},
		{Command: fmt.Sprintf("show ip fib table %d", network.TableID), Stdout: fmt.Sprintf("table %d", network.TableID)},
		{Command: "show nat44 interfaces", Stdout: "pppoe_session0 out"},
		{Command: "show nat44 ei interfaces", Stdout: ""},
		{Command: "show tap", Stdout: network.VPPInterface},
	}
	if err := verifyDNSServiceNetworkReadback(network, results); err == nil || !strings.Contains(err.Error(), "missing its NAT44 input") {
		t.Fatalf("missing input NAT readback error = %v", err)
	}
}
