package vpp

import (
	"net/netip"
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestPreNATLANPrefixUsesVPPDataInterfaceAddress(t *testing.T) {
	prefix, err := preNATLANPrefix("lyroute-ens34 (up):\n  L3 192.168.50.1/24\n  L3 2001:db8:100::1/64\n")
	if err != nil {
		t.Fatalf("preNATLANPrefix() error = %v", err)
	}
	if want := netip.MustParsePrefix("192.168.50.0/24"); prefix != want {
		t.Fatalf("preNATLANPrefix() = %s, want %s", prefix, want)
	}
}

func TestBuildPreNATRoutePolicyCommandsUsesDataLANPrefix(t *testing.T) {
	spec := routePolicyVPPCTLSpec{
		policyID: 101,
		tableID:  92258,
		priority: 100,
		ingress:  "lyroute-ens34",
		apply:    true,
		policy: trafficpolicy.RoutePolicy{
			ID:       "proxy-test",
			Priority: 100,
			Action:   "route",
			Path:     &trafficpolicy.WANPath{VPPInterface: "lypxinffdc88"},
			Match: trafficpolicy.Match{
				Sources:      []string{"192.168.50.104"},
				Destinations: []string{"any"},
				Protocols:    []string{"tcp"},
				DestPorts:    []string{"443"},
			},
		},
	}
	commands, applied, err := buildPreNATRoutePolicyCommands(spec, netip.MustParsePrefix("192.168.50.1/24"))
	if err != nil || !applied {
		t.Fatalf("buildPreNATRoutePolicyCommands() = %v, %t, %v", commands, applied, err)
	}
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, "lan-prefix 192.168.50.0/24") {
		t.Fatalf("commands missing data LAN prefix: %s", joined)
	}
	if !strings.Contains(joined, "source 192.168.50.104/32 destination 0.0.0.0/0 protocol tcp sport 0-65535 dport 443-443 table 92258") {
		t.Fatalf("commands missing normalized policy: %s", joined)
	}
	if !strings.Contains(joined, "table 92258 skip-nat") {
		t.Fatalf("proxy policy must bypass NAT before entering TPROXY: %s", joined)
	}
	dhcpBypass := "add id 65000 priority 0 source 0.0.0.0/0 destination 0.0.0.0/0 protocol udp sport 68-68 dport 67-67 bypass"
	if !strings.Contains(joined, dhcpBypass) {
		t.Fatalf("all DHCP client requests must bypass pre-NAT policy routing: %s", joined)
	}
	if strings.Index(joined, dhcpBypass) > strings.Index(joined, "add id 101 priority") {
		t.Fatalf("DHCP bypass must be installed before user route policies: %s", joined)
	}
}

func TestBuildPreNATRoutePolicyCommandsKeepsNATForOrdinaryWAN(t *testing.T) {
	spec := routePolicyVPPCTLSpec{
		policyID: 102,
		tableID:  93153,
		priority: 10,
		ingress:  "lyroute-ens34",
		apply:    true,
		policy: trafficpolicy.RoutePolicy{
			ID:       "direct-test",
			Priority: 10,
			Action:   "nat",
			Path:     &trafficpolicy.WANPath{VPPInterface: "pppoe_session0"},
			Match:    trafficpolicy.Match{Sources: []string{"192.168.50.100/32"}},
		},
	}
	commands, applied, err := buildPreNATRoutePolicyCommands(spec, netip.MustParsePrefix("192.168.50.1/24"))
	if err != nil || !applied {
		t.Fatalf("buildPreNATRoutePolicyCommands() = %v, %t, %v", commands, applied, err)
	}
	if joined := strings.Join(commands, "\n"); strings.Contains(joined, "skip-nat") {
		t.Fatalf("ordinary WAN policy must retain NAT: %s", joined)
	}
}

func TestBuildPreNATRoutePolicyCommandsSupportsWANGroupWithoutPath(t *testing.T) {
	spec := routePolicyVPPCTLSpec{
		policyID: 10886,
		tableID:  52110,
		ingress:  "lyroute-ens34",
		apply:    true,
		policy: trafficpolicy.RoutePolicy{
			ID:       "dual-wan-client-route",
			Priority: 50,
			Action:   "nat",
			Egress:   "acceptance-dual-wan",
			Match: trafficpolicy.Match{
				Sources:      []string{"192.168.50.200/32"},
				Destinations: []string{"any"},
				Protocols:    []string{"any"},
			},
		},
	}
	commands, applied, err := buildPreNATRoutePolicyCommands(spec, netip.MustParsePrefix("192.168.50.1/24"))
	if err != nil || !applied {
		t.Fatalf("buildPreNATRoutePolicyCommands() = %v, %t, %v", commands, applied, err)
	}
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, "add id 10886 priority 50 source 192.168.50.200/32 destination 0.0.0.0/0 protocol any sport 0-65535 dport 0-65535 table 52110") {
		t.Fatalf("WAN-group policy did not install its pre-NAT classifier: %s", joined)
	}
	if strings.Contains(joined, "skip-nat") {
		t.Fatalf("WAN-group policy must retain NAT: %s", joined)
	}
}

func TestNATRoutePolicyUsesNativePreNATPlanAndReadback(t *testing.T) {
	policy := trafficpolicy.RoutePolicy{
		ID:       "nat-client-route",
		Priority: 1,
		Action:   "nat",
		Egress:   "wan-pppoe",
		Path:     &trafficpolicy.WANPath{VPPInterface: "pppoe_session0", NextHop: "10.67.0.1"},
		Match: trafficpolicy.Match{
			Sources:      []string{"192.168.50.100/32"},
			Destinations: []string{"0.0.0.0/0"},
			Protocols:    []string{"any"},
			SourcePorts:  []string{"any"},
			DestPorts:    []string{"any"},
		},
	}
	options := routePolicyCommandOptions{localDestinations: []string{"192.168.50.0/24"}}
	plan, ok := compileRoutePolicyRadixPlan(policy, nil, options)
	if !ok || plan.skipNAT {
		t.Fatalf("compileRoutePolicyRadixPlan() = %#v, %t; want native NAT-preserving plan", plan, ok)
	}
	commands := strings.Join(routePolicyCommandsWithOptions(policy, nil, options), "\n")
	if !strings.Contains(commands, "set ly-route pre-nat-route add") {
		t.Fatalf("NAT route policy did not use the native classifier: %s", commands)
	}
	if strings.Contains(commands, "acl-plugin") || strings.Contains(commands, "abf policy") || strings.Contains(commands, "skip-nat") {
		t.Fatalf("NAT route policy retained legacy ABF or bypassed NAT: %s", commands)
	}
}

func TestNativePreNATRoutePolicyLifecycleRecognizesLegacyAndNativePlans(t *testing.T) {
	policy := trafficpolicy.RoutePolicy{
		ID:       "native-route",
		Priority: 10,
		Action:   "route",
		Path:     &trafficpolicy.WANPath{VPPInterface: "pppoe_session0"},
		Match:    trafficpolicy.Match{Sources: []string{"any"}, Destinations: []string{"any"}},
	}
	if !routePolicySupportsNativePreNAT(policy) {
		t.Fatal("ordinary IPv4 route policy must use the native pre-NAT lifecycle")
	}
	legacyDelete := Operation{
		Name:           "vpp.route-policy.pre-delete",
		Resource:       policy.ID,
		Payload:        policy,
		VPPCtlCommands: deleteRoutePolicyCommands(policy.ID),
	}
	if !routePolicyNativeDeleteOperation(legacyDelete) {
		t.Fatal("legacy full-rebuild cleanup must be handled by the native lifecycle")
	}
	if commands := routePolicyNativeTableCommands(legacyDelete); len(commands) != 0 {
		t.Fatalf("legacy delete must not forward ABF commands: %v", commands)
	}
	nativeApply := Operation{
		Name:     "vpp.route-policy",
		Resource: policy.ID,
		Payload:  policy,
		VPPCtlCommands: []string{
			"?set acl-plugin acl index 201 permit src 0.0.0.0/0 dst 0.0.0.0/0 proto any sport 0 dport 0",
			"?ip table add 50101",
			"?set ip flow-hash table 50101 src dst sport dport proto",
			"?ip route add table 50101 0.0.0.0/0 via pppoe_session0",
			"set ly-route pre-nat-route interface lyroute-ens34 lan-prefix 192.168.50.0/24",
			"set ly-route pre-nat-route add id 101 priority 10 source 0.0.0.0/0 destination 0.0.0.0/0 protocol any sport 0-65535 dport 0-65535 table 50101",
			"?abf policy add id 101 acl 201 via pppoe_session0",
		},
	}
	spec, err := parseRoutePolicyVPPCTLSpec(nativeApply)
	if err != nil {
		t.Fatalf("parseRoutePolicyVPPCTLSpec() error = %v", err)
	}
	if !spec.apply || spec.ingress != "lyroute-ens34" {
		t.Fatalf("native plan spec = %#v, want active plan on lyroute-ens34", spec)
	}
	commands := strings.Join(routePolicyNativeTableCommands(nativeApply), "\n")
	if strings.Contains(commands, "abf ") || strings.Contains(commands, "acl-plugin") || strings.Contains(commands, "pre-nat-route") {
		t.Fatalf("native table setup forwarded legacy state: %s", commands)
	}
	if !strings.Contains(commands, "ip table add 50101") || !strings.Contains(commands, "ip route add table 50101") {
		t.Fatalf("native table setup lost FIB provisioning: %s", commands)
	}
}

func TestParseRoutePolicyVPPCTLSpecAcceptsNativePlanWithoutACL(t *testing.T) {
	policy := trafficpolicy.RoutePolicy{
		ID:       "native-without-acl",
		Priority: 100,
		Action:   "route",
		Path:     &trafficpolicy.WANPath{VPPInterface: "lypxin86704d", NextHop: "198.18.52.146"},
		Match:    trafficpolicy.Match{Sources: []string{"any"}, Destinations: []string{"any"}},
	}
	operation := Operation{
		Name:     "vpp.route-policy",
		Resource: policy.ID,
		Payload:  policy,
		VPPCtlCommands: []string{
			"?ip table add 61940",
			"?set ip flow-hash table 61940 src dst sport dport proto",
			"?ip route add table 61940 0.0.0.0/0 via 198.18.52.146 lypxin86704d",
			"set ly-route pre-nat-route interface lyroute-ens34 lan-prefix 192.168.50.0/24",
			"set ly-route pre-nat-route add id 13333 priority 100 source 0.0.0.0/0 destination 0.0.0.0/0 protocol any sport 0-65535 dport 0-65535 table 61940 skip-nat",
		},
	}
	spec, err := parseRoutePolicyVPPCTLSpec(operation)
	if err != nil {
		t.Fatalf("parseRoutePolicyVPPCTLSpec() error = %v", err)
	}
	if !spec.apply || spec.acl != "" || spec.ingress != "lyroute-ens34" {
		t.Fatalf("native route plan spec = %#v, want active ACL-free pre-NAT plan", spec)
	}
}

func TestPreNATRoutePolicyRefreshRecreatesPrivateFIBBeforeClassifier(t *testing.T) {
	operation := Operation{VPPCtlCommands: []string{
		"?ip table add 52110",
		"?set ip flow-hash table 52110 src dst sport dport proto",
		"?ip route add table 52110 0.0.0.0/0 via 10.67.0.1 pppoe_session11",
	}}
	spec := routePolicyVPPCTLSpec{policyID: 10886, tableID: 52110}
	classifier := []string{"set ly-route pre-nat-route add id 10886 priority 50 source 192.168.50.0/24 destination 0.0.0.0/0 protocol any sport 0-65535 dport 0-65535 table 52110"}
	commands := preNATRoutePolicyRefreshCommands(operation, spec, classifier)
	joined := strings.Join(commands, "\n")

	wantOrder := []string{
		"set ly-route pre-nat-route del id 10886",
		"ip route del table 52110 0.0.0.0/0",
		"ip table del 52110",
		"ip table add 52110",
		"ip route add table 52110 0.0.0.0/0",
		"set ly-route pre-nat-route add id 10886",
	}
	position := -1
	for _, want := range wantOrder {
		next := strings.Index(joined[position+1:], want)
		if next < 0 {
			t.Fatalf("refresh command %q is missing or out of order: %s", want, joined)
		}
		position += next + 1
	}
}

func TestNativePreNATRoutePolicyLifecycleLeavesDenyOnSecurityPath(t *testing.T) {
	policy := trafficpolicy.RoutePolicy{
		ID:     "deny-route",
		Action: "deny",
		Match:  trafficpolicy.Match{Sources: []string{"any"}, Destinations: []string{"any"}},
	}
	if routePolicySupportsNativePreNAT(policy) {
		t.Fatal("deny policy must not be converted into a forwarding classifier")
	}
}
