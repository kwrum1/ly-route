package orchestratorapi

import "ly-route/backend/internal/orchestrator"

type topologyDTO struct {
	SchemaVersion       int            `json:"schema_version"`
	ManagementInterface string         `json:"management_interface"`
	ManagementShared    bool           `json:"management_shared,omitempty"`
	Interfaces          []interfaceDTO `json:"interfaces"`
	Groups              []groupDTO     `json:"orchestration_groups"`
}

type interfaceDTO struct {
	Name string   `json:"name"`
	Role string   `json:"role"`
	Port string   `json:"port,omitempty"`
	Bond *bondDTO `json:"bond,omitempty"`
}

type bondDTO struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

type groupDTO struct {
	Name  string            `json:"name"`
	Ports []directedPortDTO `json:"ports"`
}

type directedPortDTO struct {
	Interface string   `json:"interface,omitempty"`
	Direction string   `json:"direction"`
	Bond      *bondDTO `json:"bond,omitempty"`
}

func (dto topologyDTO) input() orchestrator.TopologyInput {
	input := orchestrator.TopologyInput{
		SchemaVersion:       dto.SchemaVersion,
		ManagementInterface: dto.ManagementInterface,
		ManagementShared:    dto.ManagementShared,
		Interfaces:          make([]orchestrator.InterfaceInput, 0, len(dto.Interfaces)),
		Groups:              make([]orchestrator.GroupInput, 0, len(dto.Groups)),
	}
	for _, item := range dto.Interfaces {
		parsed := orchestrator.InterfaceInput{Name: item.Name, Role: orchestrator.Role(item.Role), Port: item.Port}
		if item.Bond != nil {
			parsed.Bond = &orchestrator.BondInput{Name: item.Bond.Name, Members: append([]string(nil), item.Bond.Members...)}
		}
		input.Interfaces = append(input.Interfaces, parsed)
	}
	for _, group := range dto.Groups {
		input.Groups = append(input.Groups, group.input())
	}
	return input
}

func (dto groupDTO) input() orchestrator.GroupInput {
	input := orchestrator.GroupInput{Name: dto.Name, Ports: make([]orchestrator.DirectedPortInput, 0, len(dto.Ports))}
	for _, port := range dto.Ports {
		parsed := orchestrator.DirectedPortInput{
			Interface: port.Interface,
			Direction: orchestrator.Direction(port.Direction),
		}
		if port.Bond != nil {
			parsed.Interface = ""
			parsed.Bond = &orchestrator.BondInput{Name: port.Bond.Name, Members: append([]string(nil), port.Bond.Members...)}
		}
		input.Ports = append(input.Ports, parsed)
	}
	return input
}
