package httpapi

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	gatewayTelemetryRetention = 24 * time.Hour
	gatewayTelemetryFreshness = 10 * time.Minute
)

type logicalEgressHistory struct {
	counter  LogicalEgressCounter
	samples  []LogicalEgressSample
	lastSeen time.Time
}

type gatewayTelemetryState struct {
	series         map[string]*logicalEgressHistory
	connections    []GatewayConnection
	neighbors      []GatewayNeighbor
	lastSuccess    time.Time
	lastError      string
	nextCollection uint64
	lastCompletion uint64
}

func newGatewayTelemetryState() *gatewayTelemetryState {
	return &gatewayTelemetryState{series: map[string]*logicalEgressHistory{}}
}

func (server *Server) collectGatewayTelemetry(ctx context.Context) error {
	if server.gatewayTelemetry == nil {
		return fmt.Errorf("gateway telemetry collector is not configured")
	}
	server.runtimeMu.Lock()
	server.gatewayState.nextCollection++
	collection := server.gatewayState.nextCollection
	server.runtimeMu.Unlock()
	snapshot, err := server.gatewayTelemetry.Collect(ctx)
	now := server.now().UTC()
	server.runtimeMu.Lock()
	defer server.runtimeMu.Unlock()
	if collection < server.gatewayState.lastCompletion {
		return nil
	}
	server.gatewayState.lastCompletion = collection
	if err != nil {
		server.gatewayState.lastError = err.Error()
		return err
	}
	if snapshot.ObservedAt.IsZero() {
		snapshot.ObservedAt = now
	}
	if snapshot.ObservedAt.After(now) {
		err := fmt.Errorf("gateway telemetry snapshot %s is in the future", snapshot.ObservedAt.Format(time.RFC3339))
		server.gatewayState.lastError = err.Error()
		return err
	}
	if err := validateGatewayTelemetrySnapshot(snapshot); err != nil {
		server.gatewayState.lastError = err.Error()
		return err
	}
	if snapshot.ObservedAt.Before(server.gatewayState.lastSuccess) {
		err := fmt.Errorf("gateway telemetry snapshot %s predates last successful sample %s", snapshot.ObservedAt.Format(time.RFC3339), server.gatewayState.lastSuccess.Format(time.RFC3339))
		server.gatewayState.lastError = err.Error()
		return err
	}
	server.gatewayState.record(snapshot)
	return nil
}

func validateGatewayTelemetrySnapshot(snapshot GatewayTelemetrySnapshot) error {
	seen := make(map[string]struct{}, len(snapshot.LogicalEgresses))
	for _, counter := range snapshot.LogicalEgresses {
		id := strings.TrimSpace(counter.ID)
		if id == "" {
			return fmt.Errorf("logical egress identity is empty")
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("logical egress identity %q is duplicated", id)
		}
		seen[id] = struct{}{}
		if counter.Kind != LogicalEgressDirectWAN && counter.Kind != LogicalEgressWANGroup && counter.Kind != LogicalEgressProxy {
			return fmt.Errorf("logical egress %q has unsupported kind %q", id, counter.Kind)
		}
		if counter.DownloadBytes < 0 || counter.UploadBytes < 0 {
			return fmt.Errorf("logical egress %q has negative counters", id)
		}
		if counter.State != "" && counter.State != "available" && counter.State != "disabled" && counter.State != "unavailable" && counter.State != "failed" {
			return fmt.Errorf("logical egress %q has unsupported state %q", id, counter.State)
		}
	}
	return nil
}

func (state *gatewayTelemetryState) record(snapshot GatewayTelemetrySnapshot) {
	cutoff := snapshot.ObservedAt.Add(-gatewayTelemetryRetention)
	seen := make(map[string]struct{}, len(snapshot.LogicalEgresses))
	for _, counter := range snapshot.LogicalEgresses {
		seen[counter.ID] = struct{}{}
		history := state.series[counter.ID]
		if history == nil {
			history = &logicalEgressHistory{}
			state.series[counter.ID] = history
		}
		history.counter = counter
		history.lastSeen = snapshot.ObservedAt
		if counter.State == "" || counter.State == "available" {
			history.samples = appendLogicalEgressSample(history.samples, counter, snapshot.ObservedAt)
		}
		history.samples = retainedLogicalEgressSamples(history.samples, cutoff)
	}
	for id, history := range state.series {
		history.samples = retainedLogicalEgressSamples(history.samples, cutoff)
		if _, current := seen[id]; !current && history.lastSeen.Before(cutoff) {
			delete(state.series, id)
		}
	}
	trimLogicalEgressHistories(state.series)
	for _, connection := range snapshot.Connections {
		connection.ObservedAt = snapshot.ObservedAt
		state.connections = append(state.connections, connection)
	}
	state.connections = retainedGatewayConnections(state.connections, cutoff)
	state.neighbors = append([]GatewayNeighbor(nil), snapshot.Neighbors...)
	state.lastSuccess = snapshot.ObservedAt
	state.lastError = ""
}

func (server *Server) gatewayTrafficTrend(query TrafficTrendQuery) TrafficTrendResult {
	server.runtimeMu.Lock()
	defer server.runtimeMu.Unlock()
	now := server.now().UTC()
	result := TrafficTrendResult{Window: query.Window, Points: query.Points, SamplingIntervalSeconds: 300, State: "unavailable", Degraded: true, Series: TrafficTrendSets{LogicalEgresses: []LogicalEgressSeries{}}}
	retentionCutoff := now.Add(-gatewayTelemetryRetention)
	for id, history := range server.gatewayState.series {
		history.samples = retainedLogicalEgressSamples(history.samples, retentionCutoff)
		if history.lastSeen.Before(retentionCutoff) {
			delete(server.gatewayState.series, id)
		}
	}
	if len(server.gatewayState.series) == 0 {
		result.DegradedReason = nonEmpty(server.gatewayState.lastError, "gateway telemetry has no successful samples")
		return result
	}
	result.State = "available"
	result.Degraded = false
	if server.gatewayState.lastError != "" || now.Sub(server.gatewayState.lastSuccess) > gatewayTelemetryFreshness {
		result.State = "stale"
		result.Degraded = true
		result.DegradedReason = nonEmpty(server.gatewayState.lastError, "gateway telemetry samples are stale")
	}
	ids := make([]string, 0, len(server.gatewayState.series))
	for id := range server.gatewayState.series {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	windowCutoff := now.Add(-gatewayTrafficTrendWindow(query.Window))
	for _, id := range ids {
		history := server.gatewayState.series[id]
		samples := downsampleLogicalEgressSamples(retainedLogicalEgressSamples(history.samples, windowCutoff), query.Points)
		seriesState := logicalEgressSeriesState(history.counter, result.State)
		if result.State == "available" && history.lastSeen.Before(server.gatewayState.lastSuccess) {
			seriesState = "unavailable"
		}
		series := LogicalEgressSeries{ID: id, Name: history.counter.Name, Kind: history.counter.Kind, Health: history.counter.Health, UnderlayWANID: history.counter.UnderlayWANID, ActiveMember: history.counter.ActiveMember, State: seriesState, Fresh: !result.Degraded && seriesState == "available", Samples: samples}
		if len(samples) > 0 {
			series.LastSampleAt = samples[len(samples)-1].Timestamp
			latest := samples[len(samples)-1]
			if seriesState == "available" && latest.DownloadBPS != nil {
				result.Totals.DownloadBPS += *latest.DownloadBPS
			}
			if seriesState == "available" && latest.UploadBPS != nil {
				result.Totals.UploadBPS += *latest.UploadBPS
			}
		}
		result.Series.LogicalEgresses = append(result.Series.LogicalEgresses, series)
	}
	return result
}

func logicalEgressSeriesState(counter LogicalEgressCounter, collectionState string) string {
	if collectionState != "available" {
		return collectionState
	}
	if counter.State != "" {
		return counter.State
	}
	if health := strings.ToLower(strings.TrimSpace(counter.Health)); health != "" && health != "healthy" {
		return "failed"
	}
	return "available"
}
