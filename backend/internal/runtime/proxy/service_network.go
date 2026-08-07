package proxy

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
)

// ProxyServiceNetwork describes the two L3 hand-off points used by one proxy
// egress. VPP owns both TAP peers; Linux only terminates the transparent
// socket on the ingress peer and sends Xray's marked upstream sockets back to
// VPP through the egress peer.
type ProxyServiceNetwork struct {
	EgressID             string   `json:"egress_id"`
	MTU                  int      `json:"mtu"`
	IngressVPPInterface  string   `json:"ingress_vpp_interface"`
	IngressHostInterface string   `json:"ingress_host_interface"`
	IngressVPPAddress    string   `json:"ingress_vpp_address"`
	IngressHostAddress   string   `json:"ingress_host_address"`
	IngressCIDR          string   `json:"ingress_cidr"`
	EgressVPPInterface   string   `json:"egress_vpp_interface"`
	EgressHostInterface  string   `json:"egress_host_interface"`
	EgressVPPAddress     string   `json:"egress_vpp_address"`
	EgressHostAddress    string   `json:"egress_host_address"`
	EgressCIDR           string   `json:"egress_cidr"`
	IngressMark          uint32   `json:"ingress_mark"`
	OutboundMark         uint32   `json:"outbound_mark"`
	IngressTableID       int      `json:"ingress_table_id"`
	OutboundTableID      int      `json:"outbound_table_id"`
	IngressRulePriority  int      `json:"ingress_rule_priority"`
	OutboundRulePriority int      `json:"outbound_rule_priority"`
	IngressTapID         int      `json:"ingress_tap_id"`
	EgressTapID          int      `json:"egress_tap_id"`
	ListenerPort         int      `json:"listener_port"`
	UnderlayRoute        string   `json:"underlay_route,omitempty"`
	LANRoutes            []string `json:"lan_routes,omitempty"`
}

const (
	defaultProxyServiceMTU = 1500
	minProxyServiceMTU     = 576
	maxProxyServiceMTU     = 9000

	// VPP 25.x accepts explicit TAP ids only in its low interface-id range.
	// Keep the peers in disjoint deterministic ranges for repeatable applies.
	proxyTapIDSpan        = 1900
	proxyIngressTapIDBase = 4100
	proxyEgressTapIDBase  = 6100
)

// ServiceNetworkForEgressID returns stable, Linux-safe names and non-overlap
// values. Stability matters because a runtime apply may be repeated while
// existing VPP TAPs and policy rules are still live.
func ServiceNetworkForEgressID(id string) ProxyServiceNetwork {
	id = strings.TrimSpace(id)
	digest := sha256.Sum256([]byte(id))
	short := fmt.Sprintf("%x", digest[:3])
	value := binary.BigEndian.Uint32(digest[:4])

	// 198.18.0.0/15 is reserved for benchmark/interconnect networks. Each
	// egress gets one /30 from that space, with the two peers at .1 and .2.
	base := uint32(198)<<24 | uint32(18)<<16
	base += (value % 16384) * 4
	baseAddr := netip.AddrFrom4([4]byte{byte(base >> 24), byte(base >> 16), byte(base >> 8), byte(base)})
	ingressVPP := baseAddr.Next()
	ingressHost := ingressVPP.Next()
	egressBase := baseAddr.Next().Next().Next().Next()
	egressVPP := egressBase.Next()
	egressHost := egressVPP.Next()

	return ProxyServiceNetwork{
		EgressID:             id,
		MTU:                  defaultProxyServiceMTU,
		IngressVPPInterface:  "lypxin" + short,
		IngressHostInterface: "lypxhin" + short,
		IngressVPPAddress:    ingressVPP.String(),
		IngressHostAddress:   ingressHost.String(),
		IngressCIDR:          netip.PrefixFrom(ingressVPP, 30).String(),
		EgressVPPInterface:   "lypxout" + short,
		EgressHostInterface:  "lypxhout" + short,
		EgressVPPAddress:     egressVPP.String(),
		EgressHostAddress:    egressHost.String(),
		EgressCIDR:           netip.PrefixFrom(egressVPP, 30).String(),
		IngressMark:          0x100000 | (value & 0x0fff),
		OutboundMark:         0x200000 | ((value >> 12) & 0x0fff),
		IngressTableID:       20000 + int(value%10000),
		OutboundTableID:      30000 + int((value>>8)%10000),
		IngressRulePriority:  1200 + int(value%400),
		OutboundRulePriority: 1700 + int((value>>8)%400),
		IngressTapID:         proxyIngressTapIDBase + int(value%proxyTapIDSpan),
		EgressTapID:          proxyEgressTapIDBase + int((value>>8)%proxyTapIDSpan),
		ListenerPort:         20000 + int(value%10000),
	}
}

func (network ProxyServiceNetwork) valid() bool {
	return strings.TrimSpace(network.EgressID) != "" &&
		network.MTU >= minProxyServiceMTU && network.MTU <= maxProxyServiceMTU &&
		strings.TrimSpace(network.IngressVPPInterface) != "" &&
		strings.TrimSpace(network.IngressHostInterface) != "" &&
		strings.TrimSpace(network.EgressVPPInterface) != "" &&
		strings.TrimSpace(network.EgressHostInterface) != "" &&
		network.IngressTapID > 0 && network.EgressTapID > 0 &&
		network.IngressTableID > 0 && network.OutboundTableID > 0 &&
		network.IngressMark != 0 && network.OutboundMark != 0 &&
		network.ListenerPort > 0 && network.ListenerPort <= 65535
}

// BindServiceNetwork adds the selected VPP underlay and the LAN return routes
// after the gateway compiler has resolved WAN groups and address assignments.
func BindServiceNetwork(compiled *CompiledEgress, underlayRoute string, lanRoutes []string, mtu ...int) error {
	if compiled == nil {
		return fmt.Errorf("proxy service network requires a compiled egress")
	}
	network := compiled.ServiceNetwork
	if network.MTU == 0 {
		network.MTU = defaultProxyServiceMTU
	}
	if len(mtu) > 1 {
		return fmt.Errorf("proxy service network accepts one effective underlay MTU")
	}
	if len(mtu) == 1 {
		network.MTU = mtu[0]
	}
	if !network.valid() {
		return fmt.Errorf("proxy service network for %q is incomplete or has invalid MTU %d", compiled.ID, network.MTU)
	}
	underlayRoute = strings.TrimSpace(underlayRoute)
	if underlayRoute == "" {
		return fmt.Errorf("proxy egress %q has no resolved VPP underlay route", compiled.ID)
	}
	cleanRoutes := make([]string, 0, len(lanRoutes))
	seen := map[string]struct{}{}
	for _, route := range lanRoutes {
		route = strings.TrimSpace(route)
		if route == "" {
			continue
		}
		if _, exists := seen[route]; exists {
			continue
		}
		seen[route] = struct{}{}
		cleanRoutes = append(cleanRoutes, route)
	}
	network.UnderlayRoute = underlayRoute
	network.LANRoutes = cleanRoutes
	compiled.ServiceNetwork = network
	compiled.NftablesCapture.IngressInterface = network.IngressHostInterface
	compiled.NftablesCapture.Mark = fmt.Sprintf("0x%x", network.IngressMark)
	compiled.NftablesCapture.InboundMark = compiled.NftablesCapture.Mark
	compiled.LinuxPolicyRouting.Network = network
	compiled.LinuxPolicyRouting.LANRoutes = cleanRoutes
	compiled.LinuxPolicyRouting.UnderlayRoute = underlayRoute
	for index := range compiled.VPPSteering {
		compiled.VPPSteering[index].ServiceNetwork = network
		compiled.VPPSteering[index].UnderlayRoute = underlayRoute
	}
	ApplyOutboundSocketMark(&compiled.XrayRuntime.ConfigPayload, network.OutboundMark)
	return nil
}

// ApplyOutboundSocketMark preserves Xray stream settings while forcing every
// upstream socket through the selected Linux policy route. It is applied after
// subscription compilation because subscriptions replace the outbound list.
func ApplyOutboundSocketMark(config *XrayConfigPayload, mark uint32) {
	if config == nil || mark == 0 {
		return
	}
	for index := range config.Outbounds {
		stream := cloneMap(config.Outbounds[index].StreamSettings)
		sockopt := cloneMap(anyMap(stream["sockopt"]))
		sockopt["mark"] = mark
		stream["sockopt"] = sockopt
		config.Outbounds[index].StreamSettings = stream
	}
}

func cloneMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func anyMap(value any) map[string]any {
	if output, ok := value.(map[string]any); ok {
		return output
	}
	return nil
}
