package apply

import "ly-route/backend/internal/runtime/vpp"

func gatewayDiffApplies(name string, diff vpp.GatewayDiff) bool {
	return gatewayDiffHasDelete(name, diff) || gatewayDiffHasApply(name, diff)
}

func gatewayDiffHasDelete(name string, diff vpp.GatewayDiff) bool {
	switch name {
	case "interfaces":
		return len(diff.Interfaces.DeleteInterfaces) > 0
	case "bonds":
		return len(diff.Bonds.DeleteBonds) > 0
	case "wan-groups":
		return len(diff.WANGroups.DeleteWANGroups) > 0
	case "routes":
		return len(diff.Routes.DeleteRoutes) > 0
	case "acls":
		return len(diff.ACLs.DeleteACLs) > 0
	case "qos":
		return len(diff.QoS.DeleteQoS) > 0
	case "nat44":
		return len(diff.NAT44.DeleteStaticMappings) > 0
	case "port-maps":
		return len(diff.PortMaps.DeletePortMappings) > 0
	default:
		return false
	}
}

func gatewayDiffHasApply(name string, diff vpp.GatewayDiff) bool {
	switch name {
	case "interfaces":
		return len(diff.Interfaces.Interfaces) > 0
	case "bonds":
		return len(diff.Bonds.Bonds) > 0
	case "wan-groups":
		return len(diff.WANGroups.WANGroups) > 0
	case "routes":
		return len(diff.Routes.Routes) > 0
	case "acls":
		return len(diff.ACLs.ACLs) > 0
	case "qos":
		return len(diff.QoS.QoS) > 0
	case "nat44":
		return len(diff.NAT44.StaticMappings) > 0
	case "port-maps":
		return len(diff.PortMaps.PortMappings) > 0
	default:
		return false
	}
}
