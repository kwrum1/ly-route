package vpp

import (
	"reflect"
	"strings"
	"testing"
)

func TestManagementLCPPairs(t *testing.T) {
	output := `lcp default netns '<unset>'
itf-pair: [0] lyroute-eth0 tap4096 lymgmt0 2 type tap
itf-pair: [1] lyroute-eth1 tap4097 other0 3 type tap
`
	if got := managementLCPPairs(output, "lymgmt0"); !reflect.DeepEqual(got, []string{"lyroute-eth0"}) {
		t.Fatalf("management pairs = %#v", got)
	}
	if !managementLCPPresent(output, "lyroute-eth0", "lymgmt0") {
		t.Fatal("management pair was not found")
	}
}

func TestIPv4BroadcastLocalRoutePresent(t *testing.T) {
	if !ipv4BroadcastLocalRoutePresent("255.255.255.255/32\n  [@12]: dpo-receive: 0.0.0.0 on local0") {
		t.Fatal("limited broadcast local route was not recognized")
	}
	if ipv4BroadcastLocalRoutePresent("255.255.255.255/32\n  [@0]: dpo-drop ip4") {
		t.Fatal("drop route was accepted as a local route")
	}
}

func TestLANDHCPBroadcastPolicyReadbackUsesReceivePath(t *testing.T) {
	policy := lanDHCPBroadcastBypassPolicy("lyroute-ens192")
	if policy.PolicyID != lanDHCPBroadcastBypassPolicyID || policy.Priority != 1 || policy.NextHop != "local" {
		t.Fatalf("DHCP bypass policy = %#v", policy)
	}
	if policy.Match.DestinationIP != "255.255.255.255" || policy.Match.DestinationPort != 67 {
		t.Fatalf("DHCP bypass match = %#v", policy.Match)
	}
	output := `abf:[5]: policy:65000 acl:5
     path-list:[125] locks:1 flags:shared,no-uRPF, uRPF-list: None
      path:[148] pl-index:125 ip4 weight=1 pref=0 receive:  oper-flags:resolved, cfg-flags:local,
        [@0]: dpo-receive
`
	observed, err := parseObservedServiceChainABFPolicy(output)
	if err != nil {
		t.Fatalf("receive-path readback failed: %v", err)
	}
	if observed.ID != policy.PolicyID || observed.ACLID != 5 || observed.AddressFamily != "ip4" || observed.NextHop != "local" || observed.ServiceInterface != "" {
		t.Fatalf("receive-path readback = %#v", observed)
	}
	if !strings.Contains(serviceChainACLTag(policy), "lan_dhcp_broadcast") {
		t.Fatalf("unexpected DHCP bypass tag: %s", serviceChainACLTag(policy))
	}
}
