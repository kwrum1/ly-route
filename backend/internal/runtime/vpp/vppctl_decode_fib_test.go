package vpp

import "testing"

func TestParseConfiguredFIBPathListsPreservesPPPoEAttachedNextHop(t *testing.T) {
	results := []VPPCTLCommandResult{{
		Command: "show fib path-lists",
		Stdout: `path-list:[211] locks:2 flags:shared, uPRF-list:175 len:2 itfs:[15, 17, ]
  path:[244] pl-index:211 ip4 weight=7 pref=0 attached-nexthop: oper-flags:resolved,
    10.67.0.1 pppoe_session_lyprimary (p2p)
  [@0]: ipv4 via 0.0.0.0 pppoe_session_lyprimary: mtu:9000 next:6 flags:[]
  path:[245] pl-index:211 ip4 weight=3 pref=0 attached-nexthop: oper-flags:resolved,
    10.68.0.1 pppoe_session_lybackup (p2p)
  [@0]: ipv4 via 0.0.0.0 pppoe_session_lybackup: mtu:9000 next:6 flags:[]`,
	}}

	lists, err := parseConfiguredFIBPathLists(results)
	if err != nil {
		t.Fatalf("parseConfiguredFIBPathLists() error = %v", err)
	}
	if len(lists) != 1 || len(lists[0]) != 2 {
		t.Fatalf("path lists = %#v", lists)
	}
	if got, want := lists[0][0], (fibPath{via: "10.67.0.1 pppoe_session_lyprimary", weight: 7}); got != want {
		t.Fatalf("primary path = %#v, want %#v", got, want)
	}
	if got, want := lists[0][1], (fibPath{via: "10.68.0.1 pppoe_session_lybackup", weight: 3}); got != want {
		t.Fatalf("backup path = %#v, want %#v", got, want)
	}
}
