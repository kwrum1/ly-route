package vpp

import (
	"fmt"
	"strings"

	"ly-route/backend/internal/runtime/nat"
)

func selectNAT44(readback NAT44Readback, request SnapshotRequest) ([]nat.StaticMapping, []nat.PortMapping, error) {
	static, err := selectNAT44StaticMappings(readback.StaticMappings, request.NATStaticMappings, request.AllowMissing)
	if err != nil {
		return nil, nil, err
	}
	ports, err := selectNAT44PortMappings(readback.PortMappings, request.NATPortMappings, request.AllowMissing)
	return static, ports, err
}

func selectNAT44StaticMappings(mappings []nat.StaticMapping, requested []string, allowMissing bool) ([]nat.StaticMapping, error) {
	available := make(map[string]struct{}, len(mappings))
	for _, mapping := range mappings {
		id := strings.TrimSpace(mapping.ID)
		if id == "" {
			return nil, fmt.Errorf("%w: NAT44 static mapping ID is empty", ErrSnapshotIncomplete)
		}
		available[id] = struct{}{}
	}
	if err := requireNamesAllowMissing(requested, available, "NAT44 static mapping", allowMissing); err != nil {
		return nil, err
	}
	return mappings, nil
}

func selectNAT44PortMappings(mappings []nat.PortMapping, requested []string, allowMissing bool) ([]nat.PortMapping, error) {
	available := make(map[string]struct{}, len(mappings))
	for _, mapping := range mappings {
		id := strings.TrimSpace(mapping.ID)
		if id == "" {
			return nil, fmt.Errorf("%w: NAT44 port mapping ID is empty", ErrSnapshotIncomplete)
		}
		available[id] = struct{}{}
	}
	if err := requireNamesAllowMissing(requested, available, "NAT44 port mapping", allowMissing); err != nil {
		return nil, err
	}
	return mappings, nil
}
