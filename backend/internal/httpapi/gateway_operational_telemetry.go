package httpapi

import (
	"context"
	"fmt"
	"sort"
	"time"

	controlapi "ly-route/backend/internal/api"
)

func (server *Server) gatewayDashboardCounters() (int, float64) {
	server.runtimeMu.Lock()
	defer server.runtimeMu.Unlock()
	cutoff := server.now().UTC().Add(-gatewayConnectionRetention)
	connections := map[string]struct{}{}
	for _, connection := range server.gatewayState.connections {
		if connection.ObservedAt.Before(cutoff) {
			continue
		}
		key := fmt.Sprintf("%s|%s|%s|%d|%d", connection.SourceIP, connection.DestinationIP, connection.Protocol, connection.SourcePort, connection.DestinationPort)
		connections[key] = struct{}{}
	}
	throughput := float64(0)
	for _, history := range server.gatewayState.series {
		if len(history.samples) == 0 {
			continue
		}
		latest := history.samples[len(history.samples)-1]
		if latest.DownloadBPS != nil {
			throughput += *latest.DownloadBPS
		}
		if latest.UploadBPS != nil {
			throughput += *latest.UploadBPS
		}
	}
	return len(connections), throughput
}

func (server *Server) gatewayTopConnections(ctx context.Context) ([]map[string]any, string, string) {
	err := server.collectGatewayTelemetry(ctx)
	server.runtimeMu.Lock()
	defer server.runtimeMu.Unlock()
	state, reason := server.gatewayCollectionState(err)
	bySource := map[string]GatewayConnection{}
	cutoff := server.now().UTC().Add(-gatewayConnectionRetention)
	for _, connection := range server.gatewayState.connections {
		if connection.ObservedAt.Before(cutoff) {
			continue
		}
		current := bySource[connection.SourceIP]
		current.SourceIP = connection.SourceIP
		current.ConnectionCount += maxInt(connection.ConnectionCount, 1)
		current.Bytes += connection.Bytes
		if connection.ObservedAt.After(current.ObservedAt) {
			current.ObservedAt = connection.ObservedAt
		}
		bySource[connection.SourceIP] = current
	}
	connections := make([]GatewayConnection, 0, len(bySource))
	for _, connection := range bySource {
		connections = append(connections, connection)
	}
	sort.SliceStable(connections, func(left, right int) bool {
		return connections[left].ConnectionCount > connections[right].ConnectionCount
	})
	items := make([]map[string]any, 0, len(connections))
	for _, connection := range connections {
		connectionCount := connection.ConnectionCount
		if connectionCount <= 0 {
			connectionCount = 1
		}
		items = append(items, map[string]any{
			"src_ip": connection.SourceIP, "source_ip": connection.SourceIP,
			"dst_ip": connection.DestinationIP, "destination_ip": connection.DestinationIP,
			"protocol": connection.Protocol, "src_port": connection.SourcePort,
			"dst_port": connection.DestinationPort, "connection_count": connectionCount, "bytes": connection.Bytes,
			"observed_at": connection.ObservedAt,
		})
	}
	return items, state, reason
}

func (server *Server) gatewayActiveSessions(ctx context.Context) ([]map[string]any, string, string) {
	err := server.collectGatewayTelemetry(ctx)
	server.runtimeMu.Lock()
	defer server.runtimeMu.Unlock()
	state, reason := server.gatewayCollectionState(err)
	cutoff := server.now().UTC().Add(-gatewayConnectionRetention)
	items := make([]map[string]any, 0, len(server.gatewayState.connections))
	for _, connection := range server.gatewayState.connections {
		if connection.ObservedAt.Before(cutoff) || connection.ConnectionCount > 1 {
			continue
		}
		items = append(items, map[string]any{
			"src_ip": connection.SourceIP, "source_ip": connection.SourceIP, "dst_ip": connection.DestinationIP, "destination_ip": connection.DestinationIP,
			"translated_ip": connection.TranslatedIP, "protocol": connection.Protocol, "src_port": connection.SourcePort, "dst_port": connection.DestinationPort,
			"translated_port": connection.TranslatedPort, "bytes": connection.Bytes, "age_seconds": connection.AgeSeconds, "session_kind": connection.SessionKind, "observed_at": connection.ObservedAt,
		})
	}
	return items, state, reason
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func (server *Server) gatewayNeighborSnapshot(ctx context.Context) ([]GatewayNeighbor, string, string) {
	err := server.collectGatewayTelemetry(ctx)
	server.runtimeMu.Lock()
	defer server.runtimeMu.Unlock()
	state, reason := server.gatewayCollectionState(err)
	return append([]GatewayNeighbor(nil), server.gatewayState.neighbors...), state, reason
}

func (server *Server) gatewayCollectionState(collectionErr error) (string, string) {
	if server.gatewayState.lastSuccess.IsZero() {
		return "unavailable", nonEmpty(server.gatewayState.lastError, "gateway telemetry has no successful samples")
	}
	if collectionErr != nil || server.now().UTC().Sub(server.gatewayState.lastSuccess) > gatewayTelemetryFreshness {
		return "stale", nonEmpty(server.gatewayState.lastError, "gateway telemetry samples are stale")
	}
	return "available", ""
}

type onlineUserTelemetryInput struct {
	now            time.Time
	leases         []map[string]any
	neighbors      []GatewayNeighbor
	runtimeState   string
	leaseDegraded  bool
	neighborState  string
	neighborReason string
}

func normalizeOnlineUsersWithNeighbors(input onlineUserTelemetryInput) ([]map[string]any, controlapi.CapabilityState) {
	byIP := make(map[string]GatewayNeighbor, len(input.neighbors))
	byMAC := make(map[string]GatewayNeighbor, len(input.neighbors))
	for _, neighbor := range input.neighbors {
		byIP[neighbor.IP] = neighbor
		byMAC[neighbor.MAC] = neighbor
	}
	users, capability := normalizeOnlineUsers(input.leases, input.runtimeState, input.leaseDegraded)
	matchedIPs := map[string]struct{}{}
	matchedMACs := map[string]struct{}{}
	for _, user := range users {
		ip := firstStringField(user, "ip_address", "ip")
		mac := firstStringField(user, "mac", "hw_address")
		neighbor, found := byIP[ip]
		if !found {
			neighbor, found = byMAC[mac]
		}
		if !found {
			continue
		}
		matchedIPs[neighbor.IP] = struct{}{}
		matchedMACs[neighbor.MAC] = struct{}{}
		user["last_traffic_time"] = neighbor.LastSeen.Format(time.RFC3339)
		if !neighbor.FirstSeen.IsZero() {
			user["online_since"] = neighbor.FirstSeen.Format(time.RFC3339)
			user["online_duration_seconds"] = int64(input.now.Sub(neighbor.FirstSeen).Seconds())
		}
		user["rx_bytes"] = neighbor.DownloadBytes
		user["tx_bytes"] = neighbor.UploadBytes
		user["neighbor_state"] = "reachable"
		if input.neighborState == "stale" {
			user["neighbor_state"] = "stale"
		}
		user["traffic_activity_state"] = input.neighborState
	}
	for _, neighbor := range input.neighbors {
		if _, found := matchedIPs[neighbor.IP]; found {
			continue
		}
		if _, found := matchedMACs[neighbor.MAC]; found {
			continue
		}
		neighborState := "reachable"
		if input.neighborState == "stale" {
			neighborState = "stale"
		}
		users = append(users, map[string]any{
			"ip": neighbor.IP, "ip_address": neighbor.IP, "mac": neighbor.MAC,
			"online_status": "online", "last_traffic_time": neighbor.LastSeen.Format(time.RFC3339),
			"online_since": neighbor.FirstSeen.Format(time.RFC3339), "online_duration_seconds": int64(input.now.Sub(neighbor.FirstSeen).Seconds()),
			"rx_bps": 0, "tx_bps": 0, "rx_bytes": neighbor.DownloadBytes, "tx_bytes": neighbor.UploadBytes,
			"neighbor_state": neighborState, "traffic_activity_state": input.neighborState,
			"runtime_state": input.runtimeState,
		})
	}
	sort.SliceStable(users, func(left, right int) bool {
		return firstStringField(users[left], "ip_address", "ip") < firstStringField(users[right], "ip_address", "ip")
	})
	if len(input.neighbors) > 0 {
		capability = controlapi.CapabilityState{Name: "gateway_neighbor_traffic", Available: input.neighborState == "available", State: controlapi.CapabilityAvailable}
		if input.neighborState != "available" {
			capability.State = controlapi.CapabilityDegraded
			capability.Reason = nonEmpty(input.neighborReason, "gateway neighbor telemetry is stale")
		}
	} else if !capability.Available && input.neighborState != "available" && input.neighborReason != "" {
		capability = controlapi.CapabilityState{Name: "gateway_neighbor_traffic", Available: false, State: controlapi.CapabilityDegraded, Reason: input.neighborReason}
		for _, user := range users {
			user["traffic_activity_state"] = input.neighborState
			user["traffic_activity_reason"] = input.neighborReason
		}
	}
	return users, capability
}
