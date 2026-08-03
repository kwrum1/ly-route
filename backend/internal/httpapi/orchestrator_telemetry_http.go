package httpapi

import (
	"context"
	"math"

	controlapi "ly-route/backend/internal/api"
	orchestratortelemetry "ly-route/backend/internal/orchestrator/telemetry"
	"ly-route/backend/internal/product"
)

type OrchestratorTelemetryCollector interface {
	Collect(context.Context) orchestratortelemetry.Snapshot
}

func WithOrchestratorTelemetry(collector OrchestratorTelemetryCollector) Option {
	return func(server *Server) { server.orchestratorTelemetry = collector }
}

func (server *Server) orchestratorTelemetryPayload(ctx context.Context, kind, runtimeState string, components []RuntimeComponentState, lastApply *RuntimeApplyResult) (any, controlapi.CapabilityState, bool) {
	if server.profile.ID() != product.Orchestrator().ID() || kind == "interfaces" {
		return nil, controlapi.CapabilityState{}, false
	}
	if kind != "dashboard" && kind != "policy_hits" && kind != "online_users" && kind != "top_sessions" {
		return nil, controlapi.CapabilityState{}, false
	}
	if server.orchestratorTelemetry == nil {
		reason := "orchestrator VPP telemetry collector is not configured"
		capability := degradedCapability("orchestrator_vpp_telemetry", reason)
		switch kind {
		case "dashboard":
			return map[string]any{"device_mode": "orchestrator", "runtime_state": runtimeState, "active_path": "vpp", "degraded": true, "degraded_reason": reason, "components": components, "last_apply": lastApply, "sessions": 0, "online_users": 0, "throughput_bps": 0, "policy_hits": 0, "orchestration_groups": []orchestratortelemetry.GroupTraffic{}}, capability, true
		case "policy_hits":
			return []orchestratortelemetry.PolicyHit{}, capability, true
		default:
			return map[string]any{"items": []any{}, "runtime_state": runtimeState, "state": "unavailable", "degraded": true, "degraded_reason": reason}, capability, true
		}
	}

	snapshot := server.orchestratorTelemetry.Collect(ctx)
	available := snapshot.Status.State == orchestratortelemetry.StateAvailable
	capability := orchestratorComponentCapability("orchestrator_vpp_telemetry", orchestratortelemetry.ComponentStatus{State: snapshot.Status.State, Reason: snapshot.Status.Reason})
	switch kind {
	case "dashboard":
		capability = orchestratorComponentCapability("orchestrator_interface_telemetry", snapshot.Components.Interfaces)
		available = capability.Available
		var policyHits uint64
		for _, item := range snapshot.PolicyHits {
			policyHits += item.Hits
		}
		throughput := snapshot.Totals.WAN.WANToLAN.BytesPerSecond + snapshot.Totals.WAN.LANToWAN.BytesPerSecond
		return map[string]any{
			"device_mode": "orchestrator", "runtime_state": runtimeState, "active_path": "vpp",
			"degraded": !available || runtimeState != "running", "degraded_reason": snapshot.Status.Reason,
			"components": components, "last_apply": lastApply, "telemetry_status": snapshot.Status,
			"sessions": len(snapshot.TopConnections), "online_users": len(snapshot.OnlineUsers),
			"throughput_bps": throughput * 8, "policy_hits": policyHits,
			"boundary_totals": snapshot.Totals, "orchestration_groups": snapshot.Groups,
		}, capability, true
	case "policy_hits":
		capability = orchestratorComponentCapability("orchestrator_policy_telemetry", snapshot.Components.PolicyHits)
		return snapshot.PolicyHits, capability, true
	case "online_users":
		capability = orchestratorComponentCapability("orchestrator_neighbor_telemetry", snapshot.Components.Neighbors)
		return map[string]any{"items": snapshot.OnlineUsers, "runtime_state": runtimeState, "state": snapshot.Components.Neighbors.State, "degraded": !capability.Available, "degraded_reason": snapshot.Components.Neighbors.Reason}, capability, true
	case "top_sessions":
		capability = orchestratorComponentCapability("orchestrator_connection_telemetry", snapshot.Components.Connections)
		return map[string]any{"items": snapshot.TopConnections, "runtime_state": runtimeState, "state": snapshot.Components.Connections.State, "degraded": !capability.Available, "degraded_reason": snapshot.Components.Connections.Reason}, capability, true
	default:
		return nil, capability, false
	}
}

func (server *Server) orchestratorTrafficTrend(ctx context.Context, query TrafficTrendQuery) (TrafficTrendResult, controlapi.CapabilityState) {
	if server.orchestratorTelemetry == nil {
		reason := "orchestrator VPP telemetry collector is not configured"
		return unavailableTrafficTrend(query.Window, query.Points, reason), degradedCapability("orchestrator_vpp_telemetry", reason)
	}
	snapshot := server.orchestratorTelemetry.Collect(ctx)
	interfaceStatus := snapshot.Components.Interfaces
	result := TrafficTrendResult{Window: query.Window, Points: query.Points, SamplingIntervalSeconds: 300, State: string(interfaceStatus.State), Degraded: interfaceStatus.State != orchestratortelemetry.StateAvailable, DegradedReason: interfaceStatus.Reason}
	seriesByName := make(map[string]*LogicalEgressSeries, len(snapshot.Groups))
	result.Series.LogicalEgresses = make([]LogicalEgressSeries, 0, len(snapshot.Groups))
	for _, group := range snapshot.Groups {
		result.Series.LogicalEgresses = append(result.Series.LogicalEgresses, LogicalEgressSeries{ID: group.Name, Name: group.Name, Kind: LogicalEgressOrchestrationGroup, Health: string(group.State), State: string(group.State), Fresh: snapshot.Status.Fresh})
	}
	for index := range result.Series.LogicalEgresses {
		series := &result.Series.LogicalEgresses[index]
		seriesByName[series.ID] = series
	}
	for _, point := range snapshot.History.Traffic {
		for _, group := range point.Groups {
			series := seriesByName[group.Name]
			if series == nil {
				continue
			}
			downloadRate := group.WANToLAN.BytesPerSecond * 8
			uploadRate := group.LANToWAN.BytesPerSecond * 8
			sample := LogicalEgressSample{Timestamp: point.Timestamp, Health: string(group.State), DownloadBytes: boundedInt64(group.WANToLAN.Bytes), UploadBytes: boundedInt64(group.LANToWAN.Bytes)}
			if group.WANToLAN.RateState != orchestratortelemetry.StateUnavailable {
				sample.DownloadBPS = &downloadRate
			}
			if group.LANToWAN.RateState != orchestratortelemetry.StateUnavailable {
				sample.UploadBPS = &uploadRate
			}
			series.Samples = append(series.Samples, sample)
			series.LastSampleAt = point.Timestamp
		}
	}
	for _, group := range snapshot.Groups {
		result.Totals.DownloadBPS += group.WANToLAN.BytesPerSecond * 8
		result.Totals.UploadBPS += group.LANToWAN.BytesPerSecond * 8
	}
	capability := controlapi.CapabilityState{Name: "orchestrator_vpp_telemetry", Available: !result.Degraded, State: controlapi.CapabilityAvailable, Reason: result.DegradedReason}
	if result.Degraded {
		capability.State = controlapi.CapabilityDegraded
	}
	return result, capability
}

func degradedCapability(name, reason string) controlapi.CapabilityState {
	return controlapi.CapabilityState{Name: name, Available: false, State: controlapi.CapabilityDegraded, Reason: reason}
}

func orchestratorComponentCapability(name string, component orchestratortelemetry.ComponentStatus) controlapi.CapabilityState {
	capability := controlapi.CapabilityState{Name: name, Available: component.State == orchestratortelemetry.StateAvailable, State: controlapi.CapabilityAvailable, Reason: component.Reason}
	if !capability.Available {
		capability.State = controlapi.CapabilityDegraded
	}
	return capability
}

func boundedInt64(value uint64) int64 {
	if value > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(value)
}
