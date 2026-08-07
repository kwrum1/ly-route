package vpp

import (
	"errors"
	"time"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

var (
	ErrVPPUnavailable     = errors.New("vpp unavailable")
	ErrSnapshotIncomplete = errors.New("vpp snapshot incomplete")
)

type SnapshotCapability string

const (
	SnapshotCapabilityInterfaces    SnapshotCapability = "interfaces"
	SnapshotCapabilityBonds         SnapshotCapability = "bonds"
	SnapshotCapabilityRoutePolicies SnapshotCapability = "route_policies"
	SnapshotCapabilityWANGroups     SnapshotCapability = "wan_groups"
	SnapshotCapabilityACLs          SnapshotCapability = "acls"
	SnapshotCapabilityQoS           SnapshotCapability = "qos"
	SnapshotCapabilityNAT44         SnapshotCapability = "nat44"
)

type SnapshotRequest struct {
	TransactionID       string
	ManagementInterface string
	// AllowMissing is used only for a verified production drift readback. A
	// successful inventory with no requested product object means that the
	// object was removed while VPP was restarted; an unreadable inventory must
	// still fail closed.
	AllowMissing          bool
	VerifyNATReturnGuards bool
	Interfaces            []string
	AbsentInterfaces      []string
	Bonds                 []string
	AbsentBonds           []string
	RoutePolicies         []string
	WANGroups             []string
	ACLs                  []string
	QoS                   []string
	NATStaticMappings     []string
	NATPortMappings       []string
	AbsentRoutePolicies   []string
	AbsentWANGroups       []string
	AbsentACLs            []string
	AbsentQoS             []string
	AbsentNATStatic       []string
	AbsentNATPort         []string
	Capabilities          []SnapshotCapability
	ReadbackAt            time.Time
	Candidates            SnapshotCandidates
}

type SnapshotCandidates struct {
	Interfaces        []InterfaceState
	Bonds             []BondState
	RoutePolicies     []trafficpolicy.RoutePolicy
	WANGroups         []trafficpolicy.WANGroup
	ACLs              []trafficpolicy.SecurityACL
	QoS               []flow.VPPObjectGroup
	NATStaticMappings []nat.StaticMapping
	NATPortMappings   []nat.PortMapping
}

type InterfaceState struct {
	Name       string   `json:"name"`
	AdminState string   `json:"admin_state"`
	LinkState  string   `json:"link_state"`
	Addresses  []string `json:"addresses,omitempty"`
}

type BondState struct {
	Name    string   `json:"name"`
	Mode    string   `json:"mode"`
	Members []string `json:"members"`
}

type InterfaceReadback struct {
	Interfaces []InterfaceState `json:"interfaces"`
}

type BondReadback struct {
	Bonds []BondState `json:"bonds"`
}

type ACLReadback struct {
	ACLs []trafficpolicy.SecurityACL `json:"acls"`
}

type QoSReadback struct {
	Groups []flow.VPPObjectGroup `json:"groups"`
}
