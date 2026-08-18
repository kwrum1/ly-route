package vpp

import (
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestFlowRatePolicerConvertsBitBurstToVPPBytes(t *testing.T) {
	target := flow.Target{
		Kind:        "vpp.behavior.rate",
		RuleID:      "flow-330",
		Policer:     &flow.Policer{RateBPS: 20_000_000, BurstBPS: 2_000_000},
		Match:       flow.Match{Sources: []string{"192.168.50.0/24"}},
		Attachments: []string{"input:lyroute-lan0"},
	}
	joined := strings.Join(flowTargetCommands(target), "\n")
	if !strings.Contains(joined, "rate-kbps 20000 burst-bytes 250000") {
		t.Fatalf("policer command = %q, want 20 Mbps with a 250000-byte burst", joined)
	}
}

func TestFlowRatePolicerKeepsAtLeastOneHundredMillisecondsOfTokens(t *testing.T) {
	target := flow.Target{
		Kind:        "vpp.behavior.rate",
		RuleID:      "flow-small-burst",
		Policer:     &flow.Policer{RateBPS: 8_000_000, BurstBPS: 8_000},
		Attachments: []string{"input:lyroute-lan0"},
	}
	joined := strings.Join(flowTargetCommands(target), "\n")
	if !strings.Contains(joined, "rate-kbps 8000 burst-bytes 100000") {
		t.Fatalf("policer command = %q, want a 100 ms minimum token bucket", joined)
	}
}

func TestFlowRateRulesNormalizeNumericProtocolsForPluginCLI(t *testing.T) {
	target := flow.Target{
		Kind:    "vpp.behavior.rate",
		RuleID:  "flow-protocols",
		Policer: &flow.Policer{RateBPS: 20_000_000, BurstBPS: 2_000_000},
		Match: flow.Match{
			Protocols: []string{"6", "17", "1", "0"},
		},
		Attachments: []string{"input:lyroute-lan0"},
	}
	joined := strings.Join(flowRateRuleCommands(target), "\n")
	for _, protocol := range []string{"protocol tcp", "protocol udp", "protocol icmp", "protocol any"} {
		if !strings.Contains(joined, protocol) {
			t.Fatalf("flow-rate command = %q, missing %q", joined, protocol)
		}
	}
}

func TestVerifyFlowRateResultUsesFinalReplacementInventory(t *testing.T) {
	target := flow.Target{
		Kind:        "vpp.behavior.rate",
		RuleID:      "flow-440",
		Policer:     &flow.Policer{RateBPS: 20_000_000, BurstBPS: 2_000_000},
		Match:       flow.Match{Sources: []string{"192.168.50.0/24"}, Destinations: []string{"0.0.0.0/0"}, Protocols: []string{"tcp"}, DestPorts: []string{"443"}},
		Attachments: []string{"input:lyroute-ens34"},
	}
	results := []VPPCTLCommandResult{
		{Command: "show ly-route flow-rate", Stdout: ""},
		{Command: "show ly-route flow-rate", Stdout: "rule flow_440_0 interface lyroute-ens34 direction input source 192.168.50.0/24 destination 0.0.0.0/0 protocol 6 source-port 0-65535 destination-port 443-443 rate-kbps 20000 burst-bytes 250000 matched-packets 0 matched-bytes 0 conform-packets 0 dropped-packets 0\n"},
	}
	if err := verifyFlowRateResult(results, target); err != nil {
		t.Fatalf("replacement inventory rejected: %v", err)
	}
}

func TestNormalizeDynamicACLTagPlacesTagBeforeRules(t *testing.T) {
	input := "set acl-plugin acl permit src 192.0.2.1/32 dst 0.0.0.0/0 proto 6 sport 0-65535 dport 443 tag ly-route-flow_200"
	want := "set acl-plugin acl tag ly-route-flow_200 permit src 192.0.2.1/32 dst 0.0.0.0/0 proto 6 sport 0-65535 dport 443"
	if got := normalizeDynamicACLTag(input); got != want {
		t.Fatalf("normalized command = %q, want %q", got, want)
	}
}

func TestACLMatchCommandsNormalizeAnyAddressBeforeProtocol(t *testing.T) {
	commands := aclMatchCommands(17, "flow-30", trafficpolicy.Match{
		Sources:      []string{"192.168.50.102/32"},
		Destinations: []string{"any"},
		Protocols:    []string{"tcp"},
	}, "permit")
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, "src 192.168.50.102/32 dst 0.0.0.0/0 proto 6") {
		t.Fatalf("ACL command did not preserve the TCP match after any-address normalization: %s", joined)
	}
}

func TestACLMatchCommandsWithFallbackPreservesUnmatchedTraffic(t *testing.T) {
	commands := aclMatchCommandsWithFallback(17, "flow-30", trafficpolicy.Match{
		Sources:      []string{"192.168.50.102/32"},
		Destinations: []string{"any"},
		Protocols:    []string{"tcp"},
	}, "permit")
	if len(commands) != 1 || !strings.Contains(commands[0], ", permit src 0.0.0.0/0 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535, permit src ::/0 dst ::/0 proto 0 sport 0-65535 dport 0-65535 tag") {
		t.Fatalf("commands = %#v, want trailing IPv4 and IPv6 permit-any rules", commands)
	}
}

func TestVerifyACLOutputAcceptsRateFallbackRule(t *testing.T) {
	output := "acl-index 17 count 3 tag {ly-route-flow_30}\n" +
		"  0: ipv4 permit src 192.168.50.102/32 dst 0.0.0.0/0 proto 6 sport 0-65535 dport 0-65535\n" +
		"  1: ipv4 permit src 0.0.0.0/0 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535\n" +
		"  2: ipv6 permit src ::/0 dst ::/0 proto 0 sport 0-65535 dport 0-65535\n"
	if err := verifyACLOutput(output, aclCandidateProof{
		numericID:      17,
		id:             "flow-30",
		action:         "permit",
		allowUnmatched: true,
		match:          trafficpolicy.Match{Sources: []string{"192.168.50.102/32"}, Destinations: []string{"any"}, Protocols: []string{"tcp"}},
	}); err != nil {
		t.Fatalf("verifyACLOutput() error = %v", err)
	}
}

func TestVerifyACLOutputRejectsRateRuleWithoutIPv6Fallback(t *testing.T) {
	output := "acl-index 17 count 2 tag {ly-route-flow_30}\n" +
		"  0: ipv4 permit src 192.168.50.102/32 dst 0.0.0.0/0 proto 6 sport 0-65535 dport 0-65535\n" +
		"  1: ipv4 permit src 0.0.0.0/0 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535\n"
	err := verifyACLOutput(output, aclCandidateProof{
		numericID:      17,
		id:             "flow-30",
		action:         "permit",
		allowUnmatched: true,
		match:          trafficpolicy.Match{Sources: []string{"192.168.50.102/32"}, Destinations: []string{"any"}, Protocols: []string{"tcp"}},
	})
	if err == nil {
		t.Fatal("verifyACLOutput() accepted a rate ACL without the IPv6 fallback")
	}
}

func TestVerifyACLOutputTreatsAnyAddressAsIPv4DefaultPrefix(t *testing.T) {
	output := "acl-index 17 count 1 tag {ly-route-flow_30}\n" +
		"  0: ipv4 permit src 192.168.50.102/32 dst 0.0.0.0/0 proto 6 sport 0-65535 dport 0-65535\n"
	err := verifyACLOutput(output, aclCandidateProof{
		numericID: 17,
		id:        "flow-30",
		action:    "permit",
		match: trafficpolicy.Match{
			Sources:      []string{"192.168.50.102/32"},
			Destinations: []string{"any"},
			Protocols:    []string{"tcp"},
		},
	})
	if err != nil {
		t.Fatalf("verifyACLOutput() error = %v", err)
	}
}
