package orchestrator

import (
	"errors"
	"testing"
)

func TestValidateServiceChainState_rejects_asymmetric_and_missing_return(t *testing.T) {
	// Given
	valid := validServiceChainState()
	tests := []struct {
		name    string
		mutate  func(ServiceChain) ServiceChain
		wantErr error
	}{
		{name: "missing return", mutate: func(chain ServiceChain) ServiceChain { chain.Reverse.Hops = nil; return chain }, wantErr: ErrMissingServiceChainReturn},
		{name: "asymmetric order", mutate: func(chain ServiceChain) ServiceChain {
			chain.Reverse.Hops[0], chain.Reverse.Hops[1] = chain.Reverse.Hops[1], chain.Reverse.Hops[0]
			return chain
		}, wantErr: ErrAsymmetricServiceChain},
		{name: "asymmetric tuple", mutate: func(chain ServiceChain) ServiceChain { chain.Reverse.Match.SourceIP = "203.0.113.9"; return chain }, wantErr: ErrAsymmetricServiceChain},
		{name: "duplicate reverse hop", mutate: func(chain ServiceChain) ServiceChain { chain.Reverse.Hops[1].Group = "group-b"; return chain }, wantErr: ErrDuplicateServiceChainHop},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chain := valid
			chain.Forward.Hops = append([]ServiceChainHop(nil), valid.Forward.Hops...)
			chain.Reverse.Hops = append([]ServiceChainHop(nil), valid.Reverse.Hops...)

			// When
			err := ValidateServiceChainState(test.mutate(chain))

			// Then
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateServiceChainState_rejects_direction_mismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(ServiceChain) ServiceChain
	}{
		{name: "forward direction", mutate: func(chain ServiceChain) ServiceChain {
			chain.Forward.Direction = ServiceChainReverse
			return chain
		}},
		{name: "reverse direction", mutate: func(chain ServiceChain) ServiceChain {
			chain.Reverse.Direction = ServiceChainForward
			return chain
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			chain := test.mutate(validServiceChainState())

			// When
			err := ValidateServiceChainState(chain)

			// Then
			if !errors.Is(err, ErrInvalidServiceChainDirection) {
				t.Fatalf("error = %v, want ErrInvalidServiceChainDirection", err)
			}
		})
	}
}

func TestValidateServiceChainState_rejects_address_and_protocol_family_mismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(ServiceChain) ServiceChain
	}{
		{name: "mixed address family", mutate: func(chain ServiceChain) ServiceChain {
			chain.Forward.Match.DestinationIP = "2001:db8::2"
			chain.Reverse.Match = reverseFlowTuple(chain.Forward.Match)
			return chain
		}},
		{name: "icmpv6 over ipv4", mutate: func(chain ServiceChain) ServiceChain {
			chain.Forward.Match.Protocol = ProtocolICMPv6
			chain.Reverse.Match = reverseFlowTuple(chain.Forward.Match)
			return chain
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			chain := test.mutate(validServiceChainState())

			// When
			err := ValidateServiceChainState(chain)

			// Then
			if !errors.Is(err, ErrInvalidServiceChainFamily) {
				t.Fatalf("error = %v, want ErrInvalidServiceChainFamily", err)
			}
		})
	}
}

func validServiceChainState() ServiceChain {
	forward := FlowTuple{SourceIP: "198.18.0.2", DestinationIP: "198.18.5.2", Protocol: ProtocolTCP, SourcePort: 41000, DestinationPort: 443}
	reverse := reverseFlowTuple(forward)
	return ServiceChain{
		ID: "chain-validation",
		Forward: ServiceChainPath{Direction: ServiceChainForward, Match: forward, IngressInterface: "wan0", ExitInterface: "lan0", Hops: []ServiceChainHop{
			{Position: 1, Group: "group-a", IngressInterface: "wan0", ServiceInterface: "a-wan", ReturnInterface: "a-lan", NextHop: "198.18.1.2"},
			{Position: 2, Group: "group-b", IngressInterface: "a-lan", ServiceInterface: "b-wan", ReturnInterface: "b-lan", NextHop: "198.18.3.2"},
		}},
		Reverse: ServiceChainPath{Direction: ServiceChainReverse, Match: reverse, IngressInterface: "lan0", ExitInterface: "wan0", Hops: []ServiceChainHop{
			{Position: 1, Group: "group-b", IngressInterface: "lan0", ServiceInterface: "b-lan", ReturnInterface: "b-wan", NextHop: "198.18.4.2"},
			{Position: 2, Group: "group-a", IngressInterface: "b-wan", ServiceInterface: "a-lan", ReturnInterface: "a-wan", NextHop: "198.18.2.2"},
		}},
	}
}
