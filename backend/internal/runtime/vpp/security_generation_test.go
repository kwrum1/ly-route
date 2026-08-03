package vpp

import (
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestCompileSecurityGenerationPreservesPriorityAndTerminalSemantics(t *testing.T) {
	acl := trafficpolicy.SecurityACL{ID: "sec-admin", Priority: 5, Action: "permit", Match: trafficpolicy.Match{Sources: []string{"192.0.2.10/32"}, Destinations: []string{"0.0.0.0/0"}, Protocols: []string{"tcp"}, DestPorts: []string{"443"}, Direction: "input"}}
	generated, err := CompileSecurityGeneration("generation-a", "lan0", []trafficpolicy.SecurityACL{acl}, nil, []SecurityThreatList{
		{ID: "sec-blacklist", Interface: "wan0", Priority: 20, ListType: "blacklist", Direction: "input", Entries: []string{"198.51.100.9/32"}},
		{ID: "sec-whitelist", Interface: "wan1", Priority: 30, ListType: "whitelist", Direction: "output", Entries: []string{"203.0.113.0/24"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(generated.ACLs) != 3 {
		t.Fatalf("ACL groups = %#v", generated.ACLs)
	}
	byInterface := map[string]SecurityInterfaceACL{}
	for _, group := range generated.ACLs {
		byInterface[group.Interface] = group
	}
	blacklist := byInterface["wan0"].Rules
	if len(blacklist) != 3 || blacklist[0].Action != "deny" || blacklist[1].Action != "permit" || blacklist[2].Action != "permit" || !strings.Contains(blacklist[1].ID, "terminal-permit") {
		t.Fatalf("blacklist terminal behavior = %#v", blacklist)
	}
	whitelist := byInterface["wan1"].Rules
	if len(whitelist) != 3 || whitelist[0].Action != "permit" || whitelist[1].Action != "deny" || whitelist[2].Action != "deny" || !strings.Contains(whitelist[1].ID, "terminal-deny") {
		t.Fatalf("whitelist terminal behavior = %#v", whitelist)
	}
}

func TestSecurityGenerationExpandsEveryPortAndBothAddressFamilies(t *testing.T) {
	rules, err := securityInterfaceACLRules([]trafficpolicy.SecurityACL{{ID: "dual-stack", Action: "permit", Match: trafficpolicy.Match{
		Sources: []string{"192.0.2.1", "2001:db8::1"}, Protocols: []string{"tcp", "icmp"}, SourcePorts: []string{"1000", "2000-2001"}, DestPorts: []string{"80", "443"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 16 {
		t.Fatalf("rule count = %d, rules = %#v", len(rules), rules)
	}
	trace := strings.Join(rules, "\n")
	for _, expected := range []string{
		"src 192.0.2.1/32 dst 0.0.0.0/0 proto 6 sport 1000-1000 dport 80-80",
		"src 192.0.2.1/32 dst 0.0.0.0/0 proto 1 sport 2000-2001 dport 443-443",
		"src 2001:db8::1/128 dst ::/0 proto 6 sport 1000-1000 dport 80-80",
		"src 2001:db8::1/128 dst ::/0 proto 58 sport 2000-2001 dport 443-443",
	} {
		if !strings.Contains(trace, expected) {
			t.Fatalf("rules missing %q:\n%s", expected, trace)
		}
	}
}

func TestThreatListUsesDestinationForOutputAndDualStackTerminal(t *testing.T) {
	generation, err := CompileSecurityGeneration("generation-v6", "lan0", nil, nil, []SecurityThreatList{{
		ID: "outbound-v6", Interface: "wan0", Priority: 10, ListType: "blacklist", Direction: "output", Entries: []string{"2001:db8:bad::/48"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(generation.ACLs) != 1 || len(generation.ACLs[0].Rules) != 3 {
		t.Fatalf("generation = %#v", generation)
	}
	entry := generation.ACLs[0].Rules[0]
	if entry.Match.Sources[0] != "::/0" || entry.Match.Destinations[0] != "2001:db8:bad::/48" {
		t.Fatalf("output threat match = %#v", entry.Match)
	}
	commands, err := securityInterfaceACLRules(generation.ACLs[0].Rules)
	if err != nil {
		t.Fatal(err)
	}
	trace := strings.Join(commands, "\n")
	if !strings.Contains(trace, "deny src ::/0 dst 2001:db8:bad::/48") || !strings.Contains(trace, "permit src 0.0.0.0/0 dst 0.0.0.0/0") || !strings.Contains(trace, "permit src ::/0 dst ::/0") {
		t.Fatalf("commands = %s", trace)
	}
}

func TestSecurityACLRejectsCrossFamilySelectors(t *testing.T) {
	_, err := securityInterfaceACLRules([]trafficpolicy.SecurityACL{{ID: "mixed", Action: "deny", Match: trafficpolicy.Match{Sources: []string{"192.0.2.0/24"}, Destinations: []string{"2001:db8::/32"}}}})
	if err == nil || !strings.Contains(err.Error(), "different address families") {
		t.Fatalf("error = %v", err)
	}
}

func TestSecurityGenerationCommandsUseAggregateACLAndNativeMACIP(t *testing.T) {
	generation, err := CompileSecurityGeneration("generation-a", "lan0", nil, []SecurityMACIPACL{{Interface: "lan0", Mode: "enforce", UnboundBehavior: "block", Bindings: []SecurityMACIPRule{{IP: "192.0.2.10", MAC: "02:00:00:00:00:10"}}}}, []SecurityThreatList{{ID: "sec-feed", Interface: "wan0", Priority: 10, ListType: "blacklist", Direction: "input", Entries: []string{"198.51.100.9/32"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	commands, err := securityGenerationCommands(generation)
	if err != nil {
		t.Fatal(err)
	}
	trace := strings.Join(commands, "\n")
	for _, required := range []string{
		"set acl-plugin acl deny src 198.51.100.9/32",
		"permit src 0.0.0.0/0",
		"set acl-plugin macip acl permit ip 192.0.2.10/32 mac 02:00:00:00:00:10 mask ff:ff:ff:ff:ff:ff",
		"deny ip 0.0.0.0/0 mac 00:00:00:00:00:00 mask 00:00:00:00:00:00",
	} {
		if !strings.Contains(trace, required) {
			t.Fatalf("commands missing %q:\n%s", required, trace)
		}
	}
}

func TestSecurityGenerationCommandsUseProtocolAwareGuard(t *testing.T) {
	generation := SecurityGeneration{ID: "generation-a", AttackRules: []SecurityAttackRule{{ID: "sec-syn", Interface: "wan0", AttackType: "syn_flood", ThresholdPPS: 100, BurstPackets: 200, EnforcementMode: "enforce"}}}
	commands, err := securityGenerationCommands(generation)
	if err != nil {
		t.Fatal(err)
	}
	trace := strings.Join(commands, "\n")
	for _, expected := range []string{"set ly-route security-guard rule ly-route-security-attack-sec_syn-ip4", "set ly-route security-guard rule ly-route-security-attack-sec_syn-ip6", "attack-type syn_flood"} {
		if !strings.Contains(trace, expected) {
			t.Fatalf("commands missing %q:\n%s", expected, trace)
		}
	}
	if strings.Contains(trace, "policer add") || strings.Contains(trace, "classify") {
		t.Fatalf("attack rule must not degrade to interface-wide policer: %s", trace)
	}
}

func TestSecurityAttackRulesRejectCrossFamilySelectors(t *testing.T) {
	_, err := securityAttackCommands([]SecurityAttackRule{{ID: "bad", Interface: "wan0", AttackType: "udp_flood", ThresholdPPS: 1, BurstPackets: 1, EnforcementMode: "alert", SourcePrefix: "192.0.2.0/24", DestinationPrefix: "2001:db8::/32"}})
	if err == nil || !strings.Contains(err.Error(), "different address families") {
		t.Fatalf("error = %v", err)
	}
}

func TestSecurityGenerationInventoryParsesOwnedACLOnly(t *testing.T) {
	output := "acl-index 7 count 1 tag {other}\n  0: ipv4 permit src 0.0.0.0/0 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535\n" +
		"acl-index 9 count 1 tag {ly-route-security-gen-wan0-input}\n  0: ipv4 deny src 198.51.100.9/32 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535\n  applied inbound on sw_if_index: 4\n"
	identities := securityGenerationACLIDs(output)
	if len(identities) != 1 || identities[0].ID != 9 || identities[0].InterfaceIndex != 4 {
		t.Fatalf("identities = %#v", identities)
	}
}

func TestSecurityGenerationInterfaceInventoryIsStrictAndDeterministic(t *testing.T) {
	output := "Name Idx State MTU (L3/IP4/IP6/MPLS) Counter Count\n" +
		"local0 0 down 0/0/0/0\n" +
		"lyroute-wan0 7 up 9000/0/0/0\n" +
		"malformed not-an-index up\n" +
		"lyroute-lan0 3 up 9000/0/0/0\n" +
		"lyroute-wan0 7 up 9000/0/0/0\n"
	interfaces := securityGenerationInterfaceNames(output)
	if strings.Join(interfaces, ",") != "local0,lyroute-lan0,lyroute-wan0" {
		t.Fatalf("interfaces = %#v", interfaces)
	}
}

func TestSecurityGenerationPreflightRejectsMissingInterfaceBeforeMutation(t *testing.T) {
	generation := SecurityGeneration{AttackRules: []SecurityAttackRule{{ID: "syn", Interface: "missing0", AttackType: "syn_flood", ThresholdPPS: 1, BurstPackets: 1, EnforcementMode: "enforce"}}}
	err := validateSecurityGenerationInterfaces(generation, "Name Idx State MTU (L3/IP4/IP6/MPLS) Counter Count\nlyroute-lan0 3 up 9000/0/0/0\n")
	if err == nil || !strings.Contains(err.Error(), "missing0") || !strings.Contains(err.Error(), "not present") {
		t.Fatalf("error = %v", err)
	}
}

func TestTaggedSecurityMACIPOutputAndAttachment(t *testing.T) {
	output := "MACIP acl_index: 0, count: 2 tag {other}\n  rule 0: ipv4 action 1 ip 192.0.2.1/32 mac 02:00:00:00:00:01 mask ff:ff:ff:ff:ff:ff\n" +
		"MACIP acl_index: 4, count: 2 tag {ly-route-security-macip-lan0}\n  rule 0: ipv4 action 1 ip 192.0.2.2/32 mac 02:00:00:00:00:02 mask ff:ff:ff:ff:ff:ff\n  applied on sw_if_index(s): 7\n"
	block, id, found, err := taggedSecurityMACIPOutput(output, "ly-route-security-macip-lan0")
	if err != nil || !found || id != 4 || !strings.Contains(block, "192.0.2.2/32") || strings.Contains(block, "192.0.2.1/32") {
		t.Fatalf("block=%q id=%d found=%v err=%v", block, id, found, err)
	}
	if !securityMACIPInterfaceAttached("sw_if_index 7: 4\nsw_if_index 8: -1\n", 7, 4) {
		t.Fatal("MACIP attachment was not recognized")
	}
}
