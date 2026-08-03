package service

import (
	"context"
	"testing"

	"ly-route/backend/internal/runtime/proxy"
	"ly-route/backend/internal/runtime/vpp"
)

func TestFilesystemController_semantic_readback_accepts_plan_derived_live_state(t *testing.T) {
	// Given
	vppArtifacts, err := RenderVPPOperations([]vpp.Operation{{
		Name: "vpp.interface.address", RequestID: "txn-vpp", Resource: "wan-blue",
		VPPCtlCommands: []string{"set interface state wan-blue up"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	policyArtifacts, err := RenderLinuxPolicyRouting(proxy.LinuxPolicyRoutingPlan{
		EgressID: "edge-blue", Mark: "0x7", MarkMask: "0xffffffff", TableID: 1701, RulePriority: 1702,
		RuleSelector: proxy.LinuxRuleSelector{Family: "inet", Mark: "0x7", Mask: "0xffffffff", Table: 1701},
		DefaultRoute: proxy.LinuxDefaultRoute{Destination: "default", Table: 1701, Device: "lo", Scope: "link"},
	})
	if err != nil {
		t.Fatal(err)
	}
	nftArtifacts, err := RenderNftablesCapture(proxy.NftablesCapturePlan{
		EgressID: "edge-blue", Family: "inet", Table: "capture_blue", TargetPort: 15432, Mark: "0x7",
		Chains: []proxy.NftablesChain{{Name: "capture_pre", Type: "filter", Hook: "prerouting", Priority: -151, Policy: "accept"}},
		Rules: []proxy.NftablesRule{
			{Order: 1, EgressID: "edge-blue", Chain: "capture_pre", Expression: "tcp dport 53", Action: "return"},
			{Order: 2, EgressID: "edge-blue", Chain: "capture_pre", Expression: "udp dport 53", Action: "return"},
			{Order: 3, EgressID: "edge-blue", Chain: "capture_pre", Expression: "meta l4proto tcp", Action: "tproxy to :15432 mark set 0x7 accept"},
			{Order: 4, EgressID: "edge-blue", Chain: "capture_pre", Expression: "meta l4proto udp", Action: "tproxy to :15432 mark set 0x7 accept"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	pppoeArtifacts, err := RenderPPPoE(PPPoEPeer{ID: "wan-blue", Interface: "eth7", Username: "subscriber", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		service   ServiceName
		artifacts []RenderedArtifact
		outputs   map[string]string
	}{
		{
			name: "vpp expected object", service: VPP, artifacts: vppArtifacts,
			outputs: map[string]string{
				"vppctl show version":                          "vpp v24.10",
				"cat /var/lib/ly-route/vpp-apply-receipt.json": `{"status":"applied","operations":[{"name":"vpp.interface.address","resource":"wan-blue","results":[{"command":"set interface state wan-blue up","status":"applied"}]}]}`,
			},
		},
		{
			name: "policy expected rule and route", service: LinuxRouting, artifacts: policyArtifacts,
			outputs: map[string]string{
				"ip -j rule show":             `[{"priority":1702,"fwmark":"0x7","fwmask":"0xffffffff","table":1701}]`,
				"ip -j route show table 1701": `[{"dst":"default","dev":"lo","scope":"link"}]`,
			},
		},
		{
			name: "nft expected DNS and TProxy order", service: Nftables, artifacts: nftArtifacts,
			outputs: map[string]string{"nft list table inet capture_blue": nftArtifacts[0].Content},
		},
		{
			name: "native VPP PPPoE session", service: PPPd, artifacts: pppoeArtifacts,
			outputs: map[string]string{
				"cat /run/ly-route/pppoe/wan-blue.json":        `{"state":"connected","interface":"pppoe_session0","session":{"local_address":"198.51.100.8"}}`,
				"vppctl show pppoe session":                    "sw_if_index 8 client-ip 198.51.100.8 session-id 7",
				"vppctl show interface address pppoe_session0": "pppoe_session0 (up): 198.51.100.8/32",
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			controller := FilesystemController{RootDir: t.TempDir(), Runner: availableRunner(testCase.service, testCase.outputs)}

			// When
			err := controller.ReloadOrRestart(context.Background(), testCase.service, testCase.artifacts)

			// Then
			if err != nil {
				t.Fatalf("semantic apply failed: %v", err)
			}
		})
	}
}
