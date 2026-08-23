package vpp

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// DNSServiceNetwork is a VPP-owned L3 handoff for a DNS upstream pinned to a
// WAN or WAN group. SmartDNS selects the handoff with SocketMark; VPP performs
// the actual WAN lookup and NAT after receiving the resolver packet on
// VPPInterface.
type DNSServiceNetwork struct {
	UpstreamID      string   `json:"upstream_id"`
	WANEgressID     string   `json:"wan_egress_id"`
	MTU             int      `json:"mtu"`
	VPPInterface    string   `json:"vpp_interface"`
	HostInterface   string   `json:"host_interface"`
	VPPAddress      string   `json:"vpp_address"`
	HostAddress     string   `json:"host_address"`
	CIDR            string   `json:"cidr"`
	TableID         int      `json:"table_id"`
	TapID           int      `json:"tap_id"`
	SocketMark      uint32   `json:"socket_mark"`
	UnderlayRoute   string   `json:"underlay_route"`
	ResolverServers []string `json:"resolver_servers"`
}

const (
	defaultDNSServiceMTU = 1500
	minDNSServiceMTU     = 576
	maxDNSServiceMTU     = 9000

	// VPP's tap device implementation accepts ids through TAP_MAX_INSTANCE
	// (8192). Proxy service networks occupy 4100-7999, so DNS service
	// networks use the remaining high, non-overlapping range.
	dnsServiceTapIDBase = 8000
	dnsServiceTapIDSpan = 193

	// Keep DNS marks in a dedicated range. They are applied by SmartDNS and
	// select the per-upstream Linux route table without relying on a bound
	// interface source address.
	dnsServiceSocketMarkBase uint32 = 0x04000000
)

// DNSServiceNetworkForUpstreamID creates a stable service handoff outside the
// proxy service-network range. 198.19.0.0/16 is the second half of the
// benchmark interconnect /15 and is reserved here solely for local VPP peers.
func DNSServiceNetworkForUpstreamID(upstreamID, wanEgressID string, servers []string) (DNSServiceNetwork, error) {
	upstreamID = strings.TrimSpace(upstreamID)
	wanEgressID = strings.TrimSpace(wanEgressID)
	if upstreamID == "" || wanEgressID == "" {
		return DNSServiceNetwork{}, fmt.Errorf("DNS service network requires upstream and WAN egress ids")
	}
	cleanServers := make([]string, 0, len(servers))
	seenServers := map[string]struct{}{}
	for _, server := range servers {
		server = strings.TrimSpace(server)
		address, err := netip.ParseAddr(server)
		if err != nil || !address.Is4() {
			return DNSServiceNetwork{}, fmt.Errorf("DNS upstream %q contains unsupported resolver %q: a pinned resolver must be one IPv4 address", upstreamID, server)
		}
		server = address.String()
		if _, exists := seenServers[server]; exists {
			continue
		}
		seenServers[server] = struct{}{}
		cleanServers = append(cleanServers, server)
	}
	if len(cleanServers) == 0 {
		return DNSServiceNetwork{}, fmt.Errorf("DNS upstream %q requires at least one resolver", upstreamID)
	}
	slices.Sort(cleanServers)

	digest := sha256.Sum256([]byte(upstreamID + "\x00" + wanEgressID))
	short := fmt.Sprintf("%x", digest[:3])
	value := binary.BigEndian.Uint32(digest[:4])
	base := uint32(198)<<24 | uint32(19)<<16
	base += (value % 16384) * 4
	baseAddress := netip.AddrFrom4([4]byte{byte(base >> 24), byte(base >> 16), byte(base >> 8), byte(base)})
	vppAddress := baseAddress.Next()
	hostAddress := vppAddress.Next()

	tableID := 50000 + int((value>>8)%10000)
	return DNSServiceNetwork{
		UpstreamID:      upstreamID,
		WANEgressID:     wanEgressID,
		MTU:             defaultDNSServiceMTU,
		VPPInterface:    "lydns" + short,
		HostInterface:   "lydnsh" + short,
		VPPAddress:      vppAddress.String(),
		HostAddress:     hostAddress.String(),
		CIDR:            netip.PrefixFrom(vppAddress, 30).String(),
		TableID:         tableID,
		TapID:           dnsServiceTapIDBase + int(value%dnsServiceTapIDSpan),
		SocketMark:      dnsServiceSocketMarkBase | uint32(tableID),
		ResolverServers: cleanServers,
	}, nil
}

// AllocateDNSServiceTapIDs resolves hash collisions inside VPP's bounded TAP
// instance range. Allocation order is derived from stable upstream identity so
// the result does not depend on storage or API iteration order.
func AllocateDNSServiceTapIDs(networks []DNSServiceNetwork) error {
	indices := make([]int, len(networks))
	for index := range networks {
		indices[index] = index
	}
	slices.SortFunc(indices, func(left, right int) int {
		leftKey := fmt.Sprintf("%04d\x00%s\x00%s\x00%s", networks[left].TapID, networks[left].VPPInterface, networks[left].UpstreamID, networks[left].WANEgressID)
		rightKey := fmt.Sprintf("%04d\x00%s\x00%s\x00%s", networks[right].TapID, networks[right].VPPInterface, networks[right].UpstreamID, networks[right].WANEgressID)
		return strings.Compare(leftKey, rightKey)
	})

	used := make(map[int]struct{}, len(networks))
	for _, index := range indices {
		start := networks[index].TapID
		if start < dnsServiceTapIDBase || start >= dnsServiceTapIDBase+dnsServiceTapIDSpan {
			return fmt.Errorf("DNS upstream %q has TAP id %d outside the DNS service range", networks[index].UpstreamID, start)
		}
		for probe := 0; probe < dnsServiceTapIDSpan; probe++ {
			candidate := dnsServiceTapIDBase + (start-dnsServiceTapIDBase+probe)%dnsServiceTapIDSpan
			if _, exists := used[candidate]; exists {
				continue
			}
			networks[index].TapID = candidate
			used[candidate] = struct{}{}
			start = 0
			break
		}
		if start != 0 {
			return fmt.Errorf("DNS service TAP range is exhausted")
		}
	}
	return nil
}

// BindDNSServiceNetwork adds the resolved VPP route after WAN selection has
// compiled. It intentionally accepts only one VPP underlay expression.
func BindDNSServiceNetwork(network *DNSServiceNetwork, underlayRoute string, mtu ...int) error {
	if network == nil {
		return fmt.Errorf("DNS service network is required")
	}
	if len(mtu) > 1 {
		return fmt.Errorf("DNS service network accepts one effective underlay MTU")
	}
	if network.MTU == 0 {
		network.MTU = defaultDNSServiceMTU
	}
	if len(mtu) == 1 {
		network.MTU = mtu[0]
	}
	if err := validateDNSServiceNetwork(*network); err != nil {
		return err
	}
	underlayRoute = strings.TrimSpace(underlayRoute)
	if underlayRoute == "" || !vppCommandTokensSafe(underlayRoute) {
		return fmt.Errorf("DNS upstream %q has an unsafe VPP underlay route", network.UpstreamID)
	}
	network.UnderlayRoute = underlayRoute
	return nil
}

func validateDNSServiceNetwork(network DNSServiceNetwork) error {
	for label, value := range map[string]string{
		"upstream id":    network.UpstreamID,
		"WAN egress id":  network.WANEgressID,
		"VPP interface":  network.VPPInterface,
		"host interface": network.HostInterface,
	} {
		if !vppCommandTokenSafe(value) {
			return fmt.Errorf("DNS service network %s %q is unsafe", label, value)
		}
	}
	for label, value := range map[string]string{"VPP address": network.VPPAddress, "host address": network.HostAddress} {
		address, err := netip.ParseAddr(strings.TrimSpace(value))
		if err != nil || !address.Is4() {
			return fmt.Errorf("DNS service network %s %q is invalid", label, value)
		}
	}
	prefix, err := netip.ParsePrefix(strings.TrimSpace(network.CIDR))
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() != 30 {
		return fmt.Errorf("DNS service network CIDR %q is invalid", network.CIDR)
	}
	if network.TableID <= 0 || network.TapID <= 0 || network.SocketMark == 0 {
		return fmt.Errorf("DNS service network %q has incomplete numeric identities", network.UpstreamID)
	}
	if network.MTU < minDNSServiceMTU || network.MTU > maxDNSServiceMTU {
		return fmt.Errorf("DNS service network %q has invalid MTU %d", network.UpstreamID, network.MTU)
	}
	if len(network.ResolverServers) == 0 {
		return fmt.Errorf("DNS service network %q has no resolver servers", network.UpstreamID)
	}
	for _, server := range network.ResolverServers {
		address, err := netip.ParseAddr(strings.TrimSpace(server))
		if err != nil || !address.Is4() {
			return fmt.Errorf("DNS service network %q has invalid resolver %q", network.UpstreamID, server)
		}
	}
	return nil
}

func vppCommandTokensSafe(value string) bool {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return false
	}
	for _, field := range fields {
		if !vppCommandTokenSafe(field) {
			return false
		}
	}
	return true
}

func vppCommandTokenSafe(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' || character == ':' {
			continue
		}
		return false
	}
	return true
}
