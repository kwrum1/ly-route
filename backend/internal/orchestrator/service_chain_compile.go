package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

type serviceChainBinding struct {
	wanNextHop netip.Addr
	lanNextHop netip.Addr
}

// CompileServiceChainWithHealth removes unavailable nodes before generating both
// directions. This keeps bypass atomic and guarantees the reverse path remains
// the exact reverse of the healthy forward traversal.
func CompileServiceChainWithHealth(topology Topology, flow PolicyFlow, path CompiledPath, inputs []ServiceChainBindingInput, unavailable map[string]bool) (ServiceChain, error) {
	if len(unavailable) == 0 || len(path.Traversal) == 0 {
		return CompileServiceChain(topology, flow, path, inputs)
	}
	filtered := path
	filtered.Traversal = make([]string, 0, len(path.Traversal))
	bypassed := make([]string, 0)
	for _, group := range path.Traversal {
		if unavailable[strings.TrimSpace(group)] {
			bypassed = append(bypassed, group)
			continue
		}
		filtered.Traversal = append(filtered.Traversal, group)
	}
	sort.Strings(bypassed)
	chain, err := CompileServiceChain(topology, flow, filtered, inputs)
	if err != nil {
		return ServiceChain{}, err
	}
	chain.BypassedGroups = bypassed
	return chain, nil
}

func CompileServiceChain(topology Topology, flow PolicyFlow, path CompiledPath, inputs []ServiceChainBindingInput) (ServiceChain, error) {
	if !flow.parsed || path.Exit != PathExitLAN {
		return ServiceChain{}, ErrInvalidServiceChain
	}
	if len(path.Traversal) > MaxServiceChainHops {
		return ServiceChain{}, ErrServiceChainHopLimit
	}
	forwardMatch := flowTuple(flow)
	reverseMatch := reverseFlowTuple(forwardMatch)
	chain := ServiceChain{
		ID:     serviceChainID(forwardMatch, path.Traversal),
		Direct: len(path.Traversal) == 0,
		Forward: ServiceChainPath{Direction: ServiceChainForward, Match: forwardMatch,
			IngressInterface: logicalInterfaceName(topology.wan), ExitInterface: logicalInterfaceName(topology.lan)},
		Reverse: ServiceChainPath{Direction: ServiceChainReverse, Match: reverseMatch,
			IngressInterface: logicalInterfaceName(topology.lan), ExitInterface: logicalInterfaceName(topology.wan)},
	}
	if chain.Direct {
		return chain, nil
	}
	groups := make(map[string]Group, len(topology.groups))
	for _, group := range topology.groups {
		groups[group.Name()] = group
	}
	bindings, err := parseServiceChainBindings(inputs, groups, flow.sourceIP.Is6())
	if err != nil {
		return ServiceChain{}, err
	}
	seen := make(map[string]struct{}, len(path.Traversal))
	forwardIngress := chain.Forward.IngressInterface
	for index, name := range path.Traversal {
		if _, exists := seen[name]; exists {
			return ServiceChain{}, fmt.Errorf("%w: %q", ErrDuplicateServiceChainHop, name)
		}
		seen[name] = struct{}{}
		group, exists := groups[name]
		if !exists {
			return ServiceChain{}, fmt.Errorf("%w: group %q", ErrInvalidServiceChain, name)
		}
		binding, exists := bindings[name]
		if !exists {
			return ServiceChain{}, fmt.Errorf("%w: group %q", ErrMissingServiceChainReturn, name)
		}
		lanPort, wanPort := directedGroupPorts(group)
		chain.Forward.Hops = append(chain.Forward.Hops, ServiceChainHop{Position: index + 1, Group: name, IngressInterface: forwardIngress, ServiceInterface: wanPort, ReturnInterface: lanPort, NextHop: binding.wanNextHop.String()})
		forwardIngress = lanPort
	}
	reverseIngress := chain.Reverse.IngressInterface
	for index := len(path.Traversal) - 1; index >= 0; index-- {
		name := path.Traversal[index]
		group := groups[name]
		binding := bindings[name]
		lanPort, wanPort := directedGroupPorts(group)
		chain.Reverse.Hops = append(chain.Reverse.Hops, ServiceChainHop{Position: len(path.Traversal) - index, Group: name, IngressInterface: reverseIngress, ServiceInterface: lanPort, ReturnInterface: wanPort, NextHop: binding.lanNextHop.String()})
		reverseIngress = wanPort
	}
	if err := ValidateServiceChainState(chain); err != nil {
		return ServiceChain{}, err
	}
	return chain, nil
}

func parseServiceChainBindings(inputs []ServiceChainBindingInput, groups map[string]Group, ipv6 bool) (map[string]serviceChainBinding, error) {
	bindings := make(map[string]serviceChainBinding, len(inputs))
	for _, input := range inputs {
		name := strings.TrimSpace(input.Group)
		if _, exists := groups[name]; !exists {
			return nil, fmt.Errorf("%w: binding group %q", ErrInvalidServiceChain, name)
		}
		if _, exists := bindings[name]; exists {
			return nil, fmt.Errorf("%w: binding group %q", ErrDuplicateServiceChainHop, name)
		}
		wan, wanErr := netip.ParseAddr(strings.TrimSpace(input.WANFacingNextHop))
		lan, lanErr := netip.ParseAddr(strings.TrimSpace(input.LANFacingNextHop))
		if wanErr != nil || lanErr != nil || wan.Is6() != ipv6 || lan.Is6() != ipv6 {
			return nil, fmt.Errorf("%w: group %q requires same-family next hops on both arms", ErrMissingServiceChainReturn, name)
		}
		bindings[name] = serviceChainBinding{wanNextHop: wan, lanNextHop: lan}
	}
	return bindings, nil
}

func directedGroupPorts(group Group) (string, string) {
	var lan, wan string
	for _, port := range group.ports {
		if port.direction == DirectionLANFacing {
			lan = port.interfaceName.String()
		} else {
			wan = port.interfaceName.String()
		}
	}
	return lan, wan
}

func logicalInterfaceName(item LogicalInterface) string {
	if item.hasBond {
		return item.bond.name.String()
	}
	return item.port.String()
}

func flowTuple(flow PolicyFlow) FlowTuple {
	return FlowTuple{SourceIP: flow.sourceIP.String(), DestinationIP: flow.destinationIP.String(), Protocol: flow.protocol, SourcePort: flow.sourcePort, DestinationPort: flow.destinationPort}
}

func reverseFlowTuple(flow FlowTuple) FlowTuple {
	return FlowTuple{SourceIP: flow.DestinationIP, DestinationIP: flow.SourceIP, Protocol: flow.Protocol, SourcePort: flow.DestinationPort, DestinationPort: flow.SourcePort}
}

func serviceChainID(flow FlowTuple, traversal []string) string {
	canonical := fmt.Sprintf("%s|%s|%s|%d|%d|%s", flow.SourceIP, flow.DestinationIP, flow.Protocol, flow.SourcePort, flow.DestinationPort, strings.Join(traversal, ","))
	digest := sha256.Sum256([]byte(canonical))
	return "chain-" + hex.EncodeToString(digest[:8])
}
