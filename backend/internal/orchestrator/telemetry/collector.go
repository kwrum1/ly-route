package telemetry

import (
	"context"
	"sync"
	"time"

	"ly-route/backend/internal/orchestrator"
)

const (
	retentionWindow           = 24 * time.Hour
	freshnessWindow           = 2 * time.Minute
	idleWindow                = 5 * time.Minute
	topLimit                  = 20
	maxHistoryPoints          = 86_401
	maxObservationInterfaces  = 4_096
	maxObservationPolicyHits  = 16_384
	maxObservationNeighbors   = 16_384
	maxObservationConnections = 131_072
	maxObservationGroupHealth = 64
)

type trafficBaseline struct {
	observedAt time.Time
	interfaces []InterfaceCounter
}

type Collector struct {
	topology orchestrator.TopologyView
	source   Source
	clock    Clock

	mu                sync.Mutex
	previous          *trafficBaseline
	trafficHistory    []TrafficPoint
	connectionHistory []ConnectionPoint
}

func NewCollector(topology orchestrator.Topology, source Source, clock Clock) *Collector {
	return &Collector{topology: topology.View(), source: source, clock: clock}
}

func (collector *Collector) Collect(ctx context.Context) Snapshot {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if collector.clock == nil {
		return collector.unavailable(time.Time{}, "telemetry clock is not configured")
	}
	if collector.source == nil {
		return collector.unavailable(collector.clock.Now().UTC(), "VPP telemetry collector is not configured")
	}
	observation, err := collector.source.Observe(ctx)
	now := collector.clock.Now().UTC()
	if err != nil {
		return collector.unavailable(now, "VPP telemetry collector failed")
	}
	if observationExceedsLimits(observation) {
		return collector.unavailable(now, "VPP telemetry observation exceeds collector limits")
	}
	observation.ObservedAt = observation.ObservedAt.UTC()
	if observation.ObservedAt.IsZero() {
		return collector.unavailable(now, "VPP telemetry observation has no timestamp")
	}
	if observation.ObservedAt.After(now) {
		return collector.unavailable(now, "VPP telemetry observation timestamp is in the future")
	}

	status := observationStatus(now, observation.ObservedAt)
	components := normalizeComponentStatuses(observation.Components, status)
	totals, groups := collectTraffic(collector.topology, observation, collector.previous, status)
	users, expiredUsers := collectUsers(observation.Neighbors, now)
	connections, expiredConnections := collectConnections(observation.Connections, now)
	snapshot := Snapshot{
		Status:         status,
		Components:     components,
		Totals:         totals,
		Groups:         groups,
		Reconciliation: reconcile(totals, status.State),
		PolicyHits:     collectPolicyHits(observation.PolicyHits, observation.ObservedAt, status.State),
		OnlineUsers:    users,
		TopConnections: connections,
		Expiration:     ExpirationSummary{Users: expiredUsers, Connections: expiredConnections},
	}
	collector.record(observation.ObservedAt, totals, groups, connections)
	if collector.previous == nil || observation.ObservedAt.After(collector.previous.observedAt) {
		collector.previous = &trafficBaseline{
			observedAt: observation.ObservedAt,
			interfaces: append([]InterfaceCounter(nil), observation.Interfaces...),
		}
	}
	collector.prune(now)
	snapshot.History = collector.history()
	return snapshot
}

func observationExceedsLimits(observation Observation) bool {
	return len(observation.Interfaces) > maxObservationInterfaces ||
		len(observation.PolicyHits) > maxObservationPolicyHits ||
		len(observation.Neighbors) > maxObservationNeighbors ||
		len(observation.Connections) > maxObservationConnections ||
		len(observation.GroupHealth) > maxObservationGroupHealth
}

func observationStatus(now, observedAt time.Time) Status {
	status := Status{State: StateAvailable, Fresh: true, CollectedAt: now, ObservedAt: observedAt}
	if now.Sub(observedAt) > freshnessWindow {
		status.State = StateStale
		status.Fresh = false
		status.Reason = "last VPP telemetry observation exceeds freshness window"
	}
	return status
}

func normalizeComponentStatuses(components ComponentStatuses, status Status) ComponentStatuses {
	fallback := ComponentStatus{State: status.State, Reason: status.Reason}
	return ComponentStatuses{
		Interfaces:  normalizeComponentStatus(components.Interfaces, fallback),
		PolicyHits:  normalizeComponentStatus(components.PolicyHits, fallback),
		Neighbors:   normalizeComponentStatus(components.Neighbors, fallback),
		Connections: normalizeComponentStatus(components.Connections, fallback),
	}
}

func normalizeComponentStatus(component, fallback ComponentStatus) ComponentStatus {
	if component.State == "" {
		return fallback
	}
	return component
}

func (collector *Collector) unavailable(now time.Time, reason string) Snapshot {
	collector.prune(now)
	status := Status{State: StateUnavailable, Fresh: false, CollectedAt: now, Reason: reason}
	return Snapshot{
		Status:         status,
		Components:     normalizeComponentStatuses(ComponentStatuses{}, status),
		Totals:         unavailableTotals(collector.topology, reason),
		Groups:         unavailableGroups(collector.topology, reason),
		Reconciliation: unavailableReconciliation(),
		PolicyHits:     []PolicyHit{},
		OnlineUsers:    []OnlineUser{},
		TopConnections: []TopConnection{},
		History:        collector.history(),
	}
}
