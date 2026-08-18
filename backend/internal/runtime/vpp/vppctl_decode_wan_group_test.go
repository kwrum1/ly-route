package vpp

import (
	"fmt"
	"testing"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestDecodeVPPCTLWANGroupsAcceptsResolvedPPPoEForwardingPaths(t *testing.T) {
	group := trafficpolicy.WANGroup{
		ID:      "dual-pppoe",
		Mode:    trafficpolicy.WANGroupWeighted,
		Members: []string{"wan-primary", "wan-backup"},
		Weights: map[string]int{"wan-primary": 7, "wan-backup": 3},
		Paths: map[string]trafficpolicy.WANPath{
			"wan-primary": {VPPInterface: "pppoe_session_lyprimary", NextHop: "10.67.0.1"},
			"wan-backup":  {VPPInterface: "pppoe_session_lybackup", NextHop: "10.68.0.1"},
		},
	}
	tableID := wanGroupTableID(group.ID)
	results := []VPPCTLCommandResult{
		{
			Command: fmt.Sprintf("show ip fib table %d", tableID),
			Stdout: fmt.Sprintf(`ipv4-VRF:%d, fib_index:15, flow hash:[src dst sport dport proto]
0.0.0.0/0
  unicast-ip4-chain
  [@0]: dpo-load-balance: [proto:ip4 index:140 buckets:4 uRPF:175 to:[0:0]]
    [0-2] [@6]: ipv4 via 0.0.0.0 pppoe_session_lyprimary: mtu:9000 next:6 flags:[]
    [3] [@6]: ipv4 via 0.0.0.0 pppoe_session_lybackup: mtu:9000 next:6 flags:[]`, tableID),
		},
		{
			Command: "show fib path-lists",
			Stdout: `path-list:[211] locks:2 flags:shared, uPRF-list:175 len:2 itfs:[15, 17, ]
  path:[244] pl-index:211 ip4 weight=7 pref=0 attached-nexthop: oper-flags:resolved,
    10.67.0.1 pppoe_session_lyprimary (p2p)
  [@0]: ipv4 via 0.0.0.0 pppoe_session_lyprimary: mtu:9000 next:6 flags:[]
  path:[245] pl-index:211 ip4 weight=3 pref=0 attached-nexthop: oper-flags:resolved,
    10.68.0.1 pppoe_session_lybackup (p2p)
  [@0]: ipv4 via 0.0.0.0 pppoe_session_lybackup: mtu:9000 next:6 flags:[]`,
		},
	}

	readback, err := decodeVPPCTLWANGroups(SnapshotRequest{
		WANGroups:  []string{group.ID},
		Candidates: SnapshotCandidates{WANGroups: []trafficpolicy.WANGroup{group}},
	}, results)
	if err != nil {
		t.Fatalf("decodeVPPCTLWANGroups() error = %v", err)
	}
	if len(readback.Groups) != 1 || readback.Groups[0].ID != group.ID {
		t.Fatalf("groups = %#v", readback.Groups)
	}
}

func TestDecodeVPPCTLWANGroupsTreatsPassiveFailoverAsRepairableDrift(t *testing.T) {
	group := trafficpolicy.WANGroup{
		ID:      "dual-pppoe",
		Mode:    trafficpolicy.WANGroupPrimaryBackup,
		Members: []string{"wan-primary", "wan-backup"},
		Weights: map[string]int{"wan-primary": 1, "wan-backup": 1},
		Paths: map[string]trafficpolicy.WANPath{
			"wan-primary": {VPPInterface: "pppoe_session12", NextHop: "10.67.0.1"},
			"wan-backup":  {VPPInterface: "pppoe_session10", NextHop: "10.68.0.1"},
		},
	}
	tableID := wanGroupTableID(group.ID)
	results := []VPPCTLCommandResult{
		{
			Command: fmt.Sprintf("show ip fib table %d", tableID),
			Stdout: fmt.Sprintf(`ipv4-VRF:%d, fib_index:15, flow hash:[src dst sport dport proto]
0.0.0.0/0
  unicast-ip4-chain
  [@0]: dpo-load-balance: [proto:ip4 index:140 buckets:1 uRPF:175 to:[0:0]]
    [0] [@6]: ipv4 via 0.0.0.0 pppoe_session10: mtu:9000 next:6 flags:[]`, tableID),
		},
		{
			Command: "show fib path-lists",
			Stdout: `path-list:[211] locks:2 flags:shared, uPRF-list:175 len:1 itfs:[17, ]
  path:[245] pl-index:211 ip4 weight=1 pref=1 attached-nexthop: oper-flags:resolved,
    10.68.0.1 pppoe_session10 (p2p)`,
		},
	}

	request := SnapshotRequest{
		AllowMissing: true,
		WANGroups:    []string{group.ID},
		Candidates:   SnapshotCandidates{WANGroups: []trafficpolicy.WANGroup{group}},
	}
	readback, err := decodeVPPCTLWANGroups(request, results)
	if err != nil || len(readback.Groups) != 0 {
		t.Fatalf("passive failover readback = %#v, %v; want repairable drift", readback, err)
	}

	request.AllowMissing = false
	if _, err := decodeVPPCTLWANGroups(request, results); err == nil {
		t.Fatal("strict post-apply readback accepted a failover path as configured state")
	}
}
