package orchestrator

const PolicySchemaVersion = 1

type Protocol string

const (
	ProtocolAny    Protocol = "any"
	ProtocolTCP    Protocol = "tcp"
	ProtocolUDP    Protocol = "udp"
	ProtocolICMP   Protocol = "icmp"
	ProtocolICMPv6 Protocol = "icmpv6"
)

type ActionKind string

const (
	ActionVia    ActionKind = "via"
	ActionDirect ActionKind = "direct"
	ActionDrop   ActionKind = "drop"
)

type PolicyInput struct {
	SchemaVersion int                `json:"schema_version"`
	IPObjects     []IPObjectInput    `json:"ip_objects"`
	Groups        []PolicyGroupInput `json:"policy_groups"`
	Default       ActionInput        `json:"default"`
}

type IPObjectInput struct {
	ID       string   `json:"id"`
	Prefixes []string `json:"prefixes"`
}

type PolicyGroupInput struct {
	ID       string            `json:"id"`
	Position int               `json:"position"`
	Rules    []PolicyRuleInput `json:"rules"`
}

type PolicyRuleInput struct {
	ID       string           `json:"id"`
	Sequence int              `json:"sequence"`
	Match    PolicyMatchInput `json:"match"`
	Action   ActionInput      `json:"action"`
}

type PolicyMatchInput struct {
	Sources          []string         `json:"sources"`
	Destinations     []string         `json:"destinations"`
	Protocol         Protocol         `json:"protocol"`
	SourcePorts      []PortRangeInput `json:"source_ports,omitempty"`
	DestinationPorts []PortRangeInput `json:"destination_ports,omitempty"`
}

type PortRangeInput struct {
	Start uint16 `json:"start"`
	End   uint16 `json:"end"`
}

type ActionInput struct {
	Kind  ActionKind `json:"kind"`
	Group string     `json:"group,omitempty"`
}

type FlowInput struct {
	SourceIP        string   `json:"source_ip"`
	DestinationIP   string   `json:"destination_ip"`
	Protocol        Protocol `json:"protocol"`
	SourcePort      uint16   `json:"source_port,omitempty"`
	DestinationPort uint16   `json:"destination_port,omitempty"`
}

type PreludeInput struct {
	SecurityDrop    string   `json:"security_drop,omitempty"`
	TrafficControls []string `json:"traffic_controls,omitempty"`
}
