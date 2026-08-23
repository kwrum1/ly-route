package dataplane

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/vpp"
)

func TestRenderDPDKStartupUsesStockVPPConfigurationAndIsIdempotent(t *testing.T) {
	attachments := []vpp.NativeAttachment{
		{LinuxInterface: "eth1", VPPInterface: "lyroute-eth1", Mode: vpp.NativeModeDPDKVFIO, PCIAddress: "0000:03:00.0"},
		{LinuxInterface: "eth2", VPPInterface: "lyroute-eth2", Mode: vpp.NativeModeDPDKVFIO, PCIAddress: "0000:04:00.0"},
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
	for _, wanted := range []string{"dpdk {", "uio-driver vfio-pci", "dev 0000:03:00.0 {", "name lyroute-eth1", "dev 0000:04:00.0 {", "name lyroute-eth2", "no-rx-interrupts"} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("startup config missing %q:\n%s", wanted, text)
		}
	}
	if strings.Count(text, managedDPDKStart) != 1 || strings.Contains(text, "native-driver-attach") {
		t.Fatalf("non-idempotent or custom CLI config:\n%s", text)
	}
	if strings.Count(text, "no-rx-interrupts") != len(attachments) {
		t.Fatalf("DPDK devices are not pinned to high-performance polling mode:\n%s", text)
	}
}

func TestRenderDPDKStartupRejectsUnsafeOrDuplicatePCIIdentity(t *testing.T) {
	for _, attachments := range [][]vpp.NativeAttachment{
		{{VPPInterface: "lyroute-eth1;reboot", Mode: vpp.NativeModeDPDKVFIO, PCIAddress: "0000:03:00.0"}},
		{{VPPInterface: "lyroute-eth1", Mode: vpp.NativeModeDPDKVFIO, PCIAddress: "../../../etc"}},
		{{VPPInterface: "lyroute-eth1", Mode: vpp.NativeModeDPDKVFIO, PCIAddress: "0000:03:00.0"}, {VPPInterface: "lyroute-eth2", Mode: vpp.NativeModeDPDKVFIO, PCIAddress: "0000:03:00.0"}},
	} {
		if _, err := RenderDPDKStartup(nil, attachments); err == nil {
			t.Fatalf("accepted unsafe attachments %#v", attachments)
		}
	}
}

func TestRenderDPDKStartupSupportsUIOPCIGenericFallback(t *testing.T) {
	attachments := []vpp.NativeAttachment{{LinuxInterface: "eth1", VPPInterface: "lyroute-eth1", Mode: vpp.NativeModeDPDKUIO, PCIAddress: "0000:03:00.0"}}
	config, err := RenderDPDKStartup([]byte("unix { nodaemon }\n"), attachments)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "uio-driver uio_pci_generic") {
		t.Fatalf("UIO fallback is missing from startup config:\n%s", config)
	}
}

func TestHardwareReadbackContainsDPDKPaddedPCIFunction(t *testing.T) {
	readback := "pci: device 15ad:07b0 address 0000:0b:00.00 numa 0"
	if !hardwareReadbackContainsPCI(readback, "0000:0b:00.0") {
		t.Fatal("did not accept DPDK's zero-padded PCI function")
	}
	if hardwareReadbackContainsPCI(readback, "0000:13:00.0") {
		t.Fatal("accepted a different PCI identity")
	}
}

func TestLinuxHostRebindsOnlyActiveKea(t *testing.T) {
	t.Run("active", func(t *testing.T) {
		// Given
		runner := &recordingRunner{}
		host := &LinuxHost{Runner: runner}

		// When
		err := host.RebindKea(context.Background())

		// Then
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"systemctl is-active --quiet kea-dhcp4-server.service", "systemctl restart kea-dhcp4-server.service"}; !reflect.DeepEqual(runner.commands, want) {
			t.Fatalf("commands=%v, want=%v", runner.commands, want)
		}
	})

	t.Run("inactive", func(t *testing.T) {
		// Given
		runner := &recordingRunner{failures: map[string]error{"systemctl is-active --quiet kea-dhcp4-server.service": errors.New("inactive")}}
		host := &LinuxHost{Runner: runner}

		// When
		err := host.RebindKea(context.Background())

		// Then
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"systemctl is-active --quiet kea-dhcp4-server.service"}; !reflect.DeepEqual(runner.commands, want) {
			t.Fatalf("commands=%v, want=%v", runner.commands, want)
		}
	})
}

type recordingRunner struct {
	commands []string
	failures map[string]error
}

func (runner *recordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	command := strings.TrimSpace(name + " " + strings.Join(args, " "))
	runner.commands = append(runner.commands, command)
	return nil, runner.failures[command]
}
