package vpp

import (
	"sort"
	"strings"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

// stageRoutePolicyInstallOperations builds every private FIB before attaching
// any ABF policy. This prevents live catch-all traffic from traversing a route
// chain while a higher-priority table is only partially populated.
func stageRoutePolicyInstallOperations(operations []Operation) []Operation {
	firstRoute := -1
	routes := make([]Operation, 0)
	for index, operation := range operations {
		if !isStagedRoutePolicyApplyOperation(operation) {
			continue
		}
		if firstRoute < 0 {
			firstRoute = index
		}
		routes = append(routes, operation)
	}
	if len(routes) < 2 {
		return operations
	}

	sort.SliceStable(routes, func(i, j int) bool {
		left, leftOK := routes[i].Payload.(trafficpolicy.RoutePolicy)
		right, rightOK := routes[j].Payload.(trafficpolicy.RoutePolicy)
		if leftOK && rightOK && left.Priority != right.Priority {
			return left.Priority < right.Priority
		}
		return routes[i].Resource < routes[j].Resource
	})

	staged := make([]Operation, 0, len(operations)+2*len(routes))
	staged = append(staged, operations[:firstRoute]...)
	for _, operation := range routes {
		staged = append(staged, routePolicyStageOperation(operation, "vpp.route-table.prepare", routePolicyTableCreateCommands))
	}
	for _, operation := range routes {
		staged = append(staged, routePolicyStageOperation(operation, "vpp.route-table.populate", routePolicyTablePopulateCommands))
	}
	for _, operation := range routes {
		operation.VPPCtlCommands = routePolicyActivationCommands(operation.VPPCtlCommands)
		staged = append(staged, operation)
	}
	for index, operation := range operations {
		if index < firstRoute || isStagedRoutePolicyApplyOperation(operation) {
			continue
		}
		staged = append(staged, operation)
	}
	return staged
}

func isStagedRoutePolicyApplyOperation(operation Operation) bool {
	return isRoutePolicyApplyOperation(operation) && operationHasCommand(operation, "abf policy add")
}

func routePolicyStageOperation(operation Operation, name string, selectCommands func([]string) []string) Operation {
	operation.Name = name
	operation.VPPCtlCommands = selectCommands(operation.VPPCtlCommands)
	return operation
}

func routePolicyTableCreateCommands(commands []string) []string {
	selected := make([]string, 0, 1)
	for _, raw := range commands {
		command := strings.TrimSpace(strings.TrimPrefix(raw, "?"))
		if strings.HasPrefix(command, "ip table add ") {
			selected = append(selected, raw)
		}
	}
	return selected
}

func routePolicyTablePopulateCommands(commands []string) []string {
	selected := make([]string, 0, len(commands))
	for _, raw := range commands {
		command := strings.TrimSpace(strings.TrimPrefix(raw, "?"))
		switch {
		case command == vppRouteBatchBegin, command == vppRouteBatchEnd:
			selected = append(selected, raw)
		case strings.HasPrefix(command, "set ip flow-hash table "):
			selected = append(selected, raw)
		case strings.HasPrefix(command, "ip route add "):
			selected = append(selected, raw)
		}
	}
	return selected
}

func routePolicyActivationCommands(commands []string) []string {
	selected := make([]string, 0, len(commands))
	for _, raw := range commands {
		command := strings.TrimSpace(strings.TrimPrefix(raw, "?"))
		switch {
		case strings.HasPrefix(command, "set acl-plugin acl "):
			selected = append(selected, raw)
		case strings.HasPrefix(command, "abf policy add "):
			selected = append(selected, raw)
		case strings.HasPrefix(command, "abf attach ip4 policy "):
			selected = append(selected, raw)
		case strings.HasPrefix(command, "show acl-plugin acl index "):
			selected = append(selected, raw)
		case strings.HasPrefix(command, "show abf policy "):
			selected = append(selected, raw)
		case command == "show interface":
			selected = append(selected, raw)
		case strings.HasPrefix(command, "show abf attach "):
			selected = append(selected, raw)
		case strings.HasPrefix(command, "show ip fib table "):
			selected = append(selected, raw)
		}
	}
	return selected
}
