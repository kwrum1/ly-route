package dataplane

import (
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/vpp"
)

func TestRenderDPDKStartupUsesStockVPPConfigurationAndIsIdempotent(t *testing.T) {
	attachments := []vpp.NativeAttachment{
		{LinuxInterface: "eth1", VPPInterface: "lyroute-eth1", PCIAddress: "0000:03:00.0"},
		{LinuxInterface: "eth2", VPPInterface: "lyroute-eth2", PCIAddress: "0000:04:00.0"},
	}
	first, err := RenderDPDKStartup([]byte("unix { nodaemon }\n"), attachments)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderDPDKStartup(first, attachments)
	if err != nil {
		t.Fatal(err)
	}
	text := string(second)
	for _, wanted := range []string{"dpdk {", "uio-driver vfio-pci", "dev 0000:03:00.0 { name lyroute-eth1 }", "dev 0000:04:00.0 { name lyroute-eth2 }"} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("startup config missing %q:\n%s", wanted, text)
		}
	}
	if strings.Count(text, managedDPDKStart) != 1 || strings.Contains(text, "native-driver-attach") {
		t.Fatalf("non-idempotent or custom CLI config:\n%s", text)
	}
}

func TestRenderDPDKStartupRejectsUnsafeOrDuplicatePCIIdentity(t *testing.T) {
	for _, attachments := range [][]vpp.NativeAttachment{
		{{VPPInterface: "lyroute-eth1;reboot", PCIAddress: "0000:03:00.0"}},
		{{VPPInterface: "lyroute-eth1", PCIAddress: "../../../etc"}},
		{{VPPInterface: "lyroute-eth1", PCIAddress: "0000:03:00.0"}, {VPPInterface: "lyroute-eth2", PCIAddress: "0000:03:00.0"}},
	} {
		if _, err := RenderDPDKStartup(nil, attachments); err == nil {
			t.Fatalf("accepted unsafe attachments %#v", attachments)
		}
	}
}
