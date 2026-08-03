package telemetry

import (
	"fmt"
	"math"
	"slices"
	"time"

	"ly-route/backend/internal/orchestrator"
)

type interfaceTotals struct {
	rx uint64
	tx uint64
}

func collectTraffic(topology orchestrator.TopologyView, current Observation, previous *trafficBaseline, status Status) (BoundaryTotals, []GroupTraffic) {
	currentInterfaces := interfaceIndex(current.Interfaces)
	previousInterfaces := map[string]InterfaceCounter{}
	var elapsed time.Duration
	if previous != nil && current.ObservedAt.After(previous.observedAt) {
		previousInterfaces = interfaceIndex(previous.interfaces)
		elapsed = current.ObservedAt.Sub(previous.observedAt)
	}
	wanView, lanView := boundaryViews(topology)
	wan := collectEndpoint(wanView, true, currentInterfaces, previousInterfaces, elapsed, status.State)
	lan := collectEndpoint(lanView, false, currentInterfaces, previousInterfaces, elapsed, status.State)
	groups := make([]GroupTraffic, 0, len(topology.Groups))
	health := make(map[string]GroupHealthCounter, len(current.GroupHealth))
	for _, item := range current.GroupHealth {
		health[item.Name] = item
	}
	for _, group := range topology.Groups {
		groups = append(groups, collectGroup(group, health[group.Name], currentInterfaces, previousInterfaces, elapsed, status.State))
	}
	slices.SortFunc(groups, func(left, right GroupTraffic) int { return compare(left.Name, right.Name) })
	return BoundaryTotals{WAN: wan, LAN: lan}, groups
}

func collectEndpoint(view orchestrator.InterfaceView, wan bool, current, previous map[string]InterfaceCounter, elapsed time.Duration, state State) EndpointTraffic {
	members := logicalMembers(view)
	currentTotal, reason := sumInterfaces(members, current)
	endpoint := EndpointTraffic{ID: view.Name, State: state}
	if reason != "" {
		endpoint.State = StateUnavailable
		endpoint.Reason = reason
		endpoint.WANToLAN = unavailableCounter(reason)
		endpoint.LANToWAN = unavailableCounter(reason)
		return endpoint
	}
	previousTotal, previousReason := sumInterfaces(members, previous)
	baseline := elapsed > 0 && previousReason == ""
	if wan {
		endpoint.WANToLAN = measuredCounter(currentTotal.rx, previousTotal.rx, elapsed, baseline, state)
		endpoint.LANToWAN = measuredCounter(currentTotal.tx, previousTotal.tx, elapsed, baseline, state)
		return endpoint
	}
	endpoint.WANToLAN = measuredCounter(currentTotal.tx, previousTotal.tx, elapsed, baseline, state)
	endpoint.LANToWAN = measuredCounter(currentTotal.rx, previousTotal.rx, elapsed, baseline, state)
	return endpoint
}

func collectGroup(view orchestrator.GroupView, health GroupHealthCounter, current, previous map[string]InterfaceCounter, elapsed time.Duration, state State) GroupTraffic {
	group := GroupTraffic{Name: view.Name, Additive: false, State: state, Bypass: health.Bypass, BypassPackets: health.BypassPackets}
	lanName, wanName := directedMembers(view)
	lan, lanFound := current[lanName]
	wan, wanFound := current[wanName]
	switch {
	case !lanFound:
		group.Reason = fmt.Sprintf("group arm %q counter is unavailable", lanName)
	case !wanFound:
		group.Reason = fmt.Sprintf("group arm %q counter is unavailable", wanName)
	case !lan.LinkUp:
		group.Reason = fmt.Sprintf("group arm %q link is down", lanName)
	case !wan.LinkUp:
		group.Reason = fmt.Sprintf("group arm %q link is down", wanName)
	}
	if group.Reason != "" {
		group.Bypass = true
		group.State = StateUnavailable
		group.WANToLAN = unavailableCounter(group.Reason)
		group.LANToWAN = unavailableCounter(group.Reason)
		return group
	}
	previousLAN, previousLANFound := previous[lanName]
	previousWAN, previousWANFound := previous[wanName]
	// A packet leaves the appliance on the arm facing its destination, so RX on
	// that arm is the completed per-hop directional count.
	group.WANToLAN = measuredCounter(lan.RXBytes, previousLAN.RXBytes, elapsed, previousLANFound && previousLAN.LinkUp, state)
	group.LANToWAN = measuredCounter(wan.RXBytes, previousWAN.RXBytes, elapsed, previousWANFound && previousWAN.LinkUp, state)
	return group
}

func measuredCounter(current, previous uint64, elapsed time.Duration, baseline bool, state State) DirectionalCounter {
	counter := DirectionalCounter{Bytes: current, RateState: StateUnavailable, RateReason: "rate baseline is unavailable"}
	if !baseline || elapsed <= 0 {
		return counter
	}
	if current < previous {
		counter.RateReason = "counter reset since previous observation"
		return counter
	}
	counter.BytesPerSecond = float64(current-previous) / elapsed.Seconds()
	counter.RateState = state
	counter.RateReason = ""
	return counter
}

func interfaceIndex(items []InterfaceCounter) map[string]InterfaceCounter {
	result := make(map[string]InterfaceCounter, len(items))
	for _, item := range items {
		result[item.Name] = item
	}
	return result
}

func sumInterfaces(names []string, counters map[string]InterfaceCounter) (interfaceTotals, string) {
	var total interfaceTotals
	for _, name := range names {
		counter, exists := counters[name]
		if !exists {
			return interfaceTotals{}, fmt.Sprintf("logical boundary member %q counter is unavailable", name)
		}
		if !counter.LinkUp {
			return interfaceTotals{}, fmt.Sprintf("logical boundary member %q link is down", name)
		}
		total.rx += counter.RXBytes
		total.tx += counter.TXBytes
	}
	return total, ""
}

func logicalMembers(view orchestrator.InterfaceView) []string {
	if view.Bond == nil {
		return []string{view.Port}
	}
	return append([]string(nil), view.Bond.Members...)
}

func boundaryViews(topology orchestrator.TopologyView) (orchestrator.InterfaceView, orchestrator.InterfaceView) {
	var wan orchestrator.InterfaceView
	var lan orchestrator.InterfaceView
	for _, view := range topology.Interfaces {
		switch view.Role {
		case orchestrator.RoleWAN:
			wan = view
		case orchestrator.RoleLAN:
			lan = view
		}
	}
	return wan, lan
}

func directedMembers(group orchestrator.GroupView) (string, string) {
	var lan string
	var wan string
	for _, port := range group.Ports {
		switch port.Direction {
		case orchestrator.DirectionLANFacing:
			lan = port.Interface
		case orchestrator.DirectionWANFacing:
			wan = port.Interface
		}
	}
	return lan, wan
}

func reconcile(totals BoundaryTotals, state State) ChainReconciliation {
	return ChainReconciliation{
		TolerancePercent: 5,
		WANToLAN:         reconcileDirection(totals.WAN, totals.LAN, totals.WAN.WANToLAN.Bytes, totals.LAN.WANToLAN.Bytes, state),
		LANToWAN:         reconcileDirection(totals.WAN, totals.LAN, totals.WAN.LANToWAN.Bytes, totals.LAN.LANToWAN.Bytes, state),
	}
}

func reconcileDirection(wan, lan EndpointTraffic, wanBytes, lanBytes uint64, state State) ReconciliationResult {
	if wan.State == StateUnavailable || lan.State == StateUnavailable {
		return ReconciliationResult{State: StateUnavailable}
	}
	largest := math.Max(float64(wanBytes), float64(lanBytes))
	difference := 0.0
	if largest > 0 {
		difference = math.Abs(float64(wanBytes)-float64(lanBytes)) / largest * 100
	}
	return ReconciliationResult{State: state, WithinTolerance: difference <= 5, DifferencePercent: difference}
}

func unavailableCounter(reason string) DirectionalCounter {
	return DirectionalCounter{RateState: StateUnavailable, RateReason: reason}
}

func compare(left, right string) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}
