package httpapi

import "testing"

func TestPPPPathFromStatusJSONUsesNegotiatedPeer(t *testing.T) {
	interfaceName, nextHop, connected := pppPathFromStatusJSON([]byte(`{
		"state":"connected",
		"interface":"pppoe_session0",
		"session":{"remote_address":"10.67.0.1"}
	}`))
	if !connected || interfaceName != "pppoe_session0" || nextHop != "10.67.0.1" {
		t.Fatalf("live PPPoE path = (%q, %q, %t)", interfaceName, nextHop, connected)
	}
}

func TestPPPPathFromStatusJSONKeepsInterfaceWhenPeerIsUnavailable(t *testing.T) {
	interfaceName, nextHop, connected := pppPathFromStatusJSON([]byte(`{
		"state":"connected",
		"interface":"pppoe_session0",
		"session":{}
	}`))
	if !connected || interfaceName != "pppoe_session0" || nextHop != "" {
		t.Fatalf("incomplete live PPPoE path = (%q, %q, %t)", interfaceName, nextHop, connected)
	}
}

func TestPPPPathFromStatusJSONRejectsDisconnectedState(t *testing.T) {
	if _, _, connected := pppPathFromStatusJSON([]byte(`{"state":"disconnected"}`)); connected {
		t.Fatal("disconnected PPPoE state reported as connected")
	}
}

func TestPPPRuntimePathTokenChangesAcrossReconnects(t *testing.T) {
	first := []byte(`{"state":"connected","interface":"pppoe_session0","session":{"session_id":1,"local_address":"10.67.0.10","remote_address":"10.67.0.1"}}`)
	second := []byte(`{"state":"connected","interface":"pppoe_session0","session":{"session_id":2,"local_address":"10.67.0.10","remote_address":"10.67.0.1"}}`)
	_, _, firstToken, firstConnected := pppRuntimePathFromStatusJSON(first)
	_, _, secondToken, secondConnected := pppRuntimePathFromStatusJSON(second)
	if !firstConnected || !secondConnected || firstToken == "" || firstToken == secondToken {
		t.Fatalf("PPPoE runtime tokens = %q, %q", firstToken, secondToken)
	}
}

func TestVPPInterfaceHasAddressUsesInterfaceInventory(t *testing.T) {
	output := "lyroute-ens33 (up):\n  L3 10.67.0.10/32\nlyroute-ens35 (up):\n  L3 10.68.0.10/32\n"
	if !vppInterfaceHasAddress(output, "lyroute-ens35", "10.68.0.10") {
		t.Fatal("expected negotiated address in the selected VPP interface section")
	}
	if vppInterfaceHasAddress(output, "lyroute-ens35", "10.67.0.10") {
		t.Fatal("address from another interface must not satisfy the selected path")
	}
}
