package pppoeclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
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

func vppRoutePath(remoteAddress, interfaceName string) []string {
	remoteAddress = strings.TrimSpace(remoteAddress)
	interfaceName = strings.TrimSpace(interfaceName)
	if remoteAddress == "" {
		return []string{"via", interfaceName}
	}
	if address, err := netip.ParseAddr(remoteAddress); err == nil && address.IsUnspecified() {
		return []string{"via", interfaceName}
	}
	return []string{"via", remoteAddress, interfaceName}
}

func vppPeerRouteCommand(remoteAddress, interfaceName string) []string {
	address, err := netip.ParseAddr(strings.TrimSpace(remoteAddress))
	if err != nil || address.IsUnspecified() {
		return nil
	}
	bits := 32
	if address.Is6() {
		bits = 128
	}
	command := []string{"ip", "route", "add", address.String() + "/" + strconv.Itoa(bits)}
	return append(command, vppRoutePath(address.String(), interfaceName)...)
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
	if config.InstallDefaultRoute {
		// A gateway reconciliation can leave the previous session's cached
		// default-route adjacency in place while the client is reconnecting.
		// Delete it before adding the current session route so the rewrite
		// cannot retain the old PPPoE session ID.
		deleteDefault := append([]string{"ip", "route", "del", "0.0.0.0/0"}, vppRoutePath(session.RemoteAddress, interfaceName)...)
		_, _ = runVPP(ctx, config.Binary, deleteDefault...)
	}
	commands := [][]string{
		{"set", "interface", "mtu", strconv.Itoa(int(config.MTU)), interfaceName},
		{"set", "interface", "ip", "address", interfaceName, session.LocalAddress + "/32"},
	}
	if peerRoute := vppPeerRouteCommand(session.RemoteAddress, interfaceName); len(peerRoute) > 0 {
		commands = append(commands, peerRoute)
	}
	if config.InstallDefaultRoute {
		defaultRoute := append([]string{"ip", "route", "add", "0.0.0.0/0"}, vppRoutePath(session.RemoteAddress, interfaceName)...)
		commands = append(commands, defaultRoute)
	}
	if config.EnableNAT {
		commands = append(commands, []string{"nat44", "plugin", "enable"})
		for _, inside := range config.NATInsideInterfaces {
			inside = strings.TrimSpace(inside)
			if inside != "" {
				commands = append(commands, []string{"set", "interface", "nat44", "in", inside})
			}
		}
		// DNS interception runs on the LAN input arc, so NAT must execute on
		// WAN output. Remove any stale input-mode outside role left by an
		// earlier release before installing the output feature.
		_, _ = runVPP(ctx, config.Binary, "set", "interface", "nat44", "out", interfaceName, "output-feature", "del")
		_, _ = runVPP(ctx, config.Binary, "set", "interface", "nat44", "out", interfaceName, "del")
		commands = append(commands,
			[]string{"nat44", "add", "interface", "address", interfaceName},
			[]string{"set", "interface", "nat44", "out", interfaceName, "output-feature"},
		)
	}
	for _, command := range commands {
		if _, err := runVPP(ctx, config.Binary, command...); err != nil {
			return VPPSession{}, err
		}
	}
	if config.IPv6PrefixGroup != "" && len(config.IPv6LANInterfaces) > 0 {
		if !session.IPv6Ready {
			return VPPSession{}, errors.New("IPv6 prefix delegation requires an active IPv6CP session")
		}
		if _, err := netip.ParsePrefix(session.DelegatedPrefix); err != nil {
			return VPPSession{}, fmt.Errorf("invalid delegated prefix %q: %w", session.DelegatedPrefix, err)
		}
		if err := ensureIPv6Interface(ctx, config.Binary, interfaceName); err != nil {
			return VPPSession{}, err
		}
		_, _ = runVPP(ctx, config.Binary, "set", "interface", "ip", "address", interfaceName, session.LocalIPv6+"/128")
		deleteIPv6Default := append([]string{"ip", "route", "del", "::/0"}, vppRoutePath(session.RemoteIPv6, interfaceName)...)
		_, _ = runVPP(ctx, config.Binary, deleteIPv6Default...)
		addIPv6Default := append([]string{"ip", "route", "add", "::/0"}, vppRoutePath(session.RemoteIPv6, interfaceName)...)
		if _, err := runVPP(ctx, config.Binary, addIPv6Default...); err != nil {
			return VPPSession{}, err
		}
		// VPP's stock DHCPv6-PD client emits through an Ethernet multicast
		// adjacency and cannot transmit over its PPPoE midchain interface.
		// The native Ly Route PPPoE client obtains the prefix over its dedicated
		// control path, then VPP owns routing and LAN router advertisements.
		_, _ = runVPP(ctx, config.Binary, "dhcp6", "pd", "client", interfaceName, "disable")
		if err := configureDelegatedPrefix(ctx, config, session.DelegatedPrefix); err != nil {
			return VPPSession{}, err
		}
	}
	cleanup = false
	return programmed, nil
}

func configureDelegatedPrefix(ctx context.Context, config VPPConfig, delegated string) error {
	prefix, address, err := delegatedLANPrefix(delegated)
	if err != nil {
		return err
	}
	for _, lan := range config.IPv6LANInterfaces {
		lan = strings.TrimSpace(lan)
		if lan == "" {
			continue
		}
		if err := ensureIPv6Interface(ctx, config.Binary, lan); err != nil {
			return err
		}
		_, _ = runVPP(ctx, config.Binary, "set", "ip6", "address", lan,
			"prefix", "group", config.IPv6PrefixGroup, "::1/64", "del")
		_, _ = runVPP(ctx, config.Binary, "set", "interface", "ip", "address", "del", lan, address)
		if _, err := runVPP(ctx, config.Binary, "set", "interface", "ip", "address", lan, address); err != nil {
			return err
		}
		if _, err := runVPP(ctx, config.Binary, "ip6", "nd", lan, "prefix", prefix, "default"); err != nil {
			return err
		}
		if _, err := runVPP(ctx, config.Binary, "ip6", "nd", lan, "ra-initial", "3", "1"); err != nil {
			return err
		}
	}
	return nil
}

func ensureIPv6Interface(ctx context.Context, binary, interfaceName string) error {
	interfaceName = strings.TrimSpace(interfaceName)
	if interfaceName == "" {
		return errors.New("IPv6 interface name is required")
	}
	if output, err := runVPP(ctx, binary, "show", "ip6", "interface", interfaceName); err == nil {
		// VPP 25.10 reports an already-enabled interface as a literal
		// "Failed" when enable is repeated. Readback is the authoritative,
		// idempotent check for reconnects and runtime re-apply.
		lower := strings.ToLower(output)
		if strings.Contains(lower, "is admin up") && !strings.Contains(lower, "ipv6 disabled") {
			return nil
		}
	}
	_, err := runVPP(ctx, binary, "enable", "ip6", "interface", interfaceName)
	return err
}

func removeDelegatedPrefix(ctx context.Context, config VPPConfig, delegated string) {
	prefix, address, err := delegatedLANPrefix(delegated)
	if err != nil {
		return
	}
	for _, lan := range config.IPv6LANInterfaces {
		lan = strings.TrimSpace(lan)
		if lan == "" {
			continue
		}
		_, _ = runVPP(ctx, config.Binary, "ip6", "nd", lan, "no", "prefix", prefix)
		_, _ = runVPP(ctx, config.Binary, "set", "interface", "ip", "address", "del", lan, address)
	}
}

func delegatedLANPrefix(delegated string) (string, string, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(delegated))
	if err != nil || !prefix.Addr().Is6() || prefix.Bits() > 64 {
		return "", "", fmt.Errorf("invalid delegated IPv6 prefix %q", delegated)
	}
	prefix = netip.PrefixFrom(prefix.Masked().Addr(), 64)
	raw := prefix.Addr().As16()
	raw[15] = 1
	address := netip.AddrFrom16(raw)
	return prefix.String(), netip.PrefixFrom(address, 64).String(), nil
}

func (session *VPPSession) UpdateDelegatedPrefix(ctx context.Context, delegated string) error {
	delegated = strings.TrimSpace(delegated)
	if delegated == session.Session.DelegatedPrefix {
		return nil
	}
	if err := configureDelegatedPrefix(ctx, session.config, delegated); err != nil {
		return err
	}
	removeDelegatedPrefix(ctx, session.config, session.Session.DelegatedPrefix)
	session.Session.DelegatedPrefix = delegated
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
		for _, inside := range session.config.NATInsideInterfaces {
			inside = strings.TrimSpace(inside)
			if inside != "" {
				_, _ = runVPP(ctx, binary, "set", "interface", "nat44", "in", inside, "del")
			}
		}
		_, _ = runVPP(ctx, binary, "set", "interface", "nat44", "out", session.Interface, "output-feature", "del")
		_, _ = runVPP(ctx, binary, "set", "interface", "nat44", "out", session.Interface, "del")
		_, _ = runVPP(ctx, binary, "nat44", "add", "interface", "address", session.Interface, "del")
	}
	removeDelegatedPrefix(ctx, session.config, session.Session.DelegatedPrefix)
	if session.Session.RemoteIPv6 != "" {
		_, _ = runVPP(ctx, binary, "ip", "route", "del", "::/0", "via", session.Session.RemoteIPv6, session.Interface)
	}
	if session.config.InstallDefaultRoute {
		deleteDefault := append([]string{"ip", "route", "del", "0.0.0.0/0"}, vppRoutePath(session.Session.RemoteAddress, session.Interface)...)
		_, _ = runVPP(ctx, binary, deleteDefault...)
	}
	if peerRoute := vppPeerRouteCommand(session.Session.RemoteAddress, session.Interface); len(peerRoute) > 0 {
		peerRoute[2] = "del"
		_, _ = runVPP(ctx, binary, peerRoute...)
	}
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
