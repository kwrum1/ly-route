package telemetry

import "ly-route/backend/internal/orchestrator"

func unavailableTotals(topology orchestrator.TopologyView, reason string) BoundaryTotals {
	wan, lan := boundaryViews(topology)
	return BoundaryTotals{
		WAN: unavailableEndpoint(wan.Name, reason),
		LAN: unavailableEndpoint(lan.Name, reason),
	}
}

func unavailableEndpoint(id, reason string) EndpointTraffic {
	return EndpointTraffic{
		ID: id, State: StateUnavailable, Reason: reason,
		WANToLAN: unavailableCounter(reason), LANToWAN: unavailableCounter(reason),
	}
}

func unavailableGroups(topology orchestrator.TopologyView, reason string) []GroupTraffic {
	groups := make([]GroupTraffic, 0, len(topology.Groups))
	for _, view := range topology.Groups {
		groups = append(groups, GroupTraffic{
			Name: view.Name, Additive: false, State: StateUnavailable, Reason: reason,
			WANToLAN: unavailableCounter(reason), LANToWAN: unavailableCounter(reason),
		})
	}
	return groups
}

func unavailableReconciliation() ChainReconciliation {
	return ChainReconciliation{
		TolerancePercent: 5,
		WANToLAN:         ReconciliationResult{State: StateUnavailable},
		LANToWAN:         ReconciliationResult{State: StateUnavailable},
	}
}
