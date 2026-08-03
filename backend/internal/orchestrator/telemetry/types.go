package telemetry

import (
	"context"
	"time"
)

type State string

const (
	StateAvailable   State = "available"
	StateStale       State = "stale"
	StateUnavailable State = "unavailable"
)

type Clock interface {
	Now() time.Time
}

type ClockFunc func() time.Time

func (clock ClockFunc) Now() time.Time { return clock() }

type Source interface {
	Observe(context.Context) (Observation, error)
}

type Observation struct {
	ObservedAt  time.Time
	Interfaces  []InterfaceCounter
	PolicyHits  []PolicyHitCounter
	Neighbors   []NeighborEntry
	Connections []ConnectionEntry
	GroupHealth []GroupHealthCounter
	Components  ComponentStatuses
}

type ComponentStatus struct {
	State  State  `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type ComponentStatuses struct {
	Interfaces  ComponentStatus `json:"interfaces"`
	PolicyHits  ComponentStatus `json:"policy_hits"`
	Neighbors   ComponentStatus `json:"neighbors"`
	Connections ComponentStatus `json:"connections"`
}

type InterfaceCounter struct {
	Name    string
	RXBytes uint64
	TXBytes uint64
	LinkUp  bool
}

type PolicyHitCounter struct {
	PolicyID string
	Hits     uint64
}

type GroupHealthCounter struct {
	Name          string
	Bypass        bool
	BypassPackets uint64
}

type NeighborEntry struct {
	IP        string
	MAC       string
	Interface string
	LastSeen  time.Time
	RXBytes   uint64
	TXBytes   uint64
}

type ConnectionEntry struct {
	ID              string
	SourceIP        string
	DestinationIP   string
	Protocol        string
	SourcePort      uint16
	DestinationPort uint16
	Bytes           uint64
	Packets         uint64
	LastSeen        time.Time
	Groups          []string
}

type Status struct {
	State       State     `json:"state"`
	Fresh       bool      `json:"fresh"`
	CollectedAt time.Time `json:"collected_at"`
	ObservedAt  time.Time `json:"observed_at,omitempty"`
	Reason      string    `json:"reason,omitempty"`
}

type Snapshot struct {
	Status         Status              `json:"status"`
	Components     ComponentStatuses   `json:"components"`
	Totals         BoundaryTotals      `json:"totals"`
	Groups         []GroupTraffic      `json:"groups"`
	Reconciliation ChainReconciliation `json:"reconciliation"`
	PolicyHits     []PolicyHit         `json:"policy_hits"`
	OnlineUsers    []OnlineUser        `json:"online_users"`
	TopConnections []TopConnection     `json:"top_connections"`
	Expiration     ExpirationSummary   `json:"expiration"`
	History        History             `json:"history"`
}

type BoundaryTotals struct {
	WAN EndpointTraffic `json:"wan"`
	LAN EndpointTraffic `json:"lan"`
}

type EndpointTraffic struct {
	ID       string             `json:"id"`
	State    State              `json:"state"`
	Reason   string             `json:"reason,omitempty"`
	WANToLAN DirectionalCounter `json:"wan_to_lan"`
	LANToWAN DirectionalCounter `json:"lan_to_wan"`
}

type GroupTraffic struct {
	Name          string             `json:"name"`
	Additive      bool               `json:"additive"`
	State         State              `json:"state"`
	Reason        string             `json:"reason,omitempty"`
	Bypass        bool               `json:"bypass"`
	BypassPackets uint64             `json:"bypass_packets"`
	WANToLAN      DirectionalCounter `json:"wan_to_lan"`
	LANToWAN      DirectionalCounter `json:"lan_to_wan"`
}

type DirectionalCounter struct {
	Bytes          uint64  `json:"bytes"`
	BytesPerSecond float64 `json:"bytes_per_second,omitempty"`
	RateState      State   `json:"rate_state"`
	RateReason     string  `json:"rate_reason,omitempty"`
}

type ChainReconciliation struct {
	TolerancePercent float64              `json:"tolerance_percent"`
	WANToLAN         ReconciliationResult `json:"wan_to_lan"`
	LANToWAN         ReconciliationResult `json:"lan_to_wan"`
}

type ReconciliationResult struct {
	State             State   `json:"state"`
	WithinTolerance   bool    `json:"within_tolerance"`
	DifferencePercent float64 `json:"difference_percent"`
}

type PolicyHit struct {
	PolicyID   string    `json:"policy_id"`
	Hits       uint64    `json:"hits"`
	State      State     `json:"state"`
	ObservedAt time.Time `json:"observed_at"`
}

type OnlineUser struct {
	IP        string    `json:"ip"`
	MAC       string    `json:"mac"`
	Interface string    `json:"interface"`
	RXBytes   uint64    `json:"rx_bytes"`
	TXBytes   uint64    `json:"tx_bytes"`
	LastSeen  time.Time `json:"last_seen"`
}

type TopConnection struct {
	ID              string    `json:"id"`
	SourceIP        string    `json:"source_ip"`
	DestinationIP   string    `json:"destination_ip"`
	Protocol        string    `json:"protocol"`
	SourcePort      uint16    `json:"source_port"`
	DestinationPort uint16    `json:"destination_port"`
	Bytes           uint64    `json:"bytes"`
	Packets         uint64    `json:"packets"`
	LastSeen        time.Time `json:"last_seen"`
	Groups          []string  `json:"groups"`
}

type ExpirationSummary struct {
	Users       int `json:"users"`
	Connections int `json:"connections"`
}

type History struct {
	WindowSeconds int64             `json:"window_seconds"`
	Traffic       []TrafficPoint    `json:"traffic"`
	Connections   []ConnectionPoint `json:"connections"`
}

type TrafficPoint struct {
	Timestamp time.Time      `json:"timestamp"`
	Totals    BoundaryTotals `json:"totals"`
	Groups    []GroupTraffic `json:"groups"`
}

type ConnectionPoint struct {
	Timestamp   time.Time       `json:"timestamp"`
	Connections []TopConnection `json:"connections"`
}
