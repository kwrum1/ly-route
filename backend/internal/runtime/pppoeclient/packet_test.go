package pppoeclient

import "testing"

func TestDiscoveryAndSessionRoundTrip(t *testing.T) {
	host := []byte{1, 2, 3, 4}
	discovery, err := Decode(EtherTypeDiscovery, EncodeDiscovery(CodePADO, 0, []Tag{{Type: TagServiceName, Value: []byte("isp")}, {Type: TagHostUnique, Value: host}}))
	if err != nil || discovery.Code != CodePADO || len(discovery.Tags) != 2 {
		t.Fatalf("discovery round trip = %#v, %v", discovery, err)
	}
	if value, ok := FindTag(discovery.Tags, TagHostUnique); !ok || string(value) != string(host) {
		t.Fatalf("host unique = %x, %v", value, ok)
	}
	control := EncodeControl(controlConfigureRequest, 7, []byte{1, 4, 5, 212})
	session, err := Decode(EtherTypeSession, EncodeSession(42, ProtocolLCP, control))
	if err != nil || session.SessionID != 42 || session.Protocol != ProtocolLCP {
		t.Fatalf("session round trip = %#v, %v", session, err)
	}
	code, id, body, err := DecodeControl(session.Payload)
	if err != nil || code != controlConfigureRequest || id != 7 || len(body) != 4 {
		t.Fatalf("control round trip = %d/%d/%x, %v", code, id, body, err)
	}
}

func TestIPCPOptionSelectionRejectsCompressionAndKeepsAddress(t *testing.T) {
	options := []byte{2, 6, 0, 45, 15, 1, 3, 6, 10, 67, 0, 1}
	rejected := unsupportedIPCPOptions(options)
	if len(rejected) != 6 || rejected[0] != 2 {
		t.Fatalf("rejected options = %x", rejected)
	}
	address, ok := addressOption(options)
	if !ok || address != [4]byte{10, 67, 0, 1} {
		t.Fatalf("address = %v, %v", address, ok)
	}
}

func TestParseVPPInterfaceIndex(t *testing.T) {
	output := "              Name               Idx    State\n" +
		"tap700                            12      up\n"
	index, err := parseInterfaceIndex(output, "tap700")
	if err != nil || index != 12 {
		t.Fatalf("index = %d, err = %v", index, err)
	}
}
