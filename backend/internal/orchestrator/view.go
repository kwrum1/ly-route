package orchestrator

import "slices"

type TopologyView struct {
	SchemaVersion       int             `json:"schema_version"`
	ManagementInterface string          `json:"management_interface"`
	ManagementShared    bool            `json:"management_shared,omitempty"`
	Interfaces          []InterfaceView `json:"interfaces"`
	Groups              []GroupView     `json:"orchestration_groups"`
}

type InterfaceView struct {
	Name string    `json:"name"`
	Role Role      `json:"role"`
	Port string    `json:"port,omitempty"`
	Bond *BondView `json:"bond,omitempty"`
}

type BondView struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

type GroupView struct {
	Name  string             `json:"name"`
	Ports []DirectedPortView `json:"ports"`
}

type DirectedPortView struct {
	Interface string    `json:"interface,omitempty"`
	Direction Direction `json:"direction"`
	Bond      *BondView `json:"bond,omitempty"`
}

func (topology Topology) View() TopologyView {
	groups := append([]Group(nil), topology.groups...)
	slices.SortFunc(groups, func(left, right Group) int {
		return compareStrings(left.Name(), right.Name())
	})
	groupViews := make([]GroupView, 0, len(groups))
	for _, group := range groups {
		groupViews = append(groupViews, group.View())
	}
	return TopologyView{
		SchemaVersion:       SchemaVersion,
		ManagementInterface: topology.management.String(),
		ManagementShared:    topology.managementShared,
		Interfaces:          []InterfaceView{topology.lan.view(), topology.wan.view()},
		Groups:              groupViews,
	}
}

func (logical LogicalInterface) view() InterfaceView {
	view := InterfaceView{Name: logical.name.String(), Role: logical.role}
	if !logical.hasBond {
		view.Port = logical.port.String()
		return view
	}
	members := make([]string, 0, len(logical.bond.members))
	for _, member := range logical.bond.members {
		members = append(members, member.String())
	}
	slices.Sort(members)
	view.Bond = &BondView{Name: logical.bond.name.String(), Members: members}
	return view
}

func (group Group) View() GroupView {
	ports := make([]DirectedPortView, 0, len(group.ports))
	for _, port := range group.ports {
		view := DirectedPortView{Interface: port.interfaceName.String(), Direction: port.direction}
		if port.hasBond {
			members := make([]string, 0, len(port.bond.members))
			for _, member := range port.bond.members {
				members = append(members, member.String())
			}
			slices.Sort(members)
			view.Bond = &BondView{Name: port.bond.name.String(), Members: members}
		}
		ports = append(ports, view)
	}
	slices.SortFunc(ports, func(left, right DirectedPortView) int {
		return compareStrings(string(left.Direction), string(right.Direction))
	})
	return GroupView{Name: group.Name(), Ports: ports}
}

func (view TopologyView) input() TopologyInput {
	input := TopologyInput{SchemaVersion: view.SchemaVersion, ManagementInterface: view.ManagementInterface, ManagementShared: view.ManagementShared}
	input.Interfaces = make([]InterfaceInput, 0, len(view.Interfaces))
	for _, item := range view.Interfaces {
		parsed := InterfaceInput{Name: item.Name, Role: item.Role, Port: item.Port}
		if item.Bond != nil {
			parsed.Bond = &BondInput{Name: item.Bond.Name, Members: append([]string(nil), item.Bond.Members...)}
		}
		input.Interfaces = append(input.Interfaces, parsed)
	}
	input.Groups = make([]GroupInput, 0, len(view.Groups))
	for _, group := range view.Groups {
		input.Groups = append(input.Groups, group.input())
	}
	return input
}

func (view GroupView) input() GroupInput {
	input := GroupInput{Name: view.Name, Ports: make([]DirectedPortInput, 0, len(view.Ports))}
	for _, port := range view.Ports {
		parsed := DirectedPortInput{Interface: port.Interface, Direction: port.Direction}
		if port.Bond != nil {
			parsed.Interface = ""
			parsed.Bond = &BondInput{Name: port.Bond.Name, Members: append([]string(nil), port.Bond.Members...)}
		}
		input.Ports = append(input.Ports, parsed)
	}
	return input
}

func compareStrings(left, right string) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}
