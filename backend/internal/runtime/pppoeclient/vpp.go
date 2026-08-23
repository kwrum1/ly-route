package pppoeclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type VPPConfig struct {
	Binary       string
	WANInterface string
	// SessionInterface is accepted for compatibility with older rendered
	// configs. VPP-native PPPoE interfaces must retain their allocator name:
	// renaming a pooled session can attach a later reconnect to another WAN.
	SessionInterface    string
	ExplicitWAN         bool
	TableID             int
	MTU                 uint16
	InstallDefaultRoute bool
	EnableNAT           bool
	NATInsideInterfaces []string
	NATBehavior         string
	IPv6PrefixGroup     string
	IPv6LANInterfaces   []string
	VAT2Binary          string
}

type VPPSession struct {
	Interface string  `json:"interface"`
	Session   Session `json:"session"`
	config    VPPConfig
	// staleSlots is retained in the internal value for compatibility with the
	// allocator contract. Successful allocation releases all placeholders
	// before returning, so they never enter runtime readback as live interfaces.
	staleSlots []vppPPPoESessionInfo
}

var (
	vppPPPoESessionLine    = regexp.MustCompile(`^\s*\[(\d+)\]\s+sw-if-index\s+(\d+)\s+client-ip\s+(\S+)\s+session-id\s+(\d+)(?:\s+encap-if-index\s+(\d+))?`)
	vppPPPoEClientMACLine  = regexp.MustCompile(`client-mac\s+([0-9a-fA-F:]{17})`)
	vppInterfaceStateLine  = regexp.MustCompile(`^\s*(\S+)\s+(\d+)\s+(?:up|down)\s+`)
	vppIPv4FIBTableLine    = regexp.MustCompile(`^ipv4-VRF:(\d+),`)
	vppIPv6DefaultPathLine = regexp.MustCompile(`^\s*([0-9a-fA-F:]+)\s+(\S+)\s+\(p2p\)`)
	vppABFPolicyLine       = regexp.MustCompile(`\bpolicy:(\d+)\s+acl:(\d+)\b`)
	vppABFPPPoEPathLine    = regexp.MustCompile(`^\s*([0-9]+(?:\.[0-9]+){3})\s+(\S+)\s+\(p2p\)`)
)

type vppPPPoESessionInfo struct {
	Interface    string
	PoolIndex    uint32
	SwIfIndex    uint32
	EncapIfIndex uint32
	ClientIP     string
	SessionID    uint16
	ClientMAC    string
}

type vppPPPoEABFPolicy struct {
	PolicyID  int
	ACLID     int
	NextHop   string
	Interface string
}

func parseVPPPPPoEABFPolicies(output, interfaceName string) ([]vppPPPoEABFPolicy, error) {
	interfaceName = strings.TrimSpace(interfaceName)
	var policies []vppPPPoEABFPolicy
	current := vppPPPoEABFPolicy{}
	flush := func() {
		if current.PolicyID != 0 && current.Interface == interfaceName {
			policies = append(policies, current)
		}
		current = vppPPPoEABFPolicy{}
	}
	for _, line := range strings.Split(output, "\n") {
		if match := vppABFPolicyLine.FindStringSubmatch(line); len(match) == 3 {
			flush()
			policyID, err := strconv.Atoi(match[1])
			if err != nil {
				return nil, fmt.Errorf("invalid VPP ABF policy ID %q: %w", match[1], err)
			}
			aclID, err := strconv.Atoi(match[2])
			if err != nil {
				return nil, fmt.Errorf("invalid VPP ABF ACL ID %q: %w", match[2], err)
			}
			current.PolicyID = policyID
			current.ACLID = aclID
			continue
		}
		if match := vppABFPPPoEPathLine.FindStringSubmatch(line); len(match) == 3 {
			current.NextHop = match[1]
			current.Interface = match[2]
		}
	}
	flush()
	return policies, nil
}

// removePPPoEABFPolicies removes every policy that still references the live
// PPPoE interface while VPP can resolve its name. Deleting the interface first
// makes VPP's CLI unable to parse the old ABF path and permanently strands the
// policy until VPP is restarted.
func removePPPoEABFPolicies(ctx context.Context, config VPPConfig, interfaceName string) error {
	output, err := runVPP(ctx, config.Binary, "show", "abf", "policy")
	if err != nil {
		return err
	}
	policies, err := parseVPPPPPoEABFPolicies(output, interfaceName)
	if err != nil {
		return err
	}
	if len(policies) == 0 {
		return nil
	}
	ingressInterfaces := append([]string(nil), config.NATInsideInterfaces...)
	ingressInterfaces = append(ingressInterfaces, config.IPv6LANInterfaces...)
	for _, policy := range policies {
		for _, ingress := range ingressInterfaces {
			ingress = strings.TrimSpace(ingress)
			if ingress == "" {
				continue
			}
			attached, showErr := runVPP(ctx, config.Binary, "show", "abf", "attach", ingress)
			if showErr != nil {
				return showErr
			}
			if strings.Contains(attached, "policy:"+strconv.Itoa(policy.PolicyID)) {
				if _, err := runVPP(ctx, config.Binary, "abf", "attach", "ip4", "del", "policy", strconv.Itoa(policy.PolicyID), ingress); err != nil {
					return err
				}
			}
		}
		if _, err := runVPP(ctx, config.Binary, "abf", "policy", "del", "id", strconv.Itoa(policy.PolicyID), "acl", strconv.Itoa(policy.ACLID), "via", policy.NextHop, policy.Interface); err != nil {
			return err
		}
		readback, err := runVPP(ctx, config.Binary, "show", "abf", "policy", strconv.Itoa(policy.PolicyID))
		if err != nil {
			return err
		}
		if strings.Contains(readback, "policy:"+strconv.Itoa(policy.PolicyID)) {
			return fmt.Errorf("VPP ABF policy %d still references %s after deletion", policy.PolicyID, policy.Interface)
		}
	}
	return nil
}

// parseVPPPPPoESessionIDs returns the VPP session IDs using clientIP. VPP can
// retain a PPPoE session object after a control-plane reconnect; deleting only
// the last reported ID then leaves the old rewrite in the ABF/FIB adjacency.
func parseVPPPPPoESessions(output string) ([]vppPPPoESessionInfo, error) {
	var result []vppPPPoESessionInfo
	var current *vppPPPoESessionInfo
	for _, line := range strings.Split(output, "\n") {
		match := vppPPPoESessionLine.FindStringSubmatch(line)
		if len(match) == 6 {
			poolIndex, err := strconv.ParseUint(match[1], 10, 32)
			if err != nil {
				return nil, fmt.Errorf("invalid VPP PPPoE interface index %q: %w", match[1], err)
			}
			swIfIndex, err := strconv.ParseUint(match[2], 10, 32)
			if err != nil {
				return nil, fmt.Errorf("invalid VPP PPPoE sw-if-index %q: %w", match[2], err)
			}
			id, err := strconv.ParseUint(match[4], 10, 16)
			if err != nil {
				return nil, fmt.Errorf("invalid VPP PPPoE session ID %q: %w", match[4], err)
			}
			encapIfIndex := uint64(0)
			if match[5] != "" {
				encapIfIndex, err = strconv.ParseUint(match[5], 10, 32)
				if err != nil {
					return nil, fmt.Errorf("invalid VPP PPPoE encap-if-index %q: %w", match[5], err)
				}
			}
			current = &vppPPPoESessionInfo{
				Interface:    fmt.Sprintf("pppoe_session%d", poolIndex),
				PoolIndex:    uint32(poolIndex),
				SwIfIndex:    uint32(swIfIndex),
				EncapIfIndex: uint32(encapIfIndex),
				ClientIP:     strings.TrimSpace(match[3]),
				SessionID:    uint16(id),
			}
			continue
		}
		if current == nil {
			continue
		}
		if clientMAC := vppPPPoEClientMACLine.FindStringSubmatch(line); len(clientMAC) == 2 {
			current.ClientMAC = strings.ToLower(clientMAC[1])
			result = append(result, *current)
			current = nil
		}
	}
	if current != nil {
		result = append(result, *current)
	}
	return result, nil
}

func parseVPPInterfaceIndexes(output string) (map[uint32]string, map[string]uint32) {
	byIndex := map[uint32]string{}
	byName := map[string]uint32{}
	for _, line := range strings.Split(output, "\n") {
		match := vppInterfaceStateLine.FindStringSubmatch(line)
		if len(match) != 3 {
			continue
		}
		index, err := strconv.ParseUint(match[2], 10, 32)
		if err != nil {
			continue
		}
		name := strings.TrimSpace(match[1])
		byIndex[uint32(index)] = name
		byName[name] = uint32(index)
	}
	return byIndex, byName
}

func createdVPPPPoEInterface(ctx context.Context, config VPPConfig, session Session, createOutput string) (string, error) {
	candidate := strings.TrimSpace(createOutput)
	if fields := strings.Fields(candidate); len(fields) > 0 {
		candidate = fields[len(fields)-1]
	}
	if !config.ExplicitWAN || strings.TrimSpace(config.WANInterface) == "" {
		if candidate == "" || strings.ContainsAny(candidate, " \t\r\n;$\"") {
			return "", fmt.Errorf("VPP returned invalid PPPoE interface %q", candidate)
		}
		return candidate, nil
	}

	sessionOutput, err := runVPP(ctx, config.Binary, "show", "pppoe", "session")
	if err != nil {
		return "", err
	}
	entries, err := parseVPPPPPoESessions(sessionOutput)
	if err != nil {
		return "", err
	}
	interfacesOutput, err := runVPP(ctx, config.Binary, "show", "interface")
	if err != nil {
		return "", err
	}
	interfacesByIndex, indexesByName := parseVPPInterfaceIndexes(interfacesOutput)
	wantedEncapIndex, found := indexesByName[strings.TrimSpace(config.WANInterface)]
	if !found {
		return "", fmt.Errorf("VPP WAN interface %q is unavailable", config.WANInterface)
	}
	for _, entry := range entries {
		if entry.ClientIP != strings.TrimSpace(session.LocalAddress) || entry.SessionID != session.ID || entry.EncapIfIndex != wantedEncapIndex {
			continue
		}
		interfaceName := strings.TrimSpace(interfacesByIndex[entry.SwIfIndex])
		if interfaceName == "" || strings.ContainsAny(interfaceName, " \t\r\n;$\"") {
			return "", fmt.Errorf("VPP returned invalid PPPoE interface %q for sw-if-index %d", interfaceName, entry.SwIfIndex)
		}
		return interfaceName, nil
	}
	return "", fmt.Errorf("VPP did not expose PPPoE session %s/%d on WAN %s", session.LocalAddress, session.ID, config.WANInterface)
}

func removeVPPPPPoESessions(ctx context.Context, config VPPConfig, session Session) error {
	output, err := runVPP(ctx, config.Binary, "show", "pppoe", "session")
	if err != nil {
		return err
	}
	ac := net.HardwareAddr(session.ACMAC[:]).String()
	entries, err := parseVPPPPPoESessions(output)
	if err != nil {
		return err
	}
	interfaceOutput, err := runVPP(ctx, config.Binary, "show", "interface")
	if err != nil {
		return err
	}
	interfacesByIndex, indexesByName := parseVPPInterfaceIndexes(interfaceOutput)
	wantedEncapIndex, hasWantedEncap := indexesByName[strings.TrimSpace(config.WANInterface)]
	if config.ExplicitWAN && strings.TrimSpace(config.WANInterface) != "" && !hasWantedEncap {
		return fmt.Errorf("VPP WAN interface %q is unavailable", config.WANInterface)
	}
	for _, entry := range entries {
		if currentName := interfacesByIndex[entry.SwIfIndex]; currentName != "" {
			entry.Interface = currentName
		}
		ownedByWAN := entry.ClientIP == strings.TrimSpace(session.LocalAddress) || strings.EqualFold(entry.ClientMAC, ac)
		if config.ExplicitWAN && hasWantedEncap {
			ownedByWAN = entry.EncapIfIndex == wantedEncapIndex
		}
		if !ownedByWAN {
			continue
		}
		removePPPoENATRoles(ctx, config, entry.Interface)
		// The PPPoE interface index is reused by VPP. Remove the old peer and
		// default paths before deleting its session, otherwise their cached L2
		// rewrite can retain the preceding PPPoE session ID after reconnect.
		removePPPoERoutes(ctx, config, session.RemoteAddress, entry.Interface)
		if err := removePPPoEABFPolicies(ctx, config, entry.Interface); err != nil {
			return fmt.Errorf("remove ABF policies for %s: %w", entry.Interface, err)
		}
		if err := deleteVPPPPPoESession(ctx, config, entry, ac); err != nil {
			return err
		}
	}
	return nil
}

// removePPPoENATRoles clears both NAT plugin families and the legacy output
// feature form. PPPoE interface indices are reused, so an old placeholder can
// otherwise remain an outside interface after the negotiated session changes.
func removePPPoENATRoles(ctx context.Context, config VPPConfig, interfaceName string) {
	interfaceName = strings.TrimSpace(interfaceName)
	if interfaceName == "" {
		return
	}
	for _, inside := range config.NATInsideInterfaces {
		inside = strings.TrimSpace(inside)
		if inside == "" {
			continue
		}
		_, _ = runVPP(ctx, config.Binary, "set", "interface", "nat44", "in", inside, "out", interfaceName, "del")
		_, _ = runVPP(ctx, config.Binary, "set", "interface", "nat44", "ei", "in", inside, "out", interfaceName, "del")
		_, _ = runVPP(ctx, config.Binary, "set", "interface", "nat44", "in", inside, "out", interfaceName, "output-feature", "del")
		_, _ = runVPP(ctx, config.Binary, "set", "interface", "nat44", "ei", "in", inside, "out", interfaceName, "output-feature", "del")
	}
	_, _ = runVPP(ctx, config.Binary, "set", "interface", "nat44", "in", interfaceName, "output-feature", "del")
	_, _ = runVPP(ctx, config.Binary, "set", "interface", "nat44", "ei", "in", interfaceName, "output-feature", "del")
	_, _ = runVPP(ctx, config.Binary, "nat44", "add", "interface", "address", interfaceName, "del")
	_, _ = runVPP(ctx, config.Binary, "nat44", "ei", "add", "interface", "address", interfaceName, "del")
}

func removePPPoERoutes(ctx context.Context, config VPPConfig, remoteAddress, interfaceName string) {
	interfaceName = strings.TrimSpace(interfaceName)
	if interfaceName == "" {
		return
	}
	tables := []int{0}
	if output, err := runVPP(ctx, config.Binary, "show", "ip", "fib"); err == nil {
		if detected := vppPPPoEFIBTables(output, interfaceName); len(detected) > 0 {
			tables = detected
		}
	}
	for _, tableID := range tables {
		deleteDefault := vppTableRouteCommand("del", tableID, "0.0.0.0/0", remoteAddress, interfaceName)
		_, _ = runVPP(ctx, config.Binary, deleteDefault...)
		if peerRoute := vppTablePeerRouteCommand("del", tableID, remoteAddress, interfaceName); len(peerRoute) > 0 {
			_, _ = runVPP(ctx, config.Binary, peerRoute...)
		}
	}
	if output, err := runVPP(ctx, config.Binary, "show", "ip6", "fib"); err == nil {
		for _, path := range vppPPPoEIPv6DefaultPaths(output, interfaceName) {
			command := []string{"ip", "route", "del"}
			if path.TableID != 0 {
				command = append(command, "table", strconv.Itoa(path.TableID))
			}
			command = append(command, "::/0", "via", path.Remote, path.Interface)
			_, _ = runVPP(ctx, config.Binary, command...)
		}
	}
}

type vppPPPoEIPv6DefaultPath struct {
	TableID   int
	Remote    string
	Interface string
}

func vppPPPoEIPv6DefaultPaths(output, interfaceName string) []vppPPPoEIPv6DefaultPath {
	var result []vppPPPoEIPv6DefaultPath
	tableID := 0
	inDefault := false
	seen := map[string]struct{}{}
	for _, line := range strings.Split(output, "\n") {
		if match := regexp.MustCompile(`^ipv6-VRF:(\d+),`).FindStringSubmatch(line); len(match) == 2 {
			tableID, _ = strconv.Atoi(match[1])
			inDefault = false
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "::/0" {
			inDefault = true
			continue
		}
		if strings.HasSuffix(trimmed, "/") || strings.Contains(trimmed, "-") {
			continue
		}
		if !inDefault {
			continue
		}
		match := vppIPv6DefaultPathLine.FindStringSubmatch(line)
		if len(match) != 3 || match[2] != interfaceName {
			continue
		}
		key := fmt.Sprintf("%d/%s/%s", tableID, match[1], match[2])
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, vppPPPoEIPv6DefaultPath{TableID: tableID, Remote: match[1], Interface: match[2]})
	}
	return result
}

// vppPPPoEFIBTables returns only the tables whose current routes reference the
// session interface. These routes must be removed before VPP reuses the
// interface index for a new PPPoE session, otherwise the cached L2 rewrite can
// retain the old PPPoE session ID.
func vppPPPoEFIBTables(output, interfaceName string) []int {
	tables := map[int]struct{}{}
	currentTable := -1
	for _, line := range strings.Split(output, "\n") {
		if match := vppIPv4FIBTableLine.FindStringSubmatch(line); len(match) == 2 {
			currentTable, _ = strconv.Atoi(match[1])
			continue
		}
		if currentTable >= 0 && strings.Contains(line, interfaceName) {
			tables[currentTable] = struct{}{}
		}
	}
	result := make([]int, 0, len(tables))
	for tableID := range tables {
		result = append(result, tableID)
	}
	sort.Ints(result)
	return result
}

func vppTableRouteCommand(action string, tableID int, destination, remoteAddress, interfaceName string) []string {
	command := []string{"ip", "route", action}
	if tableID != 0 {
		command = append(command, "table", strconv.Itoa(tableID))
	}
	command = append(command, destination)
	return append(command, vppRoutePath(remoteAddress, interfaceName)...)
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

// VPP can reuse a PPPoE interface while its FIB midchain still contains the
// previous PPPoE session ID or points at a previous WAN. The human-readable
// session table does not expose that stale rewrite, so allocation is accepted
// only after inspecting an isolated FIB route.
const pppoeRewriteProbeDestination = "198.18.255.254/32"

func pppoeRewriteProbeCommands(tableID int, action, remoteAddress, interfaceName string) []string {
	command := []string{"ip", "route", action}
	if tableID != 0 {
		command = append(command, "table", strconv.Itoa(tableID))
	}
	command = append(command, pppoeRewriteProbeDestination)
	return append(command, vppRoutePath(remoteAddress, interfaceName)...)
}

func pppoeRewriteProbeShowCommand(tableID int) []string {
	if tableID == 0 {
		return []string{"show", "ip", "fib", pppoeRewriteProbeDestination}
	}
	return []string{"show", "ip", "fib", "table", strconv.Itoa(tableID), pppoeRewriteProbeDestination}
}

func pppoeRewriteUsesWAN(output, wanInterface string) bool {
	wanInterface = strings.ToLower(strings.TrimSpace(wanInterface))
	if wanInterface == "" {
		return true
	}
	// A VPP FIB rewrite exposes the physical egress as
	// "<interface>-tx-dpo" in its stacked-on section. Do not accept a
	// negotiated session merely because its PPPoE session ID is correct: a
	// reused pool slot can retain a rewrite for another WAN.
	return strings.Contains(strings.ToLower(output), wanInterface+"-tx-dpo")
}

func pppoeRewriteMatchesSession(ctx context.Context, config VPPConfig, session Session, interfaceName string) (bool, error) {
	if _, err := runVPP(ctx, config.Binary, pppoeRewriteProbeCommands(config.TableID, "add", session.RemoteAddress, interfaceName)...); err != nil {
		return false, err
	}
	defer func() {
		_, _ = runVPP(ctx, config.Binary, pppoeRewriteProbeCommands(config.TableID, "del", session.RemoteAddress, interfaceName)...)
	}()
	output, err := runVPP(ctx, config.Binary, pppoeRewriteProbeShowCommand(config.TableID)...)
	if err != nil {
		return false, err
	}
	want := fmt.Sprintf("88641100%04x", session.ID)
	if !strings.Contains(strings.ToLower(output), want) {
		return false, nil
	}
	if config.ExplicitWAN && !pppoeRewriteUsesWAN(output, config.WANInterface) {
		return false, nil
	}
	return true, nil
}

func deleteVPPPPPoESession(ctx context.Context, config VPPConfig, entry vppPPPoESessionInfo, clientMAC string) error {
	if strings.TrimSpace(entry.ClientMAC) != "" {
		clientMAC = entry.ClientMAC
	}
	_, err := runVPP(ctx, config.Binary, vppPPPoESessionCommand(config, entry.ClientIP, entry.SessionID, clientMAC, true)...)
	return err
}

func vppPPPoESessionCommand(config VPPConfig, clientIP string, sessionID uint16, clientMAC string, deleteSession bool) []string {
	command := []string{"create", "pppoe", "session", "client-ip", clientIP,
		"session-id", strconv.Itoa(int(sessionID)), "client-mac", clientMAC}
	if wan := strings.TrimSpace(config.WANInterface); config.ExplicitWAN && wan != "" {
		command = append(command, "encap-interface", wan)
	}
	command = append(command, "decap-vrf-id", strconv.Itoa(config.TableID))
	if deleteSession {
		command = append(command, "del")
	}
	return command
}

// createFreshVPPPPPoESession works around the VPP PPPoE midchain reuse bug.
// A contaminated pool slot is kept occupied by an unrouted placeholder while
// the next slot is tested. Once the negotiated rewrite is verified, the
// placeholders are removed before returning; keeping them alive creates
// duplicate interface names and makes runtime snapshots ambiguous.
func createFreshVPPPPPoESession(ctx context.Context, config VPPConfig, session Session) (string, []vppPPPoESessionInfo, error) {
	clientMAC := net.HardwareAddr(session.ACMAC[:]).String()
	placeholders := make([]vppPPPoESessionInfo, 0, 8)
	reservedIDs := map[uint16]struct{}{}
	cleanupPlaceholders := true
	defer func() {
		if !cleanupPlaceholders {
			return
		}
		for _, placeholder := range placeholders {
			_ = deleteVPPPPPoESession(ctx, config, placeholder, clientMAC)
		}
	}()
	reservePlaceholder := func(clientIP string) (vppPPPoESessionInfo, error) {
		for candidate := uint32(0xffff); candidate > 0; candidate-- {
			placeholderID := uint16(candidate)
			if placeholderID == session.ID {
				continue
			}
			if _, used := reservedIDs[placeholderID]; used {
				continue
			}
			placeholder := vppPPPoESessionInfo{ClientIP: clientIP, SessionID: placeholderID}
			if _, err := runVPP(ctx, config.Binary, vppPPPoESessionCommand(config, placeholder.ClientIP, placeholder.SessionID, clientMAC, false)...); err != nil {
				return vppPPPoESessionInfo{}, err
			}
			reservedIDs[placeholderID] = struct{}{}
			return placeholder, nil
		}
		return vppPPPoESessionInfo{}, errors.New("no PPPoE placeholder session ID available")
	}

	// VPP can preserve a deleted session's output rewrite on the first reused
	// PPPoE interface. Reserve that first slot before creating the negotiated
	// session, so each reconnect receives a distinct, verified interface.
	initialPlaceholder, err := reservePlaceholder("198.18.254.254")
	if err != nil {
		return "", nil, fmt.Errorf("reserve initial PPPoE session slot: %w", err)
	}
	placeholders = append(placeholders, initialPlaceholder)

	for attempt := 0; attempt < 64; attempt++ {
		output, err := runVPP(ctx, config.Binary, vppPPPoESessionCommand(config, session.LocalAddress, session.ID, clientMAC, false)...)
		if err != nil {
			return "", nil, err
		}
		// VPP can print a stale pool name after a reconnect. Resolve the current
		// session by its negotiated identity and encap WAN instead of trusting
		// the create command's textual output.
		interfaceName, err := createdVPPPPoEInterface(ctx, config, session, output)
		if err != nil {
			return "", nil, fmt.Errorf("resolve created PPPoE session: %w", err)
		}
		matches, err := pppoeRewriteMatchesSession(ctx, config, session, interfaceName)
		if err != nil {
			// A deleted PPPoE pool slot can retain an old VPP interface name.
			// The route probe then fails to resolve that candidate even though
			// the session itself was created. Treat it like a stale slot and
			// continue with the next pair instead of taking the WAN offline.
			entry := vppPPPoESessionInfo{ClientIP: session.LocalAddress, SessionID: session.ID}
			if deleteErr := deleteVPPPPPoESession(ctx, config, entry, clientMAC); deleteErr != nil {
				return "", nil, fmt.Errorf("remove stale PPPoE candidate %s after probe failure: %w (probe: %v)", interfaceName, deleteErr, err)
			}
			placeholder, reserveErr := reservePlaceholder(fmt.Sprintf("198.18.254.%d", attempt+1))
			if reserveErr != nil {
				return "", nil, fmt.Errorf("reserve PPPoE slot after probe failure on %s: %w (probe: %v)", interfaceName, reserveErr, err)
			}
			placeholders = append(placeholders, placeholder)
			continue
		}
		if matches {
			for _, placeholder := range placeholders {
				if err := deleteVPPPPPoESession(ctx, config, placeholder, clientMAC); err != nil {
					return "", nil, fmt.Errorf("release PPPoE placeholder %s/%d: %w", placeholder.ClientIP, placeholder.SessionID, err)
				}
			}
			placeholders = nil
			cleanupPlaceholders = false
			return interfaceName, nil, nil
		}

		entry := vppPPPoESessionInfo{ClientIP: session.LocalAddress, SessionID: session.ID}
		if err := deleteVPPPPPoESession(ctx, config, entry, clientMAC); err != nil {
			return "", nil, fmt.Errorf("remove stale PPPoE candidate %s: %w", interfaceName, err)
		}
		placeholder, err := reservePlaceholder(fmt.Sprintf("198.18.254.%d", attempt+1))
		if err != nil {
			return "", nil, fmt.Errorf("reserve stale PPPoE slot after %s: %w", interfaceName, err)
		}
		placeholders = append(placeholders, placeholder)
	}
	return "", nil, errors.New("VPP could not allocate a PPPoE session with a fresh output rewrite")
}

func vppPeerRouteCommand(remoteAddress, interfaceName string) []string {
	return vppTablePeerRouteCommand("add", 0, remoteAddress, interfaceName)
}

func vppTablePeerRouteCommand(action string, tableID int, remoteAddress, interfaceName string) []string {
	address, err := netip.ParseAddr(strings.TrimSpace(remoteAddress))
	if err != nil || address.IsUnspecified() {
		return nil
	}
	bits := 32
	if address.Is6() {
		bits = 128
	}
	command := []string{"ip", "route", action}
	if tableID != 0 {
		command = append(command, "table", strconv.Itoa(tableID))
	}
	command = append(command, address.String()+"/"+strconv.Itoa(bits))
	return append(command, vppRoutePath(address.String(), interfaceName)...)
}

func ProgramVPP(ctx context.Context, config VPPConfig, session Session) (VPPSession, error) {
	if strings.TrimSpace(config.Binary) == "" {
		config.Binary = "vppctl"
	}
	if config.MTU == 0 {
		config.MTU = 1492
	}
	config.ExplicitWAN = pppoeExplicitWANAvailable(ctx, config)
	if err := removeVPPPPPoESessions(ctx, config, session); err != nil {
		return VPPSession{}, fmt.Errorf("remove stale PPPoE VPP sessions: %w", err)
	}
	interfaceName, staleSlots, err := createFreshVPPPPPoESession(ctx, config, session)
	if err != nil {
		return VPPSession{}, err
	}
	programmed := VPPSession{Interface: interfaceName, Session: session, config: config, staleSlots: staleSlots}
	cleanup := true
	defer func() {
		if cleanup {
			_ = programmed.Remove(context.Background())
		}
	}()
	// VPP-native PPPoE interfaces carry a hidden per-session rewrite. Keep the
	// allocator-provided name instead of applying the legacy stable alias: on a
	// pooled reconnect an alias can refer to a session from a different WAN.
	// The control plane reads this verified name from the runtime status file.
	// A graceful predecessor can delete its PPPoE session before this process
	// starts, leaving service-table routes that still hold the reused interface
	// adjacency. Clear every reference again after VPP returns the new interface
	// name, before any route for the new session is installed.
	removePPPoERoutes(ctx, config, session.RemoteAddress, interfaceName)
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
	// VPP's PPPoE session interface does not implement the generic
	// `set interface mtu` CLI. The negotiated MRU is enforced by the PPPoE
	// client; sending that unsupported command caused reconnects to enter
	// cleanup before NAT and routes were installed.
	commands := [][]string{{"set", "interface", "ip", "address", interfaceName, session.LocalAddress + "/32"}}
	if peerRoute := vppPeerRouteCommand(session.RemoteAddress, interfaceName); len(peerRoute) > 0 {
		commands = append(commands, peerRoute)
	}
	if config.InstallDefaultRoute {
		defaultRoute := append([]string{"ip", "route", "add", "0.0.0.0/0"}, vppRoutePath(session.RemoteAddress, interfaceName)...)
		commands = append(commands, defaultRoute)
	}
	if config.EnableNAT {
		fullCone := strings.EqualFold(strings.TrimSpace(config.NATBehavior), "full_cone")
		// VPP retains interface role bits across a PPPoE session deletion. Clear
		// every form used by earlier releases before programming the WAN output
		// feature for the new session.
		for _, inside := range config.NATInsideInterfaces {
			inside = strings.TrimSpace(inside)
			if inside == "" {
				continue
			}
			_, _ = runVPP(ctx, config.Binary, "set", "interface", "nat44", "in", inside, "out", interfaceName, "del")
			_, _ = runVPP(ctx, config.Binary, "set", "interface", "nat44", "in", inside, "out", interfaceName, "output-feature", "del")
			_, _ = runVPP(ctx, config.Binary, "set", "interface", "nat44", "ei", "in", inside, "out", interfaceName, "del")
			_, _ = runVPP(ctx, config.Binary, "set", "interface", "nat44", "ei", "in", inside, "out", interfaceName, "output-feature", "del")
		}
		_, _ = runVPP(ctx, config.Binary, "set", "interface", "nat44", "in", interfaceName, "output-feature", "del")
		_, _ = runVPP(ctx, config.Binary, "set", "interface", "nat44", "ei", "in", interfaceName, "output-feature", "del")
		if fullCone {
			// NAT44-EI is gateway-wide. Disabling it here would erase every other
			// connected WAN whenever one PPPoE session reconnects. Remove only the
			// incompatible ED family, then idempotently enable EI and add this WAN.
			commands = append(commands, natPluginActivationCommands(true)...)
			// Use the regular inside/outside feature pair. PPPoE is a tunnel
			// interface whose output-feature runs after tunnel-output; enabling NAT
			// output-feature on it sends the packet back to local0-output instead of
			// allowing the PPPoE midchain to encapsulate it. The explicit pair keeps
			// NAT before the PPPoE tunnel output and still handles return traffic.
			for _, inside := range config.NATInsideInterfaces {
				inside = strings.TrimSpace(inside)
				if inside == "" {
					continue
				}
				commands = append(commands, []string{"set", "interface", "nat44", "ei", "in", inside, "out", interfaceName})
			}
			// A reconnect can retain the previous negotiated address in the
			// NAT interface-address pool even after the old PPPoE session has
			// been removed. Clear it before adding the current session address.
			_, _ = runVPP(ctx, config.Binary, "nat44", "ei", "add", "interface", "address", interfaceName, "del")
			commands = append(commands, []string{"nat44", "ei", "add", "interface", "address", interfaceName})
		} else {
			commands = append(commands, natPluginActivationCommands(false)...)
			// Route selection must happen before NAT on a multi-WAN gateway. The
			// output feature gives NAT44-ED the selected PPPoE sw_if_index so it
			// can allocate the address that belongs to that exact WAN.
			commands = append(commands, []string{"set", "interface", "nat44", "in", interfaceName, "output-feature"})
			_, _ = runVPP(ctx, config.Binary, "nat44", "add", "interface", "address", interfaceName, "del")
			commands = append(commands, []string{"nat44", "add", "interface", "address", interfaceName})
		}
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
		if strings.TrimSpace(session.DelegatedPrefix) != "" {
			if _, err := netip.ParsePrefix(session.DelegatedPrefix); err != nil {
				return VPPSession{}, fmt.Errorf("invalid delegated prefix %q: %w", session.DelegatedPrefix, err)
			}
			if err := configureDelegatedPrefix(ctx, config, session.DelegatedPrefix); err != nil {
				return VPPSession{}, err
			}
		}
	}
	cleanup = false
	return programmed, nil
}

func natPluginActivationCommands(fullCone bool) [][]string {
	if fullCone {
		// NAT44-EI is gateway-wide. PPPoE session lifecycle must never reset
		// the global plugin because another WAN may already be connected.
		return [][]string{{"nat44", "ei", "plugin", "enable"}}
	}
	return [][]string{{"nat44", "plugin", "enable"}}
}

func pppoeExplicitWANAvailable(ctx context.Context, config VPPConfig) bool {
	if strings.TrimSpace(config.WANInterface) == "" {
		return false
	}
	output, err := runVPP(ctx, config.Binary, "help", "create", "pppoe", "session")
	return err == nil && strings.Contains(output, "encap-interface")
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
		// Adding the interface address makes VPP synthesize an RA prefix with
		// no-autoconfig set. Updating only its lifetime retains that flag, so
		// ordinary LAN clients learn a route but never create a SLAAC address.
		// Remove the synthesized entry before creating the product-owned one.
		_, _ = runVPP(ctx, config.Binary, "ip6", "nd", lan, "no", "prefix", prefix)
		// Make the RA lifetime explicit. Some VPP builds interpret the
		// shorthand `default` as zero lifetime, which makes clients discard
		// the advertised SLAAC address immediately.
		if _, err := runVPP(ctx, config.Binary, "ip6", "nd", lan, "prefix", prefix, "infinite"); err != nil {
			return err
		}
		// Delegated-prefix LANs use SLAAC. Clear flags left by a previous
		// DHCPv6-managed configuration before sending the replacement RAs.
		_, _ = runVPP(ctx, config.Binary, "ip6", "nd", lan, "no", "ra-managed-config-flag")
		_, _ = runVPP(ctx, config.Binary, "ip6", "nd", lan, "no", "ra-other-config-flag")
		if _, err := runVPP(ctx, config.Binary, "ip6", "nd", lan, "ra-initial", "3", "1"); err != nil {
			return err
		}
	}
	return nil
}

// ReconcileDelegatedPrefix restores the dynamic LAN state after another VPP
// transaction has recreated interfaces without restarting the PPPoE client.
func ReconcileDelegatedPrefix(ctx context.Context, config VPPConfig, delegated string) error {
	if strings.TrimSpace(config.Binary) == "" {
		config.Binary = "vppctl"
	}
	return configureDelegatedPrefix(ctx, config, delegated)
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
		fullCone := strings.EqualFold(strings.TrimSpace(session.config.NATBehavior), "full_cone")
		for _, inside := range session.config.NATInsideInterfaces {
			inside = strings.TrimSpace(inside)
			if inside != "" {
				if fullCone {
					_, _ = runVPP(ctx, binary, "set", "interface", "nat44", "ei", "in", inside, "out", session.Interface, "del")
				} else {
					_, _ = runVPP(ctx, binary, "set", "interface", "nat44", "in", inside, "out", session.Interface, "del")
				}
			}
		}
		_, _ = runVPP(ctx, binary, "set", "interface", "nat44", "in", session.Interface, "output-feature", "del")
		_, _ = runVPP(ctx, binary, "set", "interface", "nat44", "ei", "in", session.Interface, "output-feature", "del")
		if fullCone {
			_, _ = runVPP(ctx, binary, "nat44", "ei", "add", "interface", "address", session.Interface, "del")
		} else {
			_, _ = runVPP(ctx, binary, "nat44", "add", "interface", "address", session.Interface, "del")
		}
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
	return removeVPPPPPoESessions(ctx, session.config, session.Session)
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
