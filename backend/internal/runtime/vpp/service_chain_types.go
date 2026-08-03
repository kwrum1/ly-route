package vpp

import (
	"errors"

	"ly-route/backend/internal/orchestrator"
)

var (
	ErrServiceChainCapability = errors.New("VPP service chain capability is not proven")
	ErrServiceChainReadback   = errors.New("VPP service chain readback is incomplete")
)

type ServiceChainPolicy struct {
	ChainID          string                             `json:"chain_id"`
	Direction        orchestrator.ServiceChainDirection `json:"direction"`
	Position         int                                `json:"position"`
	Group            string                             `json:"group"`
	ACLID            int                                `json:"acl_id"`
	PolicyID         int                                `json:"policy_id"`
	Priority         int                                `json:"priority"`
	AddressFamily    string                             `json:"address_family"`
	IngressInterface string                             `json:"ingress_interface"`
	ServiceInterface string                             `json:"service_interface"`
	ReturnInterface  string                             `json:"return_interface"`
	NextHop          string                             `json:"next_hop"`
	Match            orchestrator.FlowTuple             `json:"match"`
}

type ServiceChainPolicyReadback struct {
	ChainID          string                             `json:"chain_id"`
	Direction        orchestrator.ServiceChainDirection `json:"direction"`
	Position         int                                `json:"position"`
	Group            string                             `json:"group"`
	ACLID            int                                `json:"acl_id"`
	PolicyID         int                                `json:"policy_id"`
	Priority         int                                `json:"priority"`
	AddressFamily    string                             `json:"address_family"`
	IngressInterface string                             `json:"ingress_interface"`
	ServiceInterface string                             `json:"service_interface"`
	NextHop          string                             `json:"next_hop"`
	Match            orchestrator.FlowTuple             `json:"match"`
	Attached         bool                               `json:"attached"`
}

type ServiceChainInterfaceReadback struct {
	Interface string `json:"interface"`
	RXPackets uint64 `json:"rx_packets"`
	TXPackets uint64 `json:"tx_packets"`
	RXBytes   uint64 `json:"rx_bytes"`
	TXBytes   uint64 `json:"tx_bytes"`
}

type ServiceChainReadback struct {
	ChainID    string                          `json:"chain_id"`
	Policies   []ServiceChainPolicyReadback    `json:"policies"`
	Interfaces []ServiceChainInterfaceReadback `json:"interfaces"`
}

type ServiceChainApplyResult struct {
	Receipt  Receipt              `json:"receipt"`
	Readback ServiceChainReadback `json:"readback"`
}
