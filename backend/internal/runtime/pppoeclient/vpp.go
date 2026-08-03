package pppoeclient

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
)

type VPPConfig struct {
	Binary              string
	TableID             int
	MTU                 uint16
	InstallDefaultRoute bool
	EnableNAT           bool
	NATInsideInterfaces []string
	IPv6PrefixGroup     string
	IPv6LANInterfaces   []string
	VAT2Binary          string
}

type VPPSession struct {
	Interface string  `json:"interface"`
	Session   Session `json:"session"`
	config    VPPConfig
}

func ProgramVPP(ctx context.Context, config VPPConfig, session Session) (VPPSession, error) {
	if strings.TrimSpace(config.Binary) == "" {
		config.Binary = "vppctl"
	}
	if config.MTU == 0 {
		config.MTU = 1492
	}
	ac := net.HardwareAddr(session.ACMAC[:]).String()
	output, err := runVPP(ctx, config.Binary, "create", "pppoe", "session", "client-ip", session.LocalAddress, "session-id", strconv.Itoa(int(session.ID)), "client-mac", ac, "decap-vrf-id", strconv.Itoa(config.TableID))
	if err != nil {
		return VPPSession{}, err
	}
	interfaceName := strings.TrimSpace(output)
	if fields := strings.Fields(interfaceName); len(fields) > 0 {
		interfaceName = fields[len(fields)-1]
	}
	if !strings.HasPrefix(interfaceName, "pppoe_session") {
		return VPPSession{}, fmt.Errorf("VPP returned invalid PPPoE interface %q", interfaceName)
	}
	programmed := VPPSession{Interface: interfaceName, Session: session, config: config}
	cleanup := true
	defer func() {
		if cleanup {
			_ = programmed.Remove(context.Background())
		}
	}()
	// VPP reuses hidden PPPoE interface indices. Remove the previous negotiated
	// address before assigning the current one so reconnects are idempotent.
	_, _ = runVPP(ctx, config.Binary, "set", "interface", "ip", "address", "del", interfaceName, "all")
	commands := [][]string{
		{"set", "interface", "mtu", strconv.Itoa(int(config.MTU)), interfaceName},
		{"set", "interface", "ip", "address", interfaceName, session.LocalAddress + "/32"},
		{"ip", "route", "add", session.RemoteAddress + "/32", "via", interfaceName},
	}
	if config.InstallDefaultRoute {
		commands = append(commands, []string{"ip", "route", "add", "0.0.0.0/0", "via", session.RemoteAddress, interfaceName})
	}
	if config.EnableNAT {
		commands = append(commands, []string{"nat44", "plugin", "enable"})
		for _, inside := range config.NATInsideInterfaces {
			inside = strings.TrimSpace(inside)
			if inside != "" {
				commands = append(commands, []string{"set", "interface", "nat44", "in", inside})
			}
		}
		commands = append(commands,
			[]string{"nat44", "add", "interface", "address", interfaceName},
			[]string{"set", "interface", "nat44", "out", interfaceName},
		)
	}
	for _, command := range commands {
		if _, err := runVPP(ctx, config.Binary, command...); err != nil {
			return VPPSession{}, err
		}
	}
	if session.IPv6Ready && config.IPv6PrefixGroup != "" && len(config.IPv6LANInterfaces) > 0 {
		if _, err := runVPP(ctx, config.Binary, "enable", "ip6", "interface", interfaceName); err != nil {
			return VPPSession{}, err
		}
		if _, err := runVPP(ctx, config.Binary, "ip", "route", "add", "::/0", "via", session.RemoteIPv6, interfaceName); err != nil {
			return VPPSession{}, err
		}
		if err := enableDHCPv6PD(ctx, config, interfaceName); err != nil {
			return VPPSession{}, err
		}
	}
	cleanup = false
	return programmed, nil
}

func enableDHCPv6PD(ctx context.Context, config VPPConfig, wanInterface string) error {
	if _, err := runVPP(ctx, config.Binary, "dhcp6", "pd", "client", wanInterface,
		"prefix", "group", config.IPv6PrefixGroup); err != nil {
		return err
	}
	for _, lan := range config.IPv6LANInterfaces {
		lan = strings.TrimSpace(lan)
		if lan == "" {
			continue
		}
		if _, err := runVPP(ctx, config.Binary, "enable", "ip6", "interface", lan); err != nil {
			return err
		}
		if _, err := runVPP(ctx, config.Binary, "set", "ip6", "address", lan,
			"prefix", "group", config.IPv6PrefixGroup, "::1/64"); err != nil {
			return err
		}
	}
	return nil
}

func (session VPPSession) Remove(ctx context.Context) error {
	if session.Interface == "" {
		return nil
	}
	binary := session.config.Binary
	if binary == "" {
		binary = "vppctl"
	}
	if session.config.EnableNAT {
		_, _ = runVPP(ctx, binary, "set", "interface", "nat44", "out", session.Interface, "del")
		_, _ = runVPP(ctx, binary, "nat44", "add", "interface", "address", session.Interface, "del")
	}
	if session.config.InstallDefaultRoute {
		_, _ = runVPP(ctx, binary, "ip", "route", "del", "0.0.0.0/0", "via", session.Session.RemoteAddress, session.Interface)
	}
	_, _ = runVPP(ctx, binary, "ip", "route", "del", session.Session.RemoteAddress+"/32", "via", session.Interface)
	_, _ = runVPP(ctx, binary, "set", "interface", "ip", "address", "del", session.Interface, "all")
	ac := net.HardwareAddr(session.Session.ACMAC[:]).String()
	_, err := runVPP(ctx, binary, "create", "pppoe", "session", "client-ip", session.Session.LocalAddress, "session-id", strconv.Itoa(int(session.Session.ID)), "client-mac", ac, "decap-vrf-id", strconv.Itoa(session.config.TableID), "del")
	return err
}

func runVPP(ctx context.Context, binary string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, binary, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s failed: %w: %s", binary, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	text := strings.TrimSpace(string(output))
	lower := strings.ToLower(text)
	if strings.Contains(lower, "unknown input") || strings.Contains(lower, "failed") || strings.Contains(lower, "parse error") {
		return "", fmt.Errorf("%s %s failed: %s", binary, strings.Join(args, " "), text)
	}
	return string(output), nil
}
