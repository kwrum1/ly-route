package vpp

import "testing"

func TestTapBridgeInterfaceNames_areStableAndLinuxSafe(t *testing.T) {
	bridge, host := tapBridgeInterfaceNames(" ens33 ")
	if bridge != "lrbr-1bf492ea" || host != "lrh-1bf492ea" {
		t.Fatalf("tap bridge names = %q, %q", bridge, host)
	}
	if len(bridge) > 15 || len(host) > 15 {
		t.Fatalf("tap bridge names exceed Linux IFNAMSIZ: %q, %q", bridge, host)
	}
}

func TestDataplaneAttachOperation_buildsTapBridgeCommands(t *testing.T) {
	operation := DataplaneAttachOperation("req-tap-bridge", NativeAttachment{
		LinuxInterface: "ens33",
		VPPInterface:   "lyroute-ens33",
		Hook:           NativeHookTapBridge,
		Mode:           NativeModeTapBridge,
	})
	want := "?create tap if-name lyroute-ens33 host-if-name lrh-1bf492ea host-bridge lrbr-1bf492ea no-gso"
	if len(operation.VPPCtlCommands) == 0 || operation.VPPCtlCommands[0] != want {
		t.Fatalf("tap bridge commands = %#v", operation.VPPCtlCommands)
	}
}
