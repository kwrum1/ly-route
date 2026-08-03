package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"ly-route/backend/internal/runtime/proxy"
	"ly-route/backend/internal/runtime/vpp"
)

func TestFilesystemController_VPP_readback_rejects_stale_applied_object(t *testing.T) {
	// Given
	artifacts, err := RenderVPPOperations([]vpp.Operation{{
		Name:           "vpp.interface.address",
		RequestID:      "txn-vpp",
		Resource:       "wan-blue",
		VPPCtlCommands: []string{"set interface state wan-blue up"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner := availableRunner(VPP, map[string]string{
		"vppctl show version":                          "vpp v24.10",
		"cat /var/lib/ly-route/vpp-apply-receipt.json": `{"status":"applied","operations":[{"name":"vpp.interface.address","resource":"wan-stale","results":[{"command":"set interface state wan-stale up","status":"applied"}]}]}`,
	})
	controller := FilesystemController{RootDir: t.TempDir(), Runner: runner}

	// When
	err = controller.ReloadOrRestart(context.Background(), VPP, artifacts)

	// Then
	assertReadbackMismatch(t, err, "wan-blue")
}

func TestFilesystemController_policy_readback_rejects_wrong_mark_priority_route_and_table(t *testing.T) {
	// Given
	plan := proxy.LinuxPolicyRoutingPlan{
		EgressID:     "edge-blue",
		Mark:         "0x7",
		MarkMask:     "0xffffffff",
		TableID:      1701,
		RulePriority: 1702,
		RuleSelector: proxy.LinuxRuleSelector{Family: "inet", Mark: "0x7", Mask: "0xffffffff", Table: 1701},
		DefaultRoute: proxy.LinuxDefaultRoute{Destination: "default", Table: 1701, Device: "lo", Scope: "link"},
	}
	artifacts, err := RenderLinuxPolicyRouting(plan)
	if err != nil {
		t.Fatal(err)
	}
	runner := availableRunner(LinuxRouting, map[string]string{
		"ip -j rule show":             `[{"priority":999,"fwmark":"0x1","fwmask":"0xffffffff","table":1001}]`,
		"ip -j route show table 1701": `[{"dst":"default","dev":"eth9","scope":"link"}]`,
	})
	controller := FilesystemController{RootDir: t.TempDir(), Runner: runner}

	// When
	err = controller.ReloadOrRestart(context.Background(), LinuxRouting, artifacts)

	// Then
	assertReadbackMismatch(t, err, "fwmark 0x7")
}

func TestFilesystemController_nftables_readback_rejects_missing_DNS_bypass_and_TProxy_rules(t *testing.T) {
	// Given
	plan := proxy.NftablesCapturePlan{
		EgressID: "edge-blue", Family: "inet", Table: "capture_blue", TargetPort: 15432, Mark: "0x7",
		Chains: []proxy.NftablesChain{{Name: "capture_pre", Type: "filter", Hook: "prerouting", Priority: -151, Policy: "accept"}},
		Rules: []proxy.NftablesRule{
			{Order: 1, EgressID: "edge-blue", Chain: "capture_pre", Expression: "tcp dport 53", Action: "return"},
			{Order: 2, EgressID: "edge-blue", Chain: "capture_pre", Expression: "udp dport 53", Action: "return"},
			{Order: 3, EgressID: "edge-blue", Chain: "capture_pre", Expression: "meta l4proto tcp", Action: "tproxy to :15432 mark set 0x7 accept"},
			{Order: 4, EgressID: "edge-blue", Chain: "capture_pre", Expression: "meta l4proto udp", Action: "tproxy to :15432 mark set 0x7 accept"},
		},
	}
	artifacts, err := RenderNftablesCapture(plan)
	if err != nil {
		t.Fatal(err)
	}
	runner := availableRunner(Nftables, map[string]string{
		"nft list table inet capture_blue": "table inet capture_blue {\n chain capture_pre { type filter hook prerouting priority filter -151; policy accept; }\n}",
	})
	controller := FilesystemController{RootDir: t.TempDir(), Runner: runner}

	// When
	err = controller.ReloadOrRestart(context.Background(), Nftables, artifacts)

	// Then
	assertReadbackMismatch(t, err, "tcp dport 53")
}

func TestFilesystemController_PPPoE_readback_rejects_disconnected_native_session(t *testing.T) {
	// Given
	artifacts, err := RenderPPPoE(PPPoEPeer{ID: "wan-blue", Interface: "eth7", Username: "subscriber", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	runner := availableRunner(PPPd, map[string]string{
		"cat /run/ly-route/pppoe/wan-blue.json": `{"state":"disconnected"}`,
	})
	controller := FilesystemController{RootDir: t.TempDir(), Runner: runner}

	// When
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = controller.ReloadOrRestart(ctx, PPPd, artifacts)

	// Then
	assertReadbackMismatch(t, err, "not connected")
}

func TestFilesystemController_IPv6RA_readback_rejects_wrong_delegated_64(t *testing.T) {
	// Given
	artifacts, err := RenderIPv6RA(IPv6RAPlan{Interface: "lan7", DelegatedPrefix: "2001:db8:7::/56"})
	if err != nil {
		t.Fatal(err)
	}
	runner := availableRunner(IPv6RA, map[string]string{
		"radvdump": "interface lan7 { prefix 2001:db8:8::/64 { AdvOnLink on; }; };",
	})
	controller := FilesystemController{RootDir: t.TempDir(), Runner: runner}

	// When
	err = controller.ReloadOrRestart(context.Background(), IPv6RA, artifacts)

	// Then
	assertReadbackMismatch(t, err, "2001:db8:7::/64")
}

func TestFilesystemController_evidence_is_not_fresh_when_live_content_becomes_stale(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	artifacts, err := RenderIPv6RA(IPv6RAPlan{Interface: "lan9", DelegatedPrefix: "2001:db8:9::/56"})
	if err != nil {
		t.Fatal(err)
	}
	runner := availableRunner(IPv6RA, map[string]string{
		"radvdump": "interface lan9 { prefix 2001:db8:9::/64 { AdvOnLink on; }; };",
	})
	controller := FilesystemController{RootDir: t.TempDir(), Runner: runner, Now: func() time.Time { return now }}
	if err := controller.ReloadOrRestart(context.Background(), IPv6RA, artifacts); err != nil {
		t.Fatal(err)
	}
	request := EvidenceRequest{TransactionID: "txn-ra-stale", Capability: "ipv6-ra", Artifacts: artifacts}
	if _, err := controller.Receipt(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	runner.outputs["radvdump"] = "interface lan9 { prefix 2001:db8:99::/64 { AdvOnLink on; }; };"

	// When
	readback, err := controller.Readback(context.Background(), request)

	// Then
	if err == nil || readback.Fresh {
		t.Fatalf("readback = %#v, error = %v; want stale semantic failure", readback, err)
	}
}

func availableRunner(service ServiceName, outputs map[string]string) *fakeRunner {
	return &fakeRunner{health: map[ServiceName]Health{service: {Service: service, Available: true}}, outputs: outputs}
}

func assertReadbackMismatch(t *testing.T, err error, expected string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Fatalf("apply error = %v, want semantic readback mismatch containing %q", err, expected)
	}
}
