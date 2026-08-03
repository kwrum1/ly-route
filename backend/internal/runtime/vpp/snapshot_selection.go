package vpp

import (
	"fmt"
	"sort"
	"strings"
)

func parseInterfaceReadback(payload any) ([]InterfaceState, error) {
	payload = unwrapVPPCTLReadback(payload)
	switch value := payload.(type) {
	case InterfaceReadback:
		return value.Interfaces, nil
	case []InterfaceState:
		return value, nil
	default:
		return nil, fmt.Errorf("%w: interface readback payload has type %T", ErrSnapshotIncomplete, payload)
	}
}

func parseBondReadback(payload any) ([]BondState, error) {
	payload = unwrapVPPCTLReadback(payload)
	switch value := payload.(type) {
	case BondReadback:
		return value.Bonds, nil
	case []BondState:
		return value, nil
	default:
		return nil, fmt.Errorf("%w: bond readback payload has type %T", ErrSnapshotIncomplete, payload)
	}
}

func selectInterfaces(states []InterfaceState, request SnapshotRequest) ([]InterfaceState, error) {
	management := strings.TrimSpace(request.ManagementInterface)
	selected := make([]InterfaceState, 0, len(states))
	available := make(map[string]struct{}, len(states))
	for _, state := range states {
		state.Name = strings.TrimSpace(state.Name)
		if state.Name == "" {
			return nil, fmt.Errorf("%w: interface name is empty", ErrSnapshotIncomplete)
		}
		if state.Name == management {
			continue
		}
		available[state.Name] = struct{}{}
		selected = append(selected, state)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("%w: no interface state returned", ErrSnapshotIncomplete)
	}
	if err := requireNames(request.Interfaces, available, "interface"); err != nil {
		return nil, err
	}
	sort.Slice(selected, func(left, right int) bool { return selected[left].Name < selected[right].Name })
	return selected, nil
}

func selectBonds(states []BondState, request SnapshotRequest) ([]BondState, error) {
	selected := make([]BondState, 0, len(states))
	available := make(map[string]struct{}, len(states))
	for _, state := range states {
		state.Name = strings.TrimSpace(state.Name)
		if state.Name == "" {
			return nil, fmt.Errorf("%w: bond name is empty", ErrSnapshotIncomplete)
		}
		available[state.Name] = struct{}{}
		selected = append(selected, state)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("%w: no bond state returned", ErrSnapshotIncomplete)
	}
	if err := requireNames(request.Bonds, available, "bond"); err != nil {
		return nil, err
	}
	sort.Slice(selected, func(left, right int) bool { return selected[left].Name < selected[right].Name })
	return selected, nil
}

func requireNames(names []string, available map[string]struct{}, kind string) error {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("%w: %s name is empty", ErrSnapshotIncomplete, kind)
		}
		if _, ok := available[name]; !ok {
			return fmt.Errorf("%w: %s %q was not returned", ErrSnapshotIncomplete, kind, name)
		}
	}
	return nil
}
