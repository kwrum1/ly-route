package vpp

import (
	"net/netip"
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestDecodePreNATRouteHeaderAcceptsVPPInterfaceIndex(t *testing.T) {
	header, err := decodePreNATRouteHeader("enabled 1 interface lyroute-ens34 (2) lan-prefix 192.168.50.0/24 rules 4 radix-nodes 17")
	if err != nil {
		t.Fatalf("decodePreNATRouteHeader() error = %v", err)
	}
	if header.enabled != 1 || header.interfaceName != "lyroute-ens34" || header.lanPrefix != "192.168.50.0/24" || header.ruleCount != 4 || header.radixNodes != 17 {
		t.Fatalf("unexpected header: %#v", header)
	}
}

func TestPreNATRoutePolicySummaryForIDAcceptsVPPInterfaceIndex(t *testing.T) {
	output := "enabled 1 interface lyroute-ens34 (2) lan-prefix 192.168.50.0/24 rules 1 radix-nodes 4\n" +
		"rule id 7 priority 10 prefixes 1 table 100 fib-index 3 skip-nat 0 bypass 0\n"
	summary, found, err := preNATRoutePolicySummaryForID(output, 7)
	if err != nil {
		t.Fatalf("preNATRoutePolicySummaryForID() error = %v", err)
	}
	if !found || summary.priority != 10 || summary.prefixes != 1 || summary.tableID != 100 || summary.fibIndex != 3 || summary.skipNAT || summary.bypass {
		t.Fatalf("unexpected summary: found=%t summary=%#v", found, summary)
	}
}

func TestVerifyRoutePolicyRadixReadbackTreatsDisabledClassifierAsMissing(t *testing.T) {
	present, err := verifyRoutePolicyRadixReadback(
		"enabled 0 interface DELETED (4294967295) lan-prefix 0.0.0.0/0 rules 0 radix-nodes 1\n",
		SnapshotRequest{LANVPPInterface: "lyroute-ens34"},
		trafficpolicy.RoutePolicy{ID: "geoip-cn"},
		7,
		100,
		routePolicyRadixPlan{lanPrefix: netip.MustParsePrefix("192.168.50.0/24")},
	)
	if err != nil {
		t.Fatalf("verifyRoutePolicyRadixReadback() error = %v", err)
	}
	if present {
		t.Fatal("disabled classifier was reported present")
	}
}

func TestVerifyRoutePolicyRadixReadbackAllowsStaleSummaryDuringRecovery(t *testing.T) {
	policy := trafficpolicy.RoutePolicy{ID: "geoip-cn", Priority: 10}
	plan := routePolicyRadixPlan{lanPrefix: netip.MustParsePrefix("192.168.50.0/24"), ruleCount: 1}
	output := "enabled 1 interface lyroute-ens34 lan-prefix 192.168.50.0/24 rules 1 radix-nodes 4\n" +
		"rule id 7 priority 10 prefixes 1 table 101 fib-index 3 skip-nat 0 bypass 0\n"

	present, err := verifyRoutePolicyRadixReadback(output, SnapshotRequest{AllowMissing: true, LANVPPInterface: "lyroute-ens34"}, policy, 7, 100, plan)
	if err != nil {
		t.Fatalf("recovery readback error = %v", err)
	}
	if present {
		t.Fatal("stale pre-NAT classifier was reported present")
	}

	_, err = verifyRoutePolicyRadixReadback(output, SnapshotRequest{LANVPPInterface: "lyroute-ens34"}, policy, 7, 100, plan)
	if err == nil || !strings.Contains(err.Error(), "does not match candidate") {
		t.Fatalf("strict readback error = %v, want candidate mismatch", err)
	}
}
