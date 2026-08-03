package vpp

import (
	"fmt"

	"ly-route/backend/internal/runtime/nat"
)

func deleteNATStaticCommands(mapping nat.StaticMapping) []string {
	return []string{fmt.Sprintf("nat44 add static mapping local %s external %s del", mapping.InternalAddress, mapping.ExternalAddress), "show nat44 static mappings", "show nat44 sessions"}
}

func deleteNATPortCommands(mapping nat.PortMapping) []string {
	return []string{fmt.Sprintf("nat44 add static mapping %s local %s %d external %s %d del", mapping.Protocol, mapping.InternalHost, mapping.InternalPort, mapping.ExternalAddress, mapping.ExternalPort), "show nat44 static mappings", "show nat44 sessions"}
}

func staticMappingByID(mappings []nat.StaticMapping, id string) (nat.StaticMapping, bool) {
	for _, mapping := range mappings {
		if mapping.ID == id {
			return mapping, true
		}
	}
	return nat.StaticMapping{}, false
}

func portMappingByID(mappings []nat.PortMapping, id string) (nat.PortMapping, bool) {
	for _, mapping := range mappings {
		if mapping.ID == id {
			return mapping, true
		}
	}
	return nat.PortMapping{}, false
}

func appendMappingsByID[T nat.StaticMapping | nat.PortMapping](existing []T, ids []string, prior []T) []T {
	result := append([]T(nil), existing...)
	seen := make(map[string]struct{}, len(result))
	for _, mapping := range result {
		seen[mappingID(mapping)] = struct{}{}
	}
	for _, id := range ids {
		if _, found := seen[id]; found {
			continue
		}
		for _, mapping := range prior {
			if mappingID(mapping) == id {
				result = append(result, mapping)
				seen[id] = struct{}{}
				break
			}
		}
	}
	return result
}

func appendUniqueMappings[T nat.StaticMapping | nat.PortMapping](mappings, additions []T) []T {
	seen := make(map[string]struct{}, len(mappings)+len(additions))
	for _, mapping := range mappings {
		seen[mappingID(mapping)] = struct{}{}
	}
	for _, mapping := range additions {
		if _, found := seen[mappingID(mapping)]; found {
			continue
		}
		seen[mappingID(mapping)] = struct{}{}
		mappings = append(mappings, mapping)
	}
	return mappings
}

func mappingID[T nat.StaticMapping | nat.PortMapping](mapping T) string {
	switch value := any(mapping).(type) {
	case nat.StaticMapping:
		return value.ID
	case nat.PortMapping:
		return value.ID
	default:
		return ""
	}
}

func withoutIDs(values, removed []string) []string {
	blocked := make(map[string]struct{}, len(removed))
	for _, value := range removed {
		blocked[value] = struct{}{}
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, found := blocked[value]; !found {
			result = append(result, value)
		}
	}
	return result
}
