package httpapi

import (
	"context"
	"time"
)

type LogicalEgressKind string

const (
	LogicalEgressDirectWAN          LogicalEgressKind = "direct_wan"
	LogicalEgressWANGroup           LogicalEgressKind = "wan_group"
	LogicalEgressProxy              LogicalEgressKind = "proxy"
	LogicalEgressOrchestrationGroup LogicalEgressKind = "orchestration_group"
)

type GatewayTelemetryCollector interface {
	Collect(context.Context) (GatewayTelemetrySnapshot, error)
}

type GatewayTelemetrySnapshot struct {
	ObservedAt      time.Time              `json:"observed_at"`
	LogicalEgresses []LogicalEgressCounter `json:"logical_egresses"`
	Connections     []GatewayConnection    `json:"connections"`
	Neighbors       []GatewayNeighbor      `json:"neighbors"`
}

type LogicalEgressCounter struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Kind          LogicalEgressKind `json:"kind"`
	Health        string            `json:"health"`
	State         string            `json:"state,omitempty"`
	UnderlayWANID string            `json:"underlay_wan_id,omitempty"`
	ActiveMember  string            `json:"active_member,omitempty"`
	DownloadBytes int64             `json:"download_bytes"`
	UploadBytes   int64             `json:"upload_bytes"`
}

type GatewayConnection struct {
	SourceIP        string    `json:"src_ip"`
	DestinationIP   string    `json:"dst_ip"`
	Protocol        string    `json:"protocol"`
	SourcePort      int       `json:"src_port"`
	DestinationPort int       `json:"dst_port"`
	Bytes           int64     `json:"bytes"`
	ObservedAt      time.Time `json:"observed_at"`
}

type GatewayNeighbor struct {
	IP            string    `json:"ip"`
	MAC           string    `json:"mac"`
	LastSeen      time.Time `json:"last_seen"`
	DownloadBytes int64     `json:"download_bytes"`
	UploadBytes   int64     `json:"upload_bytes"`
}

type TrafficTrendCollector interface {
	TrafficTrend(context.Context, TrafficTrendQuery) (TrafficTrendResult, error)
}

type TrafficTrendQuery struct {
	Window string
	Points int
}

type TrafficTrendResult struct {
	Window                  string              `json:"window"`
	Points                  int                 `json:"points"`
	SamplingIntervalSeconds int                 `json:"sampling_interval_seconds"`
	State                   string              `json:"state"`
	Degraded                bool                `json:"degraded"`
	DegradedReason          string              `json:"degraded_reason,omitempty"`
	Totals                  LogicalEgressTotals `json:"totals"`
	Series                  TrafficTrendSets    `json:"series"`
}

type TrafficTrendSets struct {
	LogicalEgresses []LogicalEgressSeries `json:"logical_egresses"`
}

type LogicalEgressTotals struct {
	DownloadBPS float64 `json:"download_bps"`
	UploadBPS   float64 `json:"upload_bps"`
}

type LogicalEgressSeries struct {
	ID            string                `json:"id"`
	Name          string                `json:"name"`
	Kind          LogicalEgressKind     `json:"kind"`
	Health        string                `json:"health"`
	UnderlayWANID string                `json:"underlay_wan_id,omitempty"`
	ActiveMember  string                `json:"active_member,omitempty"`
	State         string                `json:"state"`
	Fresh         bool                  `json:"fresh"`
	LastSampleAt  time.Time             `json:"last_sample_at"`
	Samples       []LogicalEgressSample `json:"samples"`
}

type LogicalEgressSample struct {
	Timestamp     time.Time `json:"timestamp"`
	Health        string    `json:"health"`
	DownloadBytes int64     `json:"download_bytes"`
	UploadBytes   int64     `json:"upload_bytes"`
	DownloadBPS   *float64  `json:"download_bps,omitempty"`
	UploadBPS     *float64  `json:"upload_bps,omitempty"`
}
