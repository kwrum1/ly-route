package orchestrator

const SchemaVersion = 1

type TopologyInput struct {
	SchemaVersion       int
	ManagementInterface string
	ManagementShared    bool
	Interfaces          []InterfaceInput
	Groups              []GroupInput
}

type InterfaceInput struct {
	Name string
	Role Role
	Port string
	Bond *BondInput
}

type BondInput struct {
	Name    string
	Members []string
}

type GroupInput struct {
	Name  string
	Ports []DirectedPortInput
}

type DirectedPortInput struct {
	Interface string
	Direction Direction
	Bond      *BondInput
}
