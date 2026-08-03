package orchestrator

import (
	"context"
	"fmt"
	"net/netip"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ly-route/backend/internal/orchestrator"
	orchestratortelemetry "ly-route/backend/internal/orchestrator/telemetry"
)

type orchestratorTopologySnapshot interface {
	Snapshot(context.Context) (orchestrator.Topology, string, error)
}

type orchestratorTelemetryRuntime struct {
	repository orchestratorTopologySnapshot
	source     orchestratortelemetry.Source
	clock      orchestratortelemetry.Clock

	mu        sync.Mutex
	checksum  string
	collector *orchestratortelemetry.Collector
}

func newOrchestratorTelemetryRuntime(repository orchestratorTopologySnapshot, binary string, now func() time.Time) *orchestratorTelemetryRuntime {
	if now == nil {
		now = time.Now
	}
	source := vppctlOrchestratorTelemetrySource{binary: binary, now: now, run: runVPPCTLTelemetryCommand}
	return &orchestratorTelemetryRuntime{repository: repository, source: source, clock: orchestratortelemetry.ClockFunc(now)}
}

func (runtime *orchestratorTelemetryRuntime) Collect(ctx context.Context) orchestratortelemetry.Snapshot {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.repository == nil {
		return unavailableOrchestratorTelemetry(time.Now().UTC(), "orchestrator topology repository is not configured")
	}
	topology, checksum, err := runtime.repository.Snapshot(ctx)
	if err != nil {
		return unavailableOrchestratorTelemetry(time.Now().UTC(), "orchestrator topology is unavailable")
	}
	if runtime.collector == nil || runtime.checksum != checksum {
		runtime.collector = orchestratortelemetry.NewCollector(topology, runtime.source, runtime.clock)
		runtime.checksum = checksum
	}
	return runtime.collector.Collect(ctx)
}

type vppctlTelemetryCommand func(context.Context, string, ...string) (string, error)

type vppctlOrchestratorTelemetrySource struct {
	binary string
	now    func() time.Time
	run    vppctlTelemetryCommand
}

func (source vppctlOrchestratorTelemetrySource) Observe(ctx context.Context) (orchestratortelemetry.Observation, error) {
	binary := strings.TrimSpace(source.binary)
	if binary == "" {
		binary = "vppctl"
	}
	if source.run == nil {
		return orchestratortelemetry.Observation{}, fmt.Errorf("VPP telemetry command runner is not configured")
	}
	now := time.Now
	if source.now != nil {
		now = source.now
	}
	observedAt := now().UTC()
	interfaceOutput, err := source.run(ctx, binary, "show", "interface")
	if err != nil {
		return orchestratortelemetry.Observation{}, fmt.Errorf("read VPP interface telemetry: %w", err)
	}
	interfaces := parseOrchestratorInterfaceCounters(interfaceOutput)
	if len(interfaces) == 0 {
		return orchestratortelemetry.Observation{}, fmt.Errorf("read VPP interface telemetry: no LY-Route interfaces were observed")
	}
	components := orchestratortelemetry.ComponentStatuses{
		Interfaces: orchestratortelemetry.ComponentStatus{State: orchestratortelemetry.StateAvailable},
	}
	policyHits := []orchestratortelemetry.PolicyHitCounter{}
	connections := []orchestratortelemetry.ConnectionEntry{}
	groupHealth := []orchestratortelemetry.GroupHealthCounter{}
	orchestratorOutput, orchestratorErr := source.run(ctx, binary, "show", "ly-route", "orchestrator")
	if orchestratorErr != nil {
		components.PolicyHits = orchestratortelemetry.ComponentStatus{State: orchestratortelemetry.StateUnavailable, Reason: "LY-Route VPP orchestrator policy observation failed"}
		components.Connections = orchestratortelemetry.ComponentStatus{State: orchestratortelemetry.StateUnavailable, Reason: "LY-Route VPP orchestrator flow observation failed"}
	} else {
		policyHits, connections, groupHealth = parseTransparentOrchestratorTelemetry(orchestratorOutput, observedAt)
		components.PolicyHits = orchestratortelemetry.ComponentStatus{State: orchestratortelemetry.StateAvailable}
		components.Connections = orchestratortelemetry.ComponentStatus{State: orchestratortelemetry.StateAvailable}
	}
	neighbors := []orchestratortelemetry.NeighborEntry{}
	neighborOutput, neighborErr := source.run(ctx, binary, "show", "ip", "neighbors")
	if neighborErr != nil {
		components.Neighbors = orchestratortelemetry.ComponentStatus{State: orchestratortelemetry.StateUnavailable, Reason: "VPP neighbor observation failed"}
	} else {
		components.Neighbors = orchestratortelemetry.ComponentStatus{State: orchestratortelemetry.StateAvailable}
		neighbors = parseOrchestratorNeighbors(neighborOutput, observedAt)
	}
	return orchestratortelemetry.Observation{ObservedAt: observedAt, Interfaces: interfaces, PolicyHits: policyHits, Neighbors: neighbors, Connections: connections, GroupHealth: groupHealth, Components: components}, nil
}

func parseTransparentOrchestratorTelemetry(output string, observedAt time.Time) ([]orchestratortelemetry.PolicyHitCounter, []orchestratortelemetry.ConnectionEntry, []orchestratortelemetry.GroupHealthCounter) {
	hits := map[string]uint64{}
	connections := map[string]orchestratortelemetry.ConnectionEntry{}
	groupHealth := map[string]orchestratortelemetry.GroupHealthCounter{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 6 && fields[0] == "group-health" && fields[2] == "state" && fields[4] == "bypass-packets" {
			packets, err := strconv.ParseUint(fields[5], 10, 64)
			if err == nil && (fields[3] == "up" || fields[3] == "bypass") {
				groupHealth[fields[1]] = orchestratortelemetry.GroupHealthCounter{Name: fields[1], Bypass: fields[3] == "bypass", BypassPackets: packets}
			}
			continue
		}
		if len(fields) == 12 && fields[0] == "policy" && fields[2] == "group-position" && fields[4] == "sequence" && fields[6] == "action" && fields[8] == "packets" && fields[10] == "bytes" {
			packets, err := strconv.ParseUint(fields[9], 10, 64)
			if err == nil {
				hits[fields[1]] += packets
			}
			continue
		}
		if len(fields) != 21 || fields[0] != "flow" || fields[1] != "family" || fields[3] != "src" || fields[5] != "dst" || fields[7] != "proto" || fields[9] != "sport" || fields[11] != "dport" || fields[13] != "packets" || fields[15] != "bytes" || fields[17] != "age" || fields[19] != "groups" {
			continue
		}
		sourcePort, sourceErr := strconv.ParseUint(fields[10], 10, 16)
		destinationPort, destinationErr := strconv.ParseUint(fields[12], 10, 16)
		packets, packetErr := strconv.ParseUint(fields[14], 10, 64)
		bytes, bytesErr := strconv.ParseUint(fields[16], 10, 64)
		age, ageErr := strconv.ParseFloat(fields[18], 64)
		protocolNumber, protocolErr := strconv.ParseUint(fields[8], 10, 8)
		if sourceErr != nil || destinationErr != nil || packetErr != nil || bytesErr != nil || ageErr != nil || protocolErr != nil || age < 0 {
			continue
		}
		groups := []string{}
		if fields[20] != "-" {
			groups = strings.Split(fields[20], ",")
		}
		key := strings.Join([]string{fields[4], fields[6], fields[8], fields[10], fields[12], fields[20]}, "|")
		lastSeen := observedAt.Add(-time.Duration(age * float64(time.Second)))
		entry, found := connections[key]
		if !found {
			entry = orchestratortelemetry.ConnectionEntry{ID: key, SourceIP: fields[4], DestinationIP: fields[6], Protocol: transparentProtocolName(uint8(protocolNumber)), SourcePort: uint16(sourcePort), DestinationPort: uint16(destinationPort), Groups: groups, LastSeen: lastSeen}
		}
		entry.Packets += packets
		entry.Bytes += bytes
		if lastSeen.After(entry.LastSeen) {
			entry.LastSeen = lastSeen
		}
		connections[key] = entry
	}
	policyResult := make([]orchestratortelemetry.PolicyHitCounter, 0, len(hits))
	for policyID, count := range hits {
		policyResult = append(policyResult, orchestratortelemetry.PolicyHitCounter{PolicyID: policyID, Hits: count})
	}
	sort.Slice(policyResult, func(i, j int) bool { return policyResult[i].PolicyID < policyResult[j].PolicyID })
	connectionResult := make([]orchestratortelemetry.ConnectionEntry, 0, len(connections))
	for _, entry := range connections {
		connectionResult = append(connectionResult, entry)
	}
	sort.Slice(connectionResult, func(i, j int) bool { return connectionResult[i].ID < connectionResult[j].ID })
	healthResult := make([]orchestratortelemetry.GroupHealthCounter, 0, len(groupHealth))
	for _, item := range groupHealth {
		healthResult = append(healthResult, item)
	}
	sort.Slice(healthResult, func(i, j int) bool { return healthResult[i].Name < healthResult[j].Name })
	return policyResult, connectionResult, healthResult
}

func transparentProtocolName(protocol uint8) string {
	switch protocol {
	case 1:
		return "icmp"
	case 6:
		return "tcp"
	case 17:
		return "udp"
	case 58:
		return "icmpv6"
	default:
		return strconv.FormatUint(uint64(protocol), 10)
	}
}

func runVPPCTLTelemetryCommand(ctx context.Context, binary string, args ...string) (string, error) {
	output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s failed: %w: %s", binary, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func parseOrchestratorInterfaceCounters(output string) []orchestratortelemetry.InterfaceCounter {
	items := parseVPPInterfaceTelemetry(output)
	result := make([]orchestratortelemetry.InterfaceCounter, 0, len(items))
	for _, item := range items {
		name, _ := item["name"].(string)
		if strings.TrimSpace(name) == "" {
			continue
		}
		link, _ := item["link_state"].(string)
		result = append(result, orchestratortelemetry.InterfaceCounter{Name: name, RXBytes: unsignedMapValue(item["rx_bytes"]), TXBytes: unsignedMapValue(item["tx_bytes"]), LinkUp: strings.EqualFold(link, "up")})
	}
	return result
}

func parseOrchestratorNeighbors(output string, observedAt time.Time) []orchestratortelemetry.NeighborEntry {
	result := []orchestratortelemetry.NeighborEntry{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		age, err := strconv.ParseFloat(fields[0], 64)
		if err != nil || age < 0 {
			continue
		}
		if _, err := netip.ParseAddr(fields[1]); err != nil {
			continue
		}
		mac := fields[len(fields)-2]
		if !strings.Contains(mac, ":") {
			continue
		}
		interfaceName := strings.TrimPrefix(fields[len(fields)-1], "lyroute-")
		result = append(result, orchestratortelemetry.NeighborEntry{IP: fields[1], MAC: strings.ToLower(mac), Interface: interfaceName, LastSeen: observedAt.Add(-time.Duration(age * float64(time.Second)))})
	}
	return result
}

func unsignedMapValue(value any) uint64 {
	switch typed := value.(type) {
	case int:
		if typed >= 0 {
			return uint64(typed)
		}
	case int64:
		if typed >= 0 {
			return uint64(typed)
		}
	case uint64:
		return typed
	case float64:
		if typed >= 0 {
			return uint64(typed)
		}
	}
	return 0
}

func unavailableOrchestratorTelemetry(now time.Time, reason string) orchestratortelemetry.Snapshot {
	status := orchestratortelemetry.Status{State: orchestratortelemetry.StateUnavailable, Fresh: false, CollectedAt: now, Reason: reason}
	component := orchestratortelemetry.ComponentStatus{State: orchestratortelemetry.StateUnavailable, Reason: reason}
	return orchestratortelemetry.Snapshot{
		Status:     status,
		Components: orchestratortelemetry.ComponentStatuses{Interfaces: component, PolicyHits: component, Neighbors: component, Connections: component},
		Groups:     []orchestratortelemetry.GroupTraffic{}, PolicyHits: []orchestratortelemetry.PolicyHit{}, OnlineUsers: []orchestratortelemetry.OnlineUser{}, TopConnections: []orchestratortelemetry.TopConnection{},
		History: orchestratortelemetry.History{WindowSeconds: 86_400, Traffic: []orchestratortelemetry.TrafficPoint{}, Connections: []orchestratortelemetry.ConnectionPoint{}},
	}
}
