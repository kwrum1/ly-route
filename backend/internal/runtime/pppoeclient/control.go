package pppoeclient

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

type ControlPlaneConfig struct {
	VPPCTL        string
	WANInterface  string
	HostInterface string
	TapID         int
}

type ControlPlane struct {
	Config       ControlPlaneConfig
	VPPInterface string
	VPPIfIndex   int
	PhysicalMAC  net.HardwareAddr
}

var ethernetAddressPattern = regexp.MustCompile(`(?im)^\s*Ethernet address\s+([0-9a-f:]{17})\s*$`)

func PrepareControlPlane(ctx context.Context, config ControlPlaneConfig) (ControlPlane, error) {
	if config.VPPCTL == "" {
		config.VPPCTL = "vppctl"
	}
	if config.WANInterface == "" || config.HostInterface == "" || config.TapID < 0 {
		return ControlPlane{}, fmt.Errorf("PPPoE control plane requires WAN interface, host interface, and tap ID")
	}
	hardware, err := runVPP(ctx, config.VPPCTL, "show", "hardware-interfaces", config.WANInterface)
	if err != nil {
		return ControlPlane{}, err
	}
	match := ethernetAddressPattern.FindStringSubmatch(hardware)
	if len(match) != 2 {
		return ControlPlane{}, fmt.Errorf("VPP WAN %s has no readable Ethernet address", config.WANInterface)
	}
	mac, err := net.ParseMAC(match[1])
	if err != nil {
		return ControlPlane{}, err
	}
	vppInterface := "tap" + strconv.Itoa(config.TapID)
	show, showErr := runVPP(ctx, config.VPPCTL, "show", "interface", vppInterface)
	if showErr != nil || !strings.Contains(show, vppInterface) {
		created, err := runVPP(ctx, config.VPPCTL, "create", "tap", "id", strconv.Itoa(config.TapID), "host-if-name", config.HostInterface, "host-mac-addr", mac.String())
		if err != nil {
			return ControlPlane{}, err
		}
		vppInterface = strings.TrimSpace(created)
		if fields := strings.Fields(vppInterface); len(fields) > 0 {
			vppInterface = fields[len(fields)-1]
		}
	}
	cleanup := func() {}
	if _, err := runVPP(ctx, config.VPPCTL, "set", "interface", "state", vppInterface, "up"); err != nil {
		cleanup()
		return ControlPlane{}, err
	}
	if err := exec.CommandContext(ctx, "ip", "link", "set", config.HostInterface, "address", mac.String(), "up").Run(); err != nil {
		cleanup()
		return ControlPlane{}, fmt.Errorf("prepare Linux PPPoE control interface: %w", err)
	}
	show, err = runVPP(ctx, config.VPPCTL, "show", "interface", vppInterface)
	if err != nil {
		cleanup()
		return ControlPlane{}, err
	}
	ifIndex, err := parseInterfaceIndex(show, vppInterface)
	if err != nil {
		cleanup()
		return ControlPlane{}, err
	}
	if _, err := runVPP(ctx, config.VPPCTL, "create", "pppoe", "cp", "cp-if-index", strconv.Itoa(ifIndex)); err != nil {
		cleanup()
		return ControlPlane{}, err
	}
	if _, err := runVPP(ctx, config.VPPCTL, "set", "ly-route", "pppoe-client", "control-interface", vppInterface, "wan-interface", config.WANInterface); err != nil {
		cleanup()
		return ControlPlane{}, err
	}
	return ControlPlane{Config: config, VPPInterface: vppInterface, VPPIfIndex: ifIndex, PhysicalMAC: mac}, nil
}

func (control ControlPlane) Remove(ctx context.Context) error {
	// VPP may still have control frames queued for this interface when a session
	// exits. Keep the deterministic TAP and CP binding for the next reconnect;
	// deleting it hot can race interface-output in VPP 25.10.
	return nil
}

func parseInterfaceIndex(output, interfaceName string) (int, error) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == interfaceName {
			index, err := strconv.Atoi(fields[1])
			if err == nil {
				return index, nil
			}
		}
	}
	return 0, fmt.Errorf("VPP interface %s has no readable index", interfaceName)
}
