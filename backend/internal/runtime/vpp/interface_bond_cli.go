package vpp

import (
	"fmt"
	"strconv"
	"strings"
)

func createBondCommands(bond BondState) []string {
	bondID, createdName := vppBondIdentity(bond.Name)
	mode := strings.TrimSpace(bond.Mode)
	if mode == "802.3ad" {
		mode = "lacp"
	}
	commands := []string{fmt.Sprintf("create bond mode %s id %d", mode, bondID)}
	if createdName != bond.Name {
		commands = append(commands, fmt.Sprintf("set interface name %s %s", createdName, bond.Name))
	}
	for _, member := range bond.Members {
		commands = append(commands, fmt.Sprintf("bond add %s %s", bond.Name, member))
	}
	return commands
}

func vppBondIdentity(name string) (int, string) {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, "BondEthernet") {
		if id, err := strconv.Atoi(strings.TrimPrefix(name, "BondEthernet")); err == nil && id >= 0 {
			return id, name
		}
	}
	id := stableID("vpp-bond:"+name, 1, 65534)
	return id, fmt.Sprintf("BondEthernet%d", id)
}
