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
