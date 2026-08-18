package proxy

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// nodeBootstrapServers are deliberately independent of /etc/resolv.conf and
// are not configurable. They are the fixed foreign Bootstrap DNS addresses
// used by Ly Route's built-in DoH profile. Xray node endpoint lookup must use
// exactly this control-plane resolver set so it cannot recurse through, or be
// changed by, the dataplane DNS policy carried by that Xray node.
// Xray node hostnames are control-plane inputs and must not depend on the
// dataplane DNS interception path that the node itself may be used to carry.
var nodeBootstrapServers = [...]string{
	"1.1.1.1",
	"1.0.0.1",
	"8.8.8.8",
	"8.8.4.4",
	"9.9.9.9",
}

// ResolveNodeAddress resolves a node hostname once while compiling runtime
// state. The resulting Xray endpoint is an IP literal, while SNI/Reality
// settings remain unchanged. Re-applying runtime state refreshes the address,
// so a DNS change or a failed node does not leave Xray using a stale hostname
// resolver path.
func ResolveNodeAddress(ctx context.Context, node Node) (Node, error) {
	if strings.TrimSpace(node.Address) == "" {
		return Node{}, fmt.Errorf("node %q has no address", node.ID)
	}
	if _, err := netip.ParseAddr(strings.TrimSpace(node.Address)); err == nil {
		return node, nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addresses, err := lookupWithFixedBootstrapResolvers(lookupCtx, strings.TrimSpace(node.Address))
	if err != nil {
		return Node{}, fmt.Errorf("resolve node %q with built-in DoH bootstrap DNS: %w", node.ID, err)
	}
	for _, address := range addresses {
		if address.Is4() && address.IsGlobalUnicast() {
			node.Address = address.String()
			return node, nil
		}
	}
	return Node{}, fmt.Errorf("resolve node %q returned no usable IPv4 address", node.ID)
}

// lookupWithFixedBootstrapResolvers deliberately creates one resolver per
// fixed server. A UDP socket can be created successfully even when the first
// server is unreachable; using one Resolver whose Dial callback always returns
// the first socket would then prevent the remaining fixed servers from being
// tried. Retrying at the resolver level gives the hard-coded bootstrap set its
// intended failover semantics without falling back to the host resolver.
func lookupWithFixedBootstrapResolvers(ctx context.Context, hostname string) ([]netip.Addr, error) {
	var lastErr error
	for _, server := range nodeBootstrapServers {
		server = strings.TrimSpace(server)
		if server == "" {
			continue
		}
		resolver := &net.Resolver{
			PreferGo: true,
			Dial: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
				address, err := netip.ParseAddr(server)
				if err != nil || !address.Is4() {
					return nil, fmt.Errorf("invalid node bootstrap resolver %q", server)
				}
				dialer := &net.Dialer{Timeout: 2 * time.Second}
				return dialer.DialContext(dialCtx, "udp", net.JoinHostPort(address.String(), strconv.Itoa(53)))
			},
		}
		addresses, err := resolver.LookupNetIP(ctx, "ip", hostname)
		if err == nil && len(addresses) > 0 {
			return addresses, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all fixed node bootstrap resolvers are unavailable")
	}
	return nil, lastErr
}
