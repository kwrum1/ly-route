package vpp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

func parseRoutePolicyReadback(payload any) ([]trafficpolicy.RoutePolicy, error) {
	payload = unwrapVPPCTLReadback(payload)
	switch value := payload.(type) {
	case RoutePolicyReadback:
		return value.Policies, nil
	case []trafficpolicy.RoutePolicy:
		return value, nil
	default:
		return nil, fmt.Errorf("%w: route policy readback payload has type %T", ErrSnapshotIncomplete, payload)
	}
}

func parseWANGroupReadback(payload any) ([]trafficpolicy.WANGroup, error) {
	payload = unwrapVPPCTLReadback(payload)
	switch value := payload.(type) {
	case WANGroupReadback:
		return value.Groups, nil
	case []trafficpolicy.WANGroup:
		return value, nil
	default:
		return nil, fmt.Errorf("%w: WAN group readback payload has type %T", ErrSnapshotIncomplete, payload)
	}
}

func selectRoutePolicies(states []trafficpolicy.RoutePolicy, request SnapshotRequest) ([]trafficpolicy.RoutePolicy, error) {
	available := make(map[string]struct{}, len(states))
	for index := range states {
		states[index].ID = strings.TrimSpace(states[index].ID)
		if states[index].ID == "" {
			return nil, fmt.Errorf("%w: route policy ID is empty", ErrSnapshotIncomplete)
		}
		available[states[index].ID] = struct{}{}
	}
	if !request.AllowMissing {
		if err := requireNames(request.RoutePolicies, available, "route policy"); err != nil {
			return nil, err
		}
	}
	sort.Slice(states, func(left, right int) bool { return states[left].ID < states[right].ID })
	return states, nil
}

func selectWANGroups(states []trafficpolicy.WANGroup, request SnapshotRequest) ([]trafficpolicy.WANGroup, error) {
	available := make(map[string]struct{}, len(states))
	for index := range states {
		states[index].ID = strings.TrimSpace(states[index].ID)
		if states[index].ID == "" {
			return nil, fmt.Errorf("%w: WAN group ID is empty", ErrSnapshotIncomplete)
		}
		available[states[index].ID] = struct{}{}
	}
	if !request.AllowMissing {
		if err := requireNames(request.WANGroups, available, "WAN group"); err != nil {
			return nil, err
		}
	}
	sort.Slice(states, func(left, right int) bool { return states[left].ID < states[right].ID })
	return states, nil
}

func snapshotHash(snapshot Snapshot) (string, error) {
	value, err := json.Marshal(struct {
		Interfaces    []InterfaceState            `json:"interfaces"`
		Bonds         []BondState                 `json:"bonds"`
		RoutePolicies []trafficpolicy.RoutePolicy `json:"route_policies"`
		WANGroups     []trafficpolicy.WANGroup    `json:"wan_groups"`
		ACLs          []trafficpolicy.SecurityACL `json:"acls"`
		QoS           []flow.VPPObjectGroup       `json:"qos"`
		NAT           nat.CompiledConfig          `json:"nat"`
	}{
		Interfaces: canonicalSnapshotSlice(snapshot.Interfaces), Bonds: canonicalSnapshotSlice(snapshot.Bonds),
		RoutePolicies: canonicalSnapshotSlice(snapshot.RoutePolicies), WANGroups: canonicalSnapshotSlice(snapshot.WANGroups),
		ACLs: canonicalSnapshotSlice(snapshot.ACLs), QoS: canonicalSnapshotSlice(snapshot.QoS),
		NAT: nat.CompiledConfig{StaticMappings: canonicalSnapshotSlice(snapshot.NAT.StaticMappings), PortMappings: canonicalSnapshotSlice(snapshot.NAT.PortMappings)},
	})
	if err != nil {
		return "", fmt.Errorf("hash snapshot: %w", err)
	}
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalSnapshotSlice[T any](items []T) []T {
	if len(items) == 0 {
		return nil
	}
	return items
}

func verifyRouteWANGroupDeletes(snapshot Snapshot, plan RouteWANGroupPlan) error {
	for _, id := range plan.DeleteWANGroups {
		for _, group := range snapshot.WANGroups {
			if group.ID == id {
				return fmt.Errorf("%w: deleted WAN group %q is still present", ErrSnapshotIncomplete, id)
			}
		}
	}
	for _, id := range plan.DeleteRoutes {
		for _, route := range snapshot.RoutePolicies {
			if route.ID == id {
				return fmt.Errorf("%w: deleted route policy %q is still present", ErrSnapshotIncomplete, id)
			}
		}
	}
	return nil
}
