package orchestrator

import (
	"errors"
	"net/netip"
)

var (
	ErrInvalidPolicyVersion   = errors.New("invalid orchestration policy schema version")
	ErrInvalidIPObject        = errors.New("invalid policy IP object")
	ErrDuplicateIPObject      = errors.New("policy IP object ID must be unique")
	ErrInvalidPolicyGroup     = errors.New("invalid policy group")
	ErrDuplicatePolicyGroup   = errors.New("policy group ID must be unique")
	ErrInvalidPolicyPosition  = errors.New("policy group position must be unique and positive")
	ErrInvalidPolicyRule      = errors.New("invalid policy rule")
	ErrInvalidRuleSequence    = errors.New("policy rule sequence must be positive")
	ErrDuplicateRuleSequence  = errors.New("policy rule sequence must be unique within its group")
	ErrInvalidPolicyMatch     = errors.New("invalid policy match")
	ErrInvalidPolicyAction    = errors.New("invalid policy action")
	ErrDeletedPolicyReference = errors.New("policy references a missing object")
	ErrPolicyLoop             = errors.New("policy can traverse an orchestration group more than once")
	ErrEmptyPolicyDefault     = errors.New("policy default behavior is required")
	ErrInvalidPolicyFlow      = errors.New("invalid policy flow")
	ErrInvalidPolicyPrelude   = errors.New("invalid policy prelude")
	ErrInvalidPolicyCompile   = errors.New("invalid policy compilation input")
)

type policyName struct{ value string }

func (name policyName) String() string { return name.value }

type Policy struct {
	ipObjects     []policyIPObject
	groups        []policyGroup
	defaultAction policyAction
	parsed        bool
}

type policyIPObject struct {
	id       policyName
	prefixes []netip.Prefix
}

type policyGroup struct {
	id       policyName
	position int
	rules    []policyRule
}

type policyRule struct {
	id       policyName
	sequence int
	match    policyMatch
	action   policyAction
}

type policyMatch struct {
	sources          ipSelector
	destinations     ipSelector
	protocol         Protocol
	sourcePorts      []portRange
	destinationPorts []portRange
}

type ipSelector struct {
	any        bool
	references []policyName
	prefixes   []netip.Prefix
}

type portRange struct {
	start uint16
	end   uint16
}

type policyAction struct {
	kind  ActionKind
	group InterfaceName
}

type PolicyFlow struct {
	sourceIP        netip.Addr
	destinationIP   netip.Addr
	protocol        Protocol
	sourcePort      uint16
	destinationPort uint16
	parsed          bool
}

type Prelude struct {
	securityDrop    policyName
	trafficControls []policyName
}

type PolicyView struct {
	SchemaVersion int               `json:"schema_version"`
	IPObjects     []IPObjectView    `json:"ip_objects"`
	Groups        []PolicyGroupView `json:"policy_groups"`
	Default       ActionInput       `json:"default"`
}

type IPObjectView struct {
	ID       string   `json:"id"`
	Prefixes []string `json:"prefixes"`
}

type PolicyGroupView struct {
	ID       string           `json:"id"`
	Position int              `json:"position"`
	Rules    []PolicyRuleView `json:"rules"`
}

type PolicyRuleView struct {
	ID       string           `json:"id"`
	Sequence int              `json:"sequence"`
	Match    PolicyMatchInput `json:"match"`
	Action   ActionInput      `json:"action"`
}
