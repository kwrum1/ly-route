package vpp

import (
	"fmt"
	"sort"
	"strings"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

func selectACLs(states []trafficpolicy.SecurityACL, request SnapshotRequest) ([]trafficpolicy.SecurityACL, error) {
	available := make(map[string]struct{}, len(states))
	for index := range states {
		states[index].ID = strings.TrimSpace(states[index].ID)
		if states[index].ID == "" {
			return nil, fmt.Errorf("%w: ACL ID is empty", ErrSnapshotIncomplete)
		}
		available[states[index].ID] = struct{}{}
	}
	if err := requireNamesAllowMissing(request.ACLs, available, "ACL", request.AllowMissing); err != nil {
		return nil, err
	}
	sort.Slice(states, func(left, right int) bool { return states[left].ID < states[right].ID })
	return states, nil
}

func selectQoS(states []flow.VPPObjectGroup, request SnapshotRequest) ([]flow.VPPObjectGroup, error) {
	available := make(map[string]struct{}, len(states))
	for _, state := range states {
		kind := strings.TrimSpace(state.Kind)
		if kind == "" {
			return nil, fmt.Errorf("%w: QoS group kind is empty", ErrSnapshotIncomplete)
		}
		available[kind] = struct{}{}
	}
	if err := requireNamesAllowMissing(request.QoS, available, "QoS group", request.AllowMissing); err != nil {
		return nil, err
	}
	sort.Slice(states, func(left, right int) bool { return states[left].Kind < states[right].Kind })
	return states, nil
}

func parseACLReadback(payload any) ([]trafficpolicy.SecurityACL, error) {
	payload = unwrapVPPCTLReadback(payload)
	switch value := payload.(type) {
	case ACLReadback:
		return value.ACLs, nil
	case []trafficpolicy.SecurityACL:
		return value, nil
	default:
		return nil, fmt.Errorf("%w: ACL readback payload has type %T", ErrSnapshotIncomplete, payload)
	}
}

func parseQoSReadback(payload any) ([]flow.VPPObjectGroup, error) {
	payload = unwrapVPPCTLReadback(payload)
	switch value := payload.(type) {
	case QoSReadback:
		return value.Groups, nil
	case []flow.VPPObjectGroup:
		return value, nil
	default:
		return nil, fmt.Errorf("%w: QoS readback payload has type %T", ErrSnapshotIncomplete, payload)
	}
}
