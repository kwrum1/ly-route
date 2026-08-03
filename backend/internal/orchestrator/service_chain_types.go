package orchestrator

import (
	"errors"
	"net/netip"
)

const MaxServiceChainHops = 8

var (
	ErrInvalidServiceChain          = errors.New("invalid service chain")
	ErrInvalidServiceChainDirection = errors.New("invalid service chain direction")
	ErrInvalidServiceChainFamily    = errors.New("invalid service chain address or protocol family")
	ErrServiceChainHopLimit         = errors.New("service chain hop limit exceeded")
	ErrDuplicateServiceChainHop     = errors.New("service chain contains a duplicate hop")
	ErrMissingServiceChainReturn    = errors.New("service chain return state is missing")
	ErrAsymmetricServiceChain       = errors.New("service chain return state is asymmetric")
)

type ServiceChainDirection string

const (
	ServiceChainForward ServiceChainDirection = "forward"
	ServiceChainReverse ServiceChainDirection = "reverse"
)

type ServiceChainBindingInput struct {
	Group            string `json:"group"`
	WANFacingNextHop string `json:"wan_facing_next_hop"`
	LANFacingNextHop string `json:"lan_facing_next_hop"`
}

type FlowTuple struct {
	SourceIP        string   `json:"source_ip"`
	DestinationIP   string   `json:"destination_ip"`
	Protocol        Protocol `json:"protocol"`
	SourcePort      uint16   `json:"source_port,omitempty"`
	DestinationPort uint16   `json:"destination_port,omitempty"`
}

type ServiceChainHop struct {
	Position         int    `json:"position"`
	Group            string `json:"group"`
	IngressInterface string `json:"ingress_interface"`
	ServiceInterface string `json:"service_interface"`
	ReturnInterface  string `json:"return_interface"`
	NextHop          string `json:"next_hop"`
}

type ServiceChainPath struct {
	Direction        ServiceChainDirection `json:"direction"`
	Match            FlowTuple             `json:"match"`
	IngressInterface string                `json:"ingress_interface"`
	Hops             []ServiceChainHop     `json:"hops"`
	ExitInterface    string                `json:"exit_interface"`
}

type ServiceChain struct {
	ID             string           `json:"id"`
	Direct         bool             `json:"direct"`
	BypassedGroups []string         `json:"bypassed_groups,omitempty"`
	Forward        ServiceChainPath `json:"forward"`
	Reverse        ServiceChainPath `json:"reverse"`
}

func ValidateServiceChainState(chain ServiceChain) error {
	if chain.Forward.Direction != ServiceChainForward || chain.Reverse.Direction != ServiceChainReverse {
		return ErrInvalidServiceChainDirection
	}
	forwardIPv6, err := validateServiceChainFlow(chain.Forward.Match)
	if err != nil {
		return err
	}
	reverseIPv6, err := validateServiceChainFlow(chain.Reverse.Match)
	if err != nil || reverseIPv6 != forwardIPv6 {
		return ErrInvalidServiceChainFamily
	}
	if len(chain.Forward.Hops) > MaxServiceChainHops || len(chain.Reverse.Hops) > MaxServiceChainHops {
		return ErrServiceChainHopLimit
	}
	if chain.Direct {
		if len(chain.Forward.Hops) != 0 || len(chain.Reverse.Hops) != 0 {
			return ErrAsymmetricServiceChain
		}
		return validateServiceChainEdges(chain)
	}
	if len(chain.Forward.Hops) == 0 || len(chain.Reverse.Hops) == 0 || len(chain.Forward.Hops) != len(chain.Reverse.Hops) {
		return ErrMissingServiceChainReturn
	}
	if err := rejectDuplicateServiceChainHops(chain.Forward.Hops); err != nil {
		return err
	}
	if err := rejectDuplicateServiceChainHops(chain.Reverse.Hops); err != nil {
		return err
	}
	if err := validateServiceChainEdges(chain); err != nil {
		return err
	}
	if err := validateServiceChainPath(chain.Forward, forwardIPv6); err != nil {
		return err
	}
	if err := validateServiceChainPath(chain.Reverse, reverseIPv6); err != nil {
		return err
	}
	for index, forward := range chain.Forward.Hops {
		reverse := chain.Reverse.Hops[len(chain.Reverse.Hops)-1-index]
		if forward.Group != reverse.Group || forward.ServiceInterface != reverse.ReturnInterface || forward.ReturnInterface != reverse.ServiceInterface {
			return ErrAsymmetricServiceChain
		}
	}
	return nil
}

func validateServiceChainFlow(flow FlowTuple) (bool, error) {
	source, sourceErr := netip.ParseAddr(flow.SourceIP)
	destination, destinationErr := netip.ParseAddr(flow.DestinationIP)
	if sourceErr != nil || destinationErr != nil || source.Is6() != destination.Is6() {
		return false, ErrInvalidServiceChainFamily
	}
	hasPorts := flow.SourcePort != 0 || flow.DestinationPort != 0
	switch flow.Protocol {
	case ProtocolTCP, ProtocolUDP:
		if !hasPorts {
			return false, ErrInvalidServiceChainFamily
		}
	case ProtocolICMP:
		if source.Is6() || hasPorts {
			return false, ErrInvalidServiceChainFamily
		}
	case ProtocolICMPv6:
		if !source.Is6() || hasPorts {
			return false, ErrInvalidServiceChainFamily
		}
	default:
		return false, ErrInvalidServiceChainFamily
	}
	return source.Is6(), nil
}
func validateServiceChainEdges(chain ServiceChain) error {
	if chain.Forward.IngressInterface != chain.Reverse.ExitInterface || chain.Forward.ExitInterface != chain.Reverse.IngressInterface || chain.Reverse.Match != reverseFlowTuple(chain.Forward.Match) {
		return ErrAsymmetricServiceChain
	}
	return nil
}

func validateServiceChainPath(path ServiceChainPath, ipv6 bool) error {
	ingress := path.IngressInterface
	for index, hop := range path.Hops {
		if hop.Position != index+1 || hop.IngressInterface != ingress || hop.Group == "" || hop.ServiceInterface == "" || hop.ReturnInterface == "" {
			return ErrAsymmetricServiceChain
		}
		nextHop, err := netip.ParseAddr(hop.NextHop)
		if err != nil || nextHop.Is6() != ipv6 {
			return ErrInvalidServiceChainFamily
		}
		ingress = hop.ReturnInterface
	}
	return nil
}

func rejectDuplicateServiceChainHops(hops []ServiceChainHop) error {
	seen := make(map[string]struct{}, len(hops))
	for _, hop := range hops {
		if _, exists := seen[hop.Group]; exists {
			return ErrDuplicateServiceChainHop
		}
		seen[hop.Group] = struct{}{}
	}
	return nil
}
