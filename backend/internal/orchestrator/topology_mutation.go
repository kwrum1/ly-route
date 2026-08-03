package orchestrator

import "fmt"

func AddTopologyGroup(topology Topology, group Group) (Topology, error) {
	groups := topology.groupValues()
	for _, current := range groups {
		if current.Name() == group.Name() {
			return Topology{}, fmt.Errorf("%w: %q", ErrDuplicateGroup, group.Name())
		}
	}
	return topology.withGroups(append(groups, group))
}

func ReplaceTopologyGroup(topology Topology, name string, group Group) (Topology, error) {
	groups := topology.groupValues()
	found := false
	for index, current := range groups {
		if current.Name() == name {
			groups[index] = group
			found = true
		}
	}
	if !found {
		return Topology{}, fmt.Errorf("%w: %q", ErrGroupNotFound, name)
	}
	return topology.withGroups(groups)
}

func RemoveTopologyGroup(topology Topology, name string) (Topology, error) {
	groups := topology.groupValues()
	filtered := make([]Group, 0, len(groups))
	for _, group := range groups {
		if group.Name() != name {
			filtered = append(filtered, group)
		}
	}
	if len(filtered) == len(groups) {
		return Topology{}, fmt.Errorf("%w: %q", ErrGroupNotFound, name)
	}
	return topology.withGroups(filtered)
}
