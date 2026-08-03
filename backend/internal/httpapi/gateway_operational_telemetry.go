package httpapi

import (
	"context"
	"fmt"
	"sort"
	"time"

	controlapi "ly-route/backend/internal/api"
)

func (server *Server) gatewayTopConnections(ctx context.Context) ([]map[string]any, string, string) {
	err := server.collectGatewayTelemetry(ctx)
	server.runtimeMu.Lock()
	defer server.runtimeMu.Unlock()
	state, reason := server.gatewayCollectionState(err)
	latest := map[string]GatewayConnection{}
	cutoff := server.now().UTC().Add(-gatewayTelemetryRetention)
	for _, connection := range server.gatewayState.connections {
		if connection.ObservedAt.Before(cutoff) {
			continue
		}
		key := fmt.Sprintf("%s|%s|%s|%d|%d", connection.SourceIP, connection.DestinationIP, connection.Protocol, connection.SourcePort, connection.DestinationPort)
		current, exists := latest[key]
		if !exists || connection.ObservedAt.After(current.ObservedAt) || connection.Bytes > current.Bytes {
			latest[key] = connection
		}
	}
	connections := make([]GatewayConnection, 0, len(latest))
	for _, connection := range latest {
		connections = append(connections, connection)
	}
	sort.SliceStable(connections, func(left, right int) bool { return connections[left].Bytes > connections[right].Bytes })
	items := make([]map[string]any, 0, len(connections))
	for _, connection := range connections {
		items = append(items, map[string]any{
			"src_ip": connection.SourceIP, "dst_ip": connection.DestinationIP,
			"protocol": connection.Protocol, "src_port": connection.SourcePort,
			"dst_port": connection.DestinationPort, "bytes": connection.Bytes,
			"observed_at": connection.ObservedAt,
		})
	}
	return items, state, reason
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
	for _, user := range users {
		neighbor, found := byIP[firstStringField(user, "ip_address", "ip")]
		if !found {
			neighbor, found = byMAC[firstStringField(user, "mac", "hw_address")]
		}
		if !found {
			continue
		}
		user["last_traffic_time"] = neighbor.LastSeen.Format(time.RFC3339)
		user["rx_bytes"] = neighbor.DownloadBytes
		user["tx_bytes"] = neighbor.UploadBytes
		user["neighbor_state"] = "reachable"
		if input.neighborState == "stale" {
			user["neighbor_state"] = "stale"
		}
		user["traffic_activity_state"] = input.neighborState
	}
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
