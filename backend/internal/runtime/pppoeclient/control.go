package pppoeclient

import (
	"context"
	"fmt"
	"net"
	"os"
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

func pppoeBindingCommand(controlInterface, wanInterface string, disable bool) []string {
	command := []string{"set", "ly-route", "pppoe-client", "control-interface", controlInterface, "wan-interface", wanInterface}
	if disable {
		command = append(command, "disable")
	}
	return command
}

var ethernetAddressPattern = regexp.MustCompile(`(?im)^\s*Ethernet address\s+([0-9a-f:]{17})\s*$`)

func afPacketLinuxInterface(hardware, vppInterface string) string {
	if !strings.Contains(strings.ToLower(hardware), "linux packet socket interface") {
		return ""
	}
	pattern := regexp.MustCompile(`(?im)^\s*` + regexp.QuoteMeta(vppInterface) + `\s+\d+\s+\S+\s+host-(\S+)\s*$`)
	match := pattern.FindStringSubmatch(hardware)
	if len(match) != 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}

func prepareAFPacketMAC(ctx context.Context, vppctl, vppInterface string, hardware string, mac net.HardwareAddr) (net.HardwareAddr, error) {
	linuxInterface := afPacketLinuxInterface(hardware, vppInterface)
	if linuxInterface == "" {
		return mac, nil
	}
	address, err := os.ReadFile("/sys/class/net/" + linuxInterface + "/address")
	if err != nil {
		return nil, fmt.Errorf("read AF_PACKET Linux MAC for %s: %w", linuxInterface, err)
	}
	linuxMAC, err := net.ParseMAC(strings.TrimSpace(string(address)))
	if err != nil {
		return nil, fmt.Errorf("parse AF_PACKET Linux MAC for %s: %w", linuxInterface, err)
	}
	if _, err := runVPP(ctx, vppctl, "set", "interface", "mac", "address", vppInterface, linuxMAC.String()); err != nil {
		return nil, fmt.Errorf("synchronize AF_PACKET VPP MAC for %s: %w", vppInterface, err)
	}
	return linuxMAC, nil
}

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
	mac, err = prepareAFPacketMAC(ctx, config.VPPCTL, config.WANInterface, hardware, mac)
	if err != nil {
		return ControlPlane{}, err
	}
	// DPDK and native hardware interfaces can exist while administratively
	// down. PPPoE discovery frames otherwise reach the control TAP but are
	// dropped at the physical egress. Bring the selected WAN up before binding
	// the discovery relay.
	if _, err := runVPP(ctx, config.VPPCTL, "set", "interface", "state", config.WANInterface, "up"); err != nil {
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
	if _, err := runVPP(ctx, config.VPPCTL, "set", "interface", "mac", "address", vppInterface, mac.String()); err != nil {
		return ControlPlane{}, err
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
	// Stock VPP exposes one global PPPoE control interface. The Ly Route relay
	// owns negotiation per WAN so concurrent clients cannot replace each other.
	if _, err := runVPP(ctx, config.VPPCTL, pppoeBindingCommand(vppInterface, config.WANInterface, false)...); err != nil {
		cleanup()
		return ControlPlane{}, err
	}
	return ControlPlane{Config: config, VPPInterface: vppInterface, VPPIfIndex: ifIndex, PhysicalMAC: mac}, nil
}

func (control ControlPlane) Remove(ctx context.Context) error {
	if strings.TrimSpace(control.VPPInterface) == "" || strings.TrimSpace(control.Config.WANInterface) == "" {
		return nil
	}
	_, err := runVPP(ctx, control.Config.VPPCTL, pppoeBindingCommand(control.VPPInterface, control.Config.WANInterface, true)...)
	return err
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
