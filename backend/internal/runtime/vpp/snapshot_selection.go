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
	selected := make([]InterfaceState, 0, len(request.Interfaces))
	available := make(map[string]struct{}, len(states))
	byName := make(map[string]InterfaceState, len(states))
	for _, state := range states {
		state.Name = strings.TrimSpace(state.Name)
		if state.Name == "" {
			return nil, fmt.Errorf("%w: interface name is empty", ErrSnapshotIncomplete)
		}
		if state.Name == management {
			continue
		}
		available[state.Name] = struct{}{}
		byName[state.Name] = state
		if len(request.Interfaces) == 0 && len(request.AbsentInterfaces) == 0 {
			selected = append(selected, state)
		}
	}
	for _, name := range request.AbsentInterfaces {
		state, found := byName[strings.TrimSpace(name)]
		if found && (state.AdminState != "down" || len(state.Addresses) > 0) {
			return nil, fmt.Errorf("%w: deleted interface %q still has active configuration", ErrSnapshotIncomplete, name)
		}
	}
	if err := requireNamesAllowMissing(request.Interfaces, available, "interface", request.AllowMissing); err != nil {
		return nil, err
	}
	for _, name := range request.Interfaces {
		if state, found := byName[strings.TrimSpace(name)]; found {
			selected = append(selected, state)
		}
	}
	if len(selected) == 0 && (len(request.Interfaces) > 0 || len(request.AbsentInterfaces) == 0) && !request.AllowMissing {
		return nil, fmt.Errorf("%w: no interface state returned", ErrSnapshotIncomplete)
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
	if len(selected) == 0 && len(request.Bonds) > 0 {
		return nil, fmt.Errorf("%w: no bond state returned", ErrSnapshotIncomplete)
	}
	if err := requireNames(request.Bonds, available, "bond"); err != nil {
		return nil, err
	}
	sort.Slice(selected, func(left, right int) bool { return selected[left].Name < selected[right].Name })
	return selected, nil
}

func requireNames(names []string, available map[string]struct{}, kind string) error {
	return requireNamesAllowMissing(names, available, kind, false)
}

func requireNamesAllowMissing(names []string, available map[string]struct{}, kind string, allowMissing bool) error {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("%w: %s name is empty", ErrSnapshotIncomplete, kind)
		}
		if _, ok := available[name]; !ok {
			if allowMissing {
				continue
			}
			return fmt.Errorf("%w: %s %q was not returned", ErrSnapshotIncomplete, kind, name)
		}
	}
	return nil
}
