package gateway

import "testing"

func TestParseVPPInterfaceTelemetryStopsAtEveryInterfaceHeader(t *testing.T) {
	output := `              Name               Idx    State  MTU (L3/IP4/IP6/MPLS)     Counter          Count
lyroute-ens192                    1      up          9000/0/0/0     rx packets                   637
                                                                    rx bytes                  80459
                                                                    tx packets                 2630
                                                                    tx bytes                 323794
pppoe_session0                    8      up             0/0/0/0     rx packets                     6
                                                                    rx bytes                    472
                                                                    tx packets                    4
                                                                    tx bytes                    424
lyroute-ens224                    2      up          9000/0/0/0     rx packets                  7635
                                                                    rx bytes                 931460
                                                                    tx packets                 3107
                                                                    tx bytes                 339653
tap4096                           3      up          9000/0/0/0     rx packets                     7
                                                                    rx bytes                    746
                                                                    tx packets                    2
                                                                    tx bytes                    120
`

	items := parseVPPInterfaceTelemetry(output)
	if len(items) != 2 {
		t.Fatalf("interfaces = %#v, want two LY-Route physical interfaces", items)
	}
	if got := items[0]["rx_bytes"]; got != int64(80459) {
		t.Fatalf("LAN rx_bytes = %#v, want 80459", got)
	}
	if got := items[0]["tx_bytes"]; got != int64(323794) {
		t.Fatalf("LAN tx_bytes = %#v, want 323794", got)
	}
	if got := items[1]["rx_bytes"]; got != int64(931460) {
		t.Fatalf("WAN rx_bytes = %#v, want 931460", got)
	}
	if got := items[1]["tx_bytes"]; got != int64(339653) {
		t.Fatalf("WAN tx_bytes = %#v, want 339653", got)
	}
	for _, item := range items {
		if _, exists := item["active_path"]; exists {
			t.Fatalf("collector guessed active_path instead of using apply readback: %#v", item)
		}
		if _, exists := item["work_mode"]; exists {
			t.Fatalf("collector guessed work_mode instead of using apply readback: %#v", item)
		}
	}
}
