package telemetry

import (
	"slices"
	"time"
)

func collectUsers(entries []NeighborEntry, now time.Time) ([]OnlineUser, int) {
	users := make([]OnlineUser, 0, len(entries))
	expired := 0
	for _, entry := range entries {
		if now.Sub(entry.LastSeen) > idleWindow {
			expired++
			continue
		}
		users = append(users, OnlineUser{
			IP: entry.IP, MAC: entry.MAC, Interface: entry.Interface,
			RXBytes: entry.RXBytes, TXBytes: entry.TXBytes, LastSeen: entry.LastSeen.UTC(),
		})
	}
	slices.SortFunc(users, func(left, right OnlineUser) int { return compare(left.IP, right.IP) })
	return users, expired
}

func collectConnections(entries []ConnectionEntry, now time.Time) ([]TopConnection, int) {
	connections := make([]TopConnection, 0, len(entries))
	expired := 0
	for _, entry := range entries {
		if now.Sub(entry.LastSeen) > idleWindow {
			expired++
			continue
		}
		candidate := TopConnection{
			ID: entry.ID, SourceIP: entry.SourceIP, DestinationIP: entry.DestinationIP,
			Protocol: entry.Protocol, SourcePort: entry.SourcePort, DestinationPort: entry.DestinationPort,
			Bytes: entry.Bytes, Packets: entry.Packets, LastSeen: entry.LastSeen.UTC(), Groups: append([]string(nil), entry.Groups...),
		}
		if len(connections) < topLimit {
			connections = append(connections, candidate)
			continue
		}
		worst := 0
		for index := 1; index < len(connections); index++ {
			if connectionBefore(connections[worst], connections[index]) {
				worst = index
			}
		}
		if connectionBefore(candidate, connections[worst]) {
			connections[worst] = candidate
		}
	}
	slices.SortFunc(connections, func(left, right TopConnection) int {
		if connectionBefore(left, right) {
			return -1
		}
		if connectionBefore(right, left) {
			return 1
		}
		return 0
	})
	return connections, expired
}

func connectionBefore(left, right TopConnection) bool {
	if left.Bytes != right.Bytes {
		return left.Bytes > right.Bytes
	}
	return left.ID < right.ID
}

func collectPolicyHits(counters []PolicyHitCounter, observedAt time.Time, state State) []PolicyHit {
	hits := make([]PolicyHit, 0, len(counters))
	for _, counter := range counters {
		hits = append(hits, PolicyHit{PolicyID: counter.PolicyID, Hits: counter.Hits, State: state, ObservedAt: observedAt})
	}
	slices.SortFunc(hits, func(left, right PolicyHit) int { return compare(left.PolicyID, right.PolicyID) })
	return hits
}
