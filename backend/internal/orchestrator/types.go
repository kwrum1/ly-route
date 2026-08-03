package orchestrator

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidSchemaVersion  = errors.New("invalid orchestrator topology schema version")
	ErrInvalidName           = errors.New("invalid interface topology name")
	ErrInvalidInterface      = errors.New("invalid logical interface")
	ErrInvalidBond           = errors.New("invalid interface bond")
	ErrMissingLAN            = errors.New("logical LAN is required")
	ErrDuplicateLAN          = errors.New("logical LAN must be unique")
	ErrMissingWAN            = errors.New("logical WAN is required")
	ErrDuplicateWAN          = errors.New("logical WAN must be unique")
	ErrSharedInterface       = errors.New("interface ownership must be globally unique")
	ErrGroupSize             = errors.New("orchestration group must contain exactly two ports")
	ErrGroupDirection        = errors.New("orchestration group requires one LAN-facing and one WAN-facing port")
	ErrGroupBond             = errors.New("orchestration groups cannot contain bonds")
	ErrManagementMembership  = errors.New("management interface is Linux-owned and globally excluded")
	ErrLogicalMembership     = errors.New("orchestration groups cannot contain logical LAN or WAN interfaces")
	ErrDuplicateGroup        = errors.New("orchestration group name must be unique")
	ErrTopologyNotFound      = errors.New("orchestrator topology not found")
	ErrTopologyConflict      = errors.New("orchestrator topology changed during mutation")
	ErrGroupNotFound         = errors.New("orchestration group not found")
	ErrRepositoryUnavailable = errors.New("orchestrator repository store is unavailable")
)

type Role string

const (
	RoleLAN Role = "lan"
	RoleWAN Role = "wan"
)

type Direction string

const (
	DirectionLANFacing Direction = "lan_facing"
	DirectionWANFacing Direction = "wan_facing"
)

type InterfaceName struct {
	value string
}

func (name InterfaceName) String() string {
	return name.value
}

type Bond struct {
	name    InterfaceName
	members []InterfaceName
}

type LogicalInterface struct {
	name    InterfaceName
	role    Role
	port    InterfaceName
	bond    Bond
	hasBond bool
}

type DirectedPort struct {
	interfaceName InterfaceName
	direction     Direction
	bond          Bond
	hasBond       bool
}

type Group struct {
	name  InterfaceName
	ports [2]DirectedPort
}

func (group Group) Name() string {
	return group.name.String()
}

type Topology struct {
	management       InterfaceName
	managementShared bool
	lan              LogicalInterface
	wan              LogicalInterface
	groups           []Group
}

func (topology Topology) withGroups(groups []Group) (Topology, error) {
	input := topology.View().input()
	input.Groups = make([]GroupInput, 0, len(groups))
	for _, group := range groups {
		input.Groups = append(input.Groups, group.View().input())
	}
	parsed, err := ParseTopology(input)
	if err != nil {
		return Topology{}, fmt.Errorf("rebuild orchestrator topology: %w", err)
	}
	return parsed, nil
}

func (topology Topology) groupValues() []Group {
	return append([]Group(nil), topology.groups...)
}
