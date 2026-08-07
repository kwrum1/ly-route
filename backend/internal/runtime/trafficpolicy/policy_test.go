package trafficpolicy

import (
	"strings"
	"testing"
	"time"
)

func TestCompileConfigBuildsRoutePolicyAndSecurityACL(t *testing.T) {
	compiled, err := CompileConfig(
		[]map[string]any{{
			"id":        "video-egress",
			"priority":  float64(10),
			"action":    "指定线路",
			"match":     map[string]any{"src_ip": "grp-office", "dst_port": "tcp/443, udp/3478"},
			"wan_group": "wan-primary",
		}},
		[]map[string]any{{
			"id":     "guest-block",
			"action": "deny",
			"match":  map[string]any{"src_ip": "192.168.20.0/24", "dst_ip": "obj-private", "protocol": "tcp"},
		}},
		[]map[string]any{
			{"id": "grp-office", "kind": "ip", "entries": []any{"192.168.10.0/24"}},
			{"id": "obj-private", "kind": "ip", "entries": []any{"10.0.0.0/8", "192.168.0.0/16"}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.RoutePolicies) != 1 {
		t.Fatalf("route policies = %#v", compiled.RoutePolicies)
	}
	route := compiled.RoutePolicies[0]
	if route.Action != "route" || route.Egress != "wan-primary" || route.Match.Sources[0] != "192.168.10.0/24" {
		t.Fatalf("route policy = %#v", route)
	}
	if len(route.Match.Protocols) != 2 || route.Match.DestPorts[0] != "443" || route.Match.DestPorts[1] != "3478" {
		t.Fatalf("route policy match = %#v", route.Match)
	}
	if len(compiled.SecurityACLs) != 1 || len(compiled.SecurityACLs[0].Match.Destinations) != 2 {
		t.Fatalf("security acls = %#v", compiled.SecurityACLs)
	}
}

func TestCompileConfigExpandsStringSliceObjectGroup(t *testing.T) {
	compiled, err := CompileConfig([]map[string]any{{
		"id": "geoip-cn", "action": "route", "egress": "wan0",
		"match": map[string]any{"dst_ip": "obj-geoip-cn"},
	}}, nil, []map[string]any{{
		"id": "obj-geoip-cn", "kind": "ip", "entries": []string{"1.1.1.0/24", "2.2.2.2"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := compiled.RoutePolicies[0].Match.Destinations
	if strings.Join(got, ",") != "1.1.1.0/24,2.2.2.2/32" {
		t.Fatalf("destinations = %v", got)
	}
}

func TestCompileConfigInfersTCPAndUDPForBarePortConditions(t *testing.T) {
	compiled, err := CompileConfig([]map[string]any{{
		"id": "port-route", "action": "route", "egress": "wan0",
		"match": map[string]any{"src_ip": "192.0.2.10", "src_port": "1024-65535", "dst_port": "443"},
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := compiled.RoutePolicies[0].Match.Protocols
	if strings.Join(got, ",") != "tcp,udp" {
		t.Fatalf("protocols = %v, want tcp and udp", got)
	}
	if compiled.RoutePolicies[0].Match.Sources[0] != "192.0.2.10/32" {
		t.Fatalf("source = %v, want canonical host prefix", compiled.RoutePolicies[0].Match.Sources)
	}
}

func TestCompileConfigExpandsAddressRangesToMinimalPrefixes(t *testing.T) {
	compiled, err := CompileConfig(
		[]map[string]any{{
			"id": "range-route", "action": "route", "egress": "wan0",
			"match": map[string]any{"src_ip": "192.168.1.10-192.168.1.20"},
		}}, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.168.1.10/31", "192.168.1.12/30", "192.168.1.16/30", "192.168.1.20/32"}
	got := compiled.RoutePolicies[0].Match.Sources
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("range prefixes = %v, want %v", got, want)
	}
}

func TestCompileConfigExpandsIPv6AddressRange(t *testing.T) {
	compiled, err := CompileConfig(
		[]map[string]any{{
			"id": "range-v6", "action": "route", "egress": "wan0",
			"match": map[string]any{"src_ip": "2001:db8::10-2001:db8::13"},
		}}, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2001:db8::10/126"}
	got := compiled.RoutePolicies[0].Match.Sources
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("IPv6 range prefixes = %v, want %v", got, want)
	}
}

func TestCompileConfigRejectsInvalidAddressRange(t *testing.T) {
	for _, value := range []string{"192.168.1.20-192.168.1.10", "192.168.1.1-2001:db8::1"} {
		_, err := CompileConfig([]map[string]any{{"id": "bad-range", "action": "route", "egress": "wan0", "match": map[string]any{"src_ip": value}}}, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "invalid IP range") {
			t.Fatalf("CompileConfig(%q) error = %v, want invalid IP range", value, err)
		}
	}
}

func TestIPSelectorMatchesAddressRangeBoundaries(t *testing.T) {
	selector := "192.168.1.10-192.168.1.20"
	for _, address := range []string{"192.168.1.10", "192.168.1.15", "192.168.1.20"} {
		if !ipSelectorMatches(selector, address) {
			t.Fatalf("%s should match %s", selector, address)
		}
	}
	for _, address := range []string{"192.168.1.9", "192.168.1.21"} {
		if ipSelectorMatches(selector, address) {
			t.Fatalf("%s should not match %s", selector, address)
		}
	}
}

func TestCompileConfigAcceptsPluralMatchSelectors(t *testing.T) {
	compiled, err := CompileConfig(
		[]map[string]any{{
			"id":       "ui-route",
			"priority": float64(20),
			"action":   "route",
			"match": map[string]any{
				"sources":      []any{"grp-office"},
				"destinations": []any{"8.8.8.8/32"},
				"protocols":    []any{"tcp"},
				"dest_ports":   []any{"443"},
			},
			"egress": "wan-primary",
		}},
		nil,
		[]map[string]any{{"id": "grp-office", "kind": "ip", "entries": []any{"192.168.10.0/24"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.RoutePolicies) != 1 {
		t.Fatalf("route policies = %#v", compiled.RoutePolicies)
	}
	match := compiled.RoutePolicies[0].Match
	if match.Sources[0] != "192.168.10.0/24" || match.Destinations[0] != "8.8.8.8/32" || match.Protocols[0] != "tcp" || match.DestPorts[0] != "443" {
		t.Fatalf("match = %#v", match)
	}
}

func TestCompileConfigWithDomainIPSetExpandsDomainRoutePolicy(t *testing.T) {
	compiled, err := CompileConfigWithDomainIPSet(
		[]map[string]any{{"id": "domain-route", "action": "route", "egress": "wan0", "match": map[string]any{"domain_group": "obj-video-domains"}}},
		nil,
		[]map[string]any{{"id": "obj-video-domains", "kind": "domain", "entries": []any{"video.example", "cdn.example"}}},
		[]DomainIPSetEntry{{Domain: "video.example", IPs: []string{"203.0.113.10", "203.0.113.11"}}, {Domain: "cdn.example", IPs: []string{"198.51.100.8"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.RoutePolicies) != 1 {
		t.Fatalf("route policies = %#v", compiled.RoutePolicies)
	}
	got := compiled.RoutePolicies[0].Match.Destinations
	for _, want := range []string{"203.0.113.10", "203.0.113.11", "198.51.100.8"} {
		found := false
		for _, value := range got {
			if value == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("destinations = %#v, missing %s", got, want)
		}
	}
}

func TestCompileConfigWithDomainIPSetHonorsTTLExpiry(t *testing.T) {
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	compiled, err := CompileConfigWithDomainIPSetAt(
		[]map[string]any{{"id": "domain-route", "action": "route", "egress": "wan0", "match": map[string]any{"domain_group": "obj-video-domains"}}},
		nil,
		[]map[string]any{{"id": "obj-video-domains", "kind": "domain", "entries": []any{"video.example", "expired.example"}}},
		[]DomainIPSetEntry{{Domain: "video.example", IPs: []string{"203.0.113.10"}, ExpiresAt: now.Add(time.Minute).Format(time.RFC3339)}, {Domain: "expired.example", IPs: []string{"198.51.100.8"}, ExpiresAt: now.Add(-time.Second).Format(time.RFC3339)}},
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	destinations := compiled.RoutePolicies[0].Match.Destinations
	if len(destinations) != 1 || destinations[0] != "203.0.113.10" {
		t.Fatalf("destinations = %#v, want only unexpired DNS IP", destinations)
	}
}

func TestCompileConfigAnyFiveTupleCompilesDeterministicallyAndNoMatchIsEmpty(t *testing.T) {
	compiled, err := CompileConfig(
		[]map[string]any{{"id": "any-route", "priority": 10, "action": "route", "egress": "wan0", "match": map[string]any{"src_ip": "any", "dst_ip": "any", "protocol": "any", "src_port": "any", "dst_port": "any"}}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	route := compiled.RoutePolicies[0]
	if route.Match.Sources[0] != "0.0.0.0/0" || route.Match.Destinations[0] != "0.0.0.0/0" || route.Match.Protocols[0] != "any" || route.Match.SourcePorts[0] != "any" || route.Match.DestPorts[0] != "any" {
		t.Fatalf("any route match = %#v", route.Match)
	}
	decision := DecideRoute(compiled.RoutePolicies, Flow{SourceIP: "192.0.2.10", DestIP: "198.51.100.20", Protocol: "udp", SourcePort: "53000", DestPort: "443"}, nil, time.Now())
	if !decision.Matched || decision.RuleID != "any-route" || decision.Egress != "wan0" {
		t.Fatalf("any route decision = %#v", decision)
	}
	noMatch := DecideRoute(nil, Flow{SourceIP: "192.0.2.10", DestIP: "198.51.100.20"}, nil, time.Now())
	if noMatch.Matched || noMatch.Action != "" || noMatch.Egress != "" || noMatch.NextHop != "" {
		t.Fatalf("no-match decision = %#v, want empty", noMatch)
	}
}

func TestDNSOverrideWinsUntilTTLExpiry(t *testing.T) {
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	policies := []RoutePolicy{{ID: "route-b", Priority: 10, Action: "route", Egress: "wan-b", Match: Match{Sources: []string{"192.168.88.0/24"}, Destinations: []string{"203.0.113.10"}, Protocols: []string{"any"}}}}
	flow := Flow{SourceIP: "192.168.88.10", DestIP: "203.0.113.10", Protocol: "tcp"}
	overrides := []DNSOverrideIntent{{Source: "192.168.88.0/24", ResolvedIP: "203.0.113.10", Egress: "wan-a", ExpiresAt: now.Add(time.Minute).Format(time.RFC3339)}}
	active := DecideRoute(policies, flow, overrides, now)
	if !active.Matched || active.Egress != "wan-a" || active.Reason != "dns_intent_override" {
		t.Fatalf("active override decision = %#v", active)
	}
	expired := DecideRoute(policies, flow, overrides, now.Add(2*time.Minute))
	if !expired.Matched || expired.RuleID != "route-b" || expired.Egress != "wan-b" || expired.Reason != "" {
		t.Fatalf("expired override decision = %#v", expired)
	}
}

func TestCompileConfigRejectsUnsupportedPolicyCondition(t *testing.T) {
	_, err := CompileConfig([]map[string]any{{"id": "bad", "action": "route", "egress": "wan0", "match": map[string]any{"app_id": "video"}}}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported policy condition") {
		t.Fatalf("error = %v, want unsupported condition", err)
	}
}

func TestCompileConfigRejectsUnsupportedObjectGroupKind(t *testing.T) {
	_, err := CompileConfig(
		[]map[string]any{{"id": "domain-route", "action": "route", "next_hop": "wan0", "match": map[string]any{"dst_ip": "obj-domain"}}},
		nil,
		[]map[string]any{{"id": "obj-domain", "kind": "domain", "entries": []any{"example.com"}}},
	)
	if err == nil {
		t.Fatal("expected non-IP object group to fail VPP ACL compilation")
	}
}

func TestCompileConfigSupportsSchemaObjectGroupsAndOutputDirection(t *testing.T) {
	compiled, err := CompileConfig(
		nil,
		[]map[string]any{{"id": "office-web", "action": "permit", "match": map[string]any{"src_ip": "grp-office-all", "dst_port": "svc-web", "direction": "lan_to_wan"}}},
		[]map[string]any{
			{"id": "grp-office-all", "group_type": "ip", "entries": []any{map[string]any{"value": "192.168.10.0/24"}}, "references": []any{"grp-office-extra"}},
			{"id": "grp-office-extra", "group_type": "ip", "members": []any{"192.168.11.0/24"}},
			{"id": "svc-web", "group_type": "service", "entries": []any{map[string]any{"value": "tcp/443"}, map[string]any{"value": "tcp/8443"}}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	acl := compiled.SecurityACLs[0]
	if acl.Match.Direction != "output" || len(acl.Match.Sources) != 2 || len(acl.Match.DestPorts) != 2 || acl.Match.DestPorts[1] != "8443" {
		t.Fatalf("security acl = %#v", acl)
	}
}

func TestCompileConfigPreservesBidirectionalSecurityACL(t *testing.T) {
	compiled, err := CompileConfig(nil, []map[string]any{{
		"id": "lan-bidirectional", "action": "deny",
		"match": map[string]any{
			"src_ip": "192.168.50.0/24", "dst_ip": "0.0.0.0/0",
			"protocol": "any", "direction": "both",
		},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.SecurityACLs) != 1 || compiled.SecurityACLs[0].Match.Direction != "both" {
		t.Fatalf("compiled bidirectional ACL = %#v", compiled.SecurityACLs)
	}
}

func TestCompileConfigSupportsSchemaObjectGroupsReferencesAndDirection(t *testing.T) {
	compiled, err := CompileConfig(
		nil,
		[]map[string]any{{"id": "egress-web", "action": "permit", "match": map[string]any{"src_ip": "grp-lan", "dst_port": "svc-web", "direction": "lan_to_wan"}}},
		[]map[string]any{
			{"id": "grp-lan", "group_type": "ip", "entries": []any{map[string]any{"value": "192.168.88.0/24"}}, "references": []any{"grp-guest"}},
			{"id": "grp-guest", "group_type": "ip", "entries": []any{map[string]any{"value": "192.168.20.0/24"}}, "references": []any{}},
			{"id": "svc-web", "group_type": "service", "entries": []any{map[string]any{"value": "tcp/443"}}, "references": []any{}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	acl := compiled.SecurityACLs[0]
	if acl.Match.Direction != "output" || len(acl.Match.Sources) != 2 || acl.Match.Protocols[0] != "tcp" || acl.Match.DestPorts[0] != "443" {
		t.Fatalf("compiled acl = %#v", acl)
	}
}

func TestCompileWANGroupsBuildsMembers(t *testing.T) {
	groups, err := CompileWANGroups([]map[string]any{{"id": "wan-primary", "wan_members": []any{"wan0", "wan1"}, "member_weights": []any{map[string]any{"id": "wan0", "weight": float64(3)}, map[string]any{"id": "wan1", "weight": float64(1)}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Mode != WANGroupWeighted || groups[0].Members[0] != "wan0" || groups[0].Members[1] != "wan1" || groups[0].Weights["wan0"] != 3 || groups[0].Weights["wan1"] != 1 {
		t.Fatalf("wan groups = %#v", groups)
	}
}

func TestCompileWANGroupsSupportsPrimaryBackupAndFiveTuple(t *testing.T) {
	groups, err := CompileWANGroups([]map[string]any{
		{"id": "failover", "members": []any{"wan0", "wan1"}, "load_balance": map[string]any{"mode": "primary_backup"}},
		{"id": "ecmp", "members": []any{"wan0", "wan1"}, "load_balance": map[string]any{"mode": "five_tuple"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if groups[0].Mode != WANGroupPrimaryBackup || groups[1].Mode != WANGroupFiveTuple {
		t.Fatalf("WAN group modes = %#v", groups)
	}
}

func TestCompileWANGroupsRejectsFiveTupleWeightsAndUnknownMembers(t *testing.T) {
	for _, item := range []map[string]any{
		{"id": "ecmp", "members": []any{"wan0", "wan1"}, "mode": "five_tuple", "weights": map[string]any{"wan0": float64(2)}},
		{"id": "weighted", "members": []any{"wan0", "wan1"}, "weights": map[string]any{"wan2": float64(2)}},
	} {
		if _, err := CompileWANGroups([]map[string]any{item}); err == nil {
			t.Fatalf("invalid WAN group accepted: %#v", item)
		}
	}
}

func TestCompileWANGroupsAcceptsMemberObjectsAndWeightMaps(t *testing.T) {
	groups, err := CompileWANGroups([]map[string]any{{
		"id":             "wan-primary",
		"wan_members":    []any{map[string]any{"id": "wan0", "enabled": true, "weight": float64(3)}, map[string]any{"id": "wan1", "enabled": true, "weight": float64(1)}},
		"member_weights": map[string]any{"wan0": float64(3), "wan1": float64(1)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Members[0] != "wan0" || groups[0].Members[1] != "wan1" || groups[0].Weights["wan0"] != 3 || groups[0].Weights["wan1"] != 1 {
		t.Fatalf("wan groups = %#v", groups)
	}
}

func TestCompileWANGroupsRejectsCommandCharacters(t *testing.T) {
	_, err := CompileWANGroups([]map[string]any{{"id": "wan-primary", "wan_members": []any{"wan0;show", "wan1"}}})
	if err == nil {
		t.Fatal("expected command characters in WAN group member to fail")
	}
}

func TestBindRoutePolicyPathsResolvesDirectWANAndPreservesGroups(t *testing.T) {
	policies := []RoutePolicy{
		{ID: "direct", Action: "route", Egress: "wan0", NextHop: "198.51.100.9"},
		{ID: "grouped", Action: "route", Egress: "wan-primary"},
	}
	err := BindRoutePolicyPaths(policies, map[string]WANPath{
		"wan0": {VPPInterface: "lyroute-eth1", NextHop: "198.51.100.1"},
	}, []WANGroup{{ID: "wan-primary"}})
	if err != nil {
		t.Fatal(err)
	}
	if policies[0].Path == nil || policies[0].Path.VPPInterface != "lyroute-eth1" || policies[0].Path.NextHop != "198.51.100.9" {
		t.Fatalf("direct route path = %#v", policies[0].Path)
	}
	if policies[1].Path != nil {
		t.Fatalf("group route unexpectedly has direct path %#v", policies[1].Path)
	}
}

func TestBindRoutePolicyPathsRejectsUnknownDirectWAN(t *testing.T) {
	policies := []RoutePolicy{{ID: "direct", Action: "route", Egress: "missing"}}
	if err := BindRoutePolicyPaths(policies, nil, nil); err == nil {
		t.Fatal("unknown direct WAN binding was accepted")
	}
}

func TestCompileConfigRejectsRouteNextHopCommandCharacters(t *testing.T) {
	_, err := CompileConfig([]map[string]any{{"id": "bad-route", "action": "route", "next_hop": "wan0;show", "match": map[string]any{"src_ip": "any"}}}, nil, nil)
	if err == nil {
		t.Fatal("expected command characters in route next_hop to fail")
	}
}

func TestCompileConfigRejectsObjectGroupCommandCharacters(t *testing.T) {
	_, err := CompileConfig(
		nil,
		[]map[string]any{{"id": "bad-acl", "action": "deny", "match": map[string]any{"src_ip": "bad-group"}}},
		[]map[string]any{{"id": "bad-group", "kind": "ip", "entries": []any{"192.168.1.0/24;show version"}}},
	)
	if err == nil {
		t.Fatal("expected command characters in object group entry to fail")
	}
}
