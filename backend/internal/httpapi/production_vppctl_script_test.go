package httpapi

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/proxy"
	"ly-route/backend/internal/runtime/trafficpolicy"
	"ly-route/backend/internal/runtime/vpp"
)

func writeProductionVPPCTL(t *testing.T, directory string, plan vpp.Plan) string {
	t.Helper()
	responses, err := productionVPPResponses(plan)
	if err != nil {
		t.Fatalf("compile production VPP proof responses: %v", err)
	}
	path := filepath.Join(directory, "vppctl")
	var script strings.Builder
	script.WriteString("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$FAKE_VPPCTL_TRACE\"\n")
	script.WriteString("case \"$*\" in\n")
	script.WriteString("  lcp\\ create\\ *) printf 'itf-pair: [0] %s tap4096 %s 36 type tap\\n' \"$3\" \"$5\" > \"$FAKE_VPPCTL_LCP_STATE\";;\n")
	script.WriteString("  lcp\\ delete\\ *) : > \"$FAKE_VPPCTL_LCP_STATE\";;\n")
	if len(plan.Interfaces) > 0 {
		for _, state := range plan.Interfaces {
			fmt.Fprintf(&script, "  %q) printf 'absent\\n' > \"$FAKE_VPPCTL_STATE\";;\n", "set interface state "+state.Name+" down")
			fmt.Fprintf(&script, "  %q) printf 'desired\\n' > \"$FAKE_VPPCTL_STATE\";;\n", "set interface state "+state.Name+" up")
		}
	}
	if len(plan.NAT.StaticMappings) > 0 {
		mapping := plan.NAT.StaticMappings[0]
		fmt.Fprintf(&script, "  %q) printf 'static\\n' > \"$FAKE_VPPCTL_NAT_STATE\";;\n", fmt.Sprintf("nat44 add static mapping local %s external %s", mapping.InternalAddress, mapping.ExternalAddress))
	}
	if len(plan.NAT.PortMappings) > 0 {
		mapping := plan.NAT.PortMappings[0]
		fmt.Fprintf(&script, "  %q) printf 'both\\n' > \"$FAKE_VPPCTL_NAT_STATE\";;\n", fmt.Sprintf("nat44 add static mapping %s local %s %d external %s %d", mapping.Protocol, mapping.InternalHost, mapping.InternalPort, mapping.ExternalAddress, mapping.ExternalPort))
		fmt.Fprintf(&script, "  %q) printf 'static\\n' > \"$FAKE_VPPCTL_NAT_STATE\";;\n", fmt.Sprintf("nat44 add static mapping %s local %s %d external %s %d del", mapping.Protocol, mapping.InternalHost, mapping.InternalPort, mapping.ExternalAddress, mapping.ExternalPort))
	}
	if plan.DNSInterception {
		v4ACL := proofStableID("dns-transparent-v4-acl", 50000, 9999)
		v6ACL := proofStableID("dns-transparent-v6-acl", 50000, 9999)
		fmt.Fprintf(&script, "  %q) printf 'index:%d\\n';;\n", "set acl-plugin acl permit src 0.0.0.0/0 dst 0.0.0.0/0 proto 17 sport 0-65535 dport 53-53, permit src 0.0.0.0/0 dst 0.0.0.0/0 proto 6 sport 0-65535 dport 53-53 tag ly-route-dns-transparent-v4", v4ACL)
		fmt.Fprintf(&script, "  %q) printf 'index:%d\\n';;\n", "set acl-plugin acl permit src ::/0 dst ::/0 proto 17 sport 0-65535 dport 53-53, permit src ::/0 dst ::/0 proto 6 sport 0-65535 dport 53-53 tag ly-route-dns-transparent-v6", v6ACL)
	}
	commands := sortedResponseCommands(responses)
	for _, command := range commands {
		if command == "show lcp" {
			script.WriteString("  'show lcp') printf \"lcp default netns '<unset>'\\n\"; cat \"$FAKE_VPPCTL_LCP_STATE\";;\n")
			continue
		}
		fmt.Fprintf(&script, "  '%s')\n", command)
		if command == "show interface address" {
			script.WriteString("    if [ \"$(cat \"$FAKE_VPPCTL_STATE\")\" = prior ]; then\n")
			script.WriteString("      cat <<'VPP_PRIOR'\n")
			fmt.Fprintf(&script, "%s (up):\n  L3 %s\n", plan.Interfaces[0].Name, plan.Interfaces[0].Addresses[0])
			script.WriteString("VPP_PRIOR\n    else\n")
		}
		if command == "show nat44 static mappings" {
			script.WriteString("    if [ \"$(cat \"$FAKE_VPPCTL_NAT_STATE\")\" = static ]; then\n      cat <<'VPP_NAT_STATIC'\nNAT44 static mappings:\n  local 192.168.88.10 external 203.0.113.10 vrf 0\nVPP_NAT_STATIC\n    else\n")
		}
		script.WriteString("    cat <<'VPP_OUTPUT'\n")
		script.WriteString(responses[command])
		script.WriteString("VPP_OUTPUT\n")
		if command == "show interface address" {
			script.WriteString("    fi\n")
		}
		if command == "show nat44 static mappings" {
			script.WriteString("    fi\n")
		}
		script.WriteString("    ;;\n")
	}
	script.WriteString("  show\\ *) printf '%s present\\n' \"$*\";;\n")
	script.WriteString("esac\nif [ -n \"$FAKE_VPPCTL_FAIL\" ] && printf '%s' \"$*\" | grep -F \"$FAKE_VPPCTL_FAIL\" >/dev/null; then printf 'injected vppctl failure\\n' >&2; exit 17; fi\nexit 0\n")
	if err := os.WriteFile(path, []byte(script.String()), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func productionVPPResponses(plan vpp.Plan) (map[string]string, error) {
	responses := map[string]string{}
	var routeACLInventory strings.Builder
	var wanPathListInventory strings.Builder
	if len(plan.Interfaces) > 0 {
		var output strings.Builder
		for _, state := range plan.Interfaces {
			fmt.Fprintf(&output, "%s (up):\n  L3 %s\n", state.Name, state.Addresses[0])
		}
		responses["show interface address"] = output.String()
		// The management LCP operation now verifies the limited-broadcast
		// route explicitly. Keep the production proof harness honest by
		// returning the same local receive DPO that VPP emits on a live node.
		responses["show ip fib 255.255.255.255"] = "255.255.255.255/32\n  [@12]: dpo-receive: 0.0.0.0 on local0\n"
	}
	if len(plan.Bonds) > 0 {
		bond := plan.Bonds[0]
		responses["show bond details"] = fmt.Sprintf("%s\n  mode: %s\n  number of active members: 1\n    %s\n      weight: 1, is_local_numa: 1, sw_if_index: 1\n  number of members: 1\n    %s\n  device instance: 0\n  interface id: 0\n  sw_if_index: 5\n  hw_if_index: 5\n", bond.Name, bond.Mode, bond.Members[0], bond.Members[0])
	}
	for _, group := range plan.Policy.WANGroups {
		tableID := proofStableID("wan-group:"+group.ID, 50000, 49999)
		fixture := productionWANGroupFixture(group, tableID)
		responses[fmt.Sprintf("show ip fib table %d", tableID)] = fixture
		wanPathListInventory.WriteString(fixture)
	}
	if wanPathListInventory.Len() > 0 {
		responses["show fib path-lists"] = wanPathListInventory.String()
	}
	for _, route := range plan.Policy.RoutePolicies {
		aclID := proofStableID("route-acl:"+route.ID, 10000, 49999)
		policyID := proofStableID("route-abf:"+route.ID, 10000, 8999)
		tableID := proofStableID("route-table:"+route.ID, 50000, 49999)
		wanTableID := proofStableID("wan-group:"+route.Egress, 50000, 49999)
		aclOutput := fmt.Sprintf("acl-index %d count 1 tag {ly-route-route_office}\n  0: ipv4 permit src 10.0.0.0/24 dst 0.0.0.0/0 proto 6 sport 0-65535 dport 443-443\n", aclID)
		responses[fmt.Sprintf("show acl-plugin acl index %d", aclID)] = aclOutput
		routeACLInventory.WriteString(aclOutput)
		responses[fmt.Sprintf("show abf policy %d", policyID)] = fmt.Sprintf("abf:[0]: policy:%d acl:%d\n path-list:[17] locks:1 flags:shared len:1\n  path:[21] pl-index:17 ip4 weight=1 pref=0\n    [@0]: ipv4 via table %d\n", policyID, aclID, wanTableID)
		responses[fmt.Sprintf("show ip fib table %d", tableID)] = fmt.Sprintf("ipv4-VRF:%d, fib_index:3, flow hash:[src dst sport dport proto]\n0.0.0.0/0\n  unicast-ip4-chain\n    [@0]: dpo-load-balance: [proto:ip4 index:8 buckets:1 uRPF:7 to:[0:0]]\n      path-list:[17] locks:1 flags:shared len:1\n        path:[21] pl-index:17 ip4 weight=1 pref=0\n          [@0]: ipv4 via table %d\n", tableID, wanTableID)
	}
	if plan.DNSInterception {
		v4Policy := proofStableID("dns-transparent-v4", 9000, 999)
		v6Policy := proofStableID("dns-transparent-v6", 9000, 999)
		v4ACL := proofStableID("dns-transparent-v4-acl", 50000, 9999)
		v6ACL := proofStableID("dns-transparent-v6-acl", 50000, 9999)
		lanInterface := "lyroute-eth1"
		for _, assignment := range plan.AddressAssignments {
			if strings.EqualFold(assignment.Role, "lan") && assignment.VPPInterface != "" {
				lanInterface = assignment.VPPInterface
				break
			}
		}
		responses[fmt.Sprintf("show abf policy %d", v4Policy)] = fmt.Sprintf("abf:[0]: policy:%d acl:%d\n", v4Policy, v4ACL)
		responses[fmt.Sprintf("show abf policy %d", v6Policy)] = fmt.Sprintf("abf:[1]: policy:%d acl:%d\n", v6Policy, v6ACL)
		attachments := fmt.Sprintf("%s\nipv4:\n abf-interface-attach: policy:%d priority:0\nipv6:\n abf-interface-attach: policy:%d priority:0\n", lanInterface, v4Policy, v6Policy)
		responses["show abf attach"] = attachments
		responses["show abf attach "+lanInterface] = attachments
		responses[fmt.Sprintf("show ip fib table %d", 101)] = "ipv6-VRF:101\n::/0 via local\n"
		fmt.Fprintf(&routeACLInventory, "acl-index %d count 2 tag {ly-route-dns-transparent-v4}\n  0: ipv4 permit src 0.0.0.0/0 dst 0.0.0.0/0 proto 17 sport 0-65535 dport 53-53\n  1: ipv4 permit src 0.0.0.0/0 dst 0.0.0.0/0 proto 6 sport 0-65535 dport 53-53\n", v4ACL)
		fmt.Fprintf(&routeACLInventory, "acl-index %d count 2 tag {ly-route-dns-transparent-v6}\n  0: ipv6 permit src ::/0 dst ::/0 proto 17 sport 0-65535 dport 53-53\n  1: ipv6 permit src ::/0 dst ::/0 proto 6 sport 0-65535 dport 53-53\n", v6ACL)
	}
	if routeACLInventory.Len() > 0 {
		responses["show acl-plugin acl"] = routeACLInventory.String()
	}
	for _, acl := range plan.Policy.SecurityACLs {
		aclID := proofStableID("security-acl:"+acl.ID, 50000, 49999)
		responses[fmt.Sprintf("show acl-plugin acl index %d", aclID)] = productionACLFixture(aclID, acl)
	}
	for _, group := range plan.Flow.VPPGroups {
		addQoSResponses(responses, group)
	}
	for _, assignment := range plan.NativePath.Assignments {
		interfaceName := "lyroute-" + assignment.LinuxInterface
		responses["show interface "+interfaceName] = interfaceName + " up\n"
		responses["show hardware-interfaces "+interfaceName] = interfaceName + "\n  netdev " + assignment.LinuxInterface + "\n"
	}
	for _, steering := range plan.Proxy.VPPSteering {
		resource := steering.EgressID
		if resource == "" {
			resource = string(steering.Handoff)
		}
		aclID := proofStableID("acl:"+resource, 1000, 8999)
		policyID := proofStableID("abf:"+resource, 1000, 8999)
		tableID := proofStableID("pbr:"+resource, 10000, 49999)
		responses[fmt.Sprintf("show acl-plugin acl index %d", aclID)] = fmt.Sprintf("proxy acl %d present\n", aclID)
		responses[fmt.Sprintf("show abf policy %d", policyID)] = fmt.Sprintf("proxy abf %d present\n", policyID)
		responses[fmt.Sprintf("show ip table %d", tableID)] = fmt.Sprintf("proxy table %d present\n", tableID)
		responses[fmt.Sprintf("show ip fib table %d", tableID)] = fmt.Sprintf("proxy FIB %d present\n", tableID)
		if steering.TargetKind == "vpp.proxy-service.network" {
			network := steering.ServiceNetwork
			if network.EgressVPPInterface == "" || network.IngressVPPInterface == "" {
				network = proxy.ServiceNetworkForEgressID(resource)
			}
			responses["show interface address "+network.IngressVPPInterface] = network.IngressVPPInterface + " (up):\n  L3 " + network.IngressVPPAddress + "/30\n"
			responses["show interface address "+network.EgressVPPInterface] = network.EgressVPPInterface + " (up):\n  L3 " + network.EgressVPPAddress + "/30\n"
			responses["show nat44 interfaces"] = "nat44 in: " + network.EgressVPPInterface + "\n"
			responses["show tap"] = "tap " + network.IngressVPPInterface + "\n"
		}
	}
	// The HTTP proof server supplies its built-in proxy egress even when the
	// compiler fixture has no persisted proxy document yet.
	if _, ok := responses["show nat44 interfaces"]; !ok {
		network := proxy.ServiceNetworkForEgressID("proxy-egress-default")
		responses["show interface address "+network.IngressVPPInterface] = network.IngressVPPInterface + " (up):\n  L3 " + network.IngressVPPAddress + "/30\n"
		responses["show interface address "+network.EgressVPPInterface] = network.EgressVPPInterface + " (up):\n  L3 " + network.EgressVPPAddress + "/30\n"
		responses["show nat44 interfaces"] = "nat44 in: " + network.EgressVPPInterface + "\n"
		responses["show tap"] = "tap " + network.IngressVPPInterface + "\n"
	}
	responses["show nat44 static mappings"] = "NAT44 static mappings:\n  local 192.168.88.10 external 203.0.113.10 vrf 0\n  tcp local 192.168.88.20:8443 external 203.0.113.10:8443 vrf 0\n"
	operationPlan := plan
	now := time.Now().UTC()
	operationPlan.NativePath.Now = now
	for index := range operationPlan.NativePath.Assignments {
		proof := &operationPlan.NativePath.Assignments[index].Proof
		proof.Source = vpp.ProofSourceRuntimeProbe
		proof.RuntimeVerified = true
		proof.Native = true
		proof.HighPerformance = true
		proof.ObservedAt = now.Add(-time.Minute)
		proof.ValidUntil = now.Add(time.Minute)
	}
	operations, err := vpp.BuildOperations(operationPlan)
	if err != nil {
		return nil, err
	}
	for _, operation := range operations {
		for _, command := range operation.VPPCtlCommands {
			command = strings.TrimSpace(strings.TrimPrefix(command, "?"))
			command = strings.ReplaceAll(command, "$LY_ROUTE_LAN_INTERFACE", "eth0")
			if strings.HasPrefix(command, "show ") && responses[command] == "" {
				responses[command] = fmt.Sprintf("%s resource %s present\n", operation.Name, operation.Resource)
				if strings.HasPrefix(command, "show abf attach ") {
					responses[command] = command + " present\n"
				}
			}
		}
	}
	normalized := make(map[string]string, len(responses))
	for command, output := range responses {
		normalized[strings.ReplaceAll(command, "$LY_ROUTE_LAN_INTERFACE", "eth0")] = output
	}
	return normalized, nil
}

func productionWANGroupFixture(group trafficpolicy.WANGroup, tableID int) string {
	var output strings.Builder
	fmt.Fprintf(&output, "ipv4-VRF:%d, fib_index:4, flow hash:[src dst sport dport proto]\n0.0.0.0/0\n  unicast-ip4-chain\n    [@0]: dpo-load-balance: [proto:ip4 index:9 buckets:%d uRPF:8 to:[0:0]]\n      path-list:[18] locks:1 flags:shared len:%d\n", tableID, len(group.Members), len(group.Members))
	for index, member := range group.Members {
		weight := group.Weights[member]
		if weight < 1 || group.Mode == trafficpolicy.WANGroupPrimaryBackup {
			weight = 1
		}
		preference := 0
		if group.Mode == trafficpolicy.WANGroupPrimaryBackup {
			preference = index
		}
		path := group.Paths[member]
		via := path.VPPInterface
		if via == "" {
			via = member
		}
		if path.NextHop != "" {
			via = path.NextHop + " " + via
		}
		fmt.Fprintf(&output, "        path:[%d] pl-index:18 ip4 weight=%d pref=%d\n          [@%d]: ipv4 via %s\n", 22+index, weight, preference, index, via)
	}
	return output.String()
}

func productionACLFixture(aclID int, acl trafficpolicy.SecurityACL) string {
	sources := proofValues(acl.Match.Sources, "0.0.0.0/0")
	destinations := proofValues(acl.Match.Destinations, "0.0.0.0/0")
	protocols := proofValues(acl.Match.Protocols, "any")
	sourcePorts := proofValues(acl.Match.SourcePorts, "any")
	destPorts := proofValues(acl.Match.DestPorts, "any")
	count := len(sources) * len(destinations) * len(protocols) * len(sourcePorts) * len(destPorts)
	var fixture strings.Builder
	fmt.Fprintf(&fixture, "acl-index %d count %d tag {ly-route-%s}\n", aclID, count, proofSafeTag(acl.ID))
	index := 0
	for _, source := range sources {
		for _, destination := range destinations {
			for _, protocol := range protocols {
				for _, sourcePort := range sourcePorts {
					for _, destPort := range destPorts {
						fmt.Fprintf(&fixture, "  %d: ipv4 %s src %s dst %s proto %s sport %s dport %s\n", index, acl.Action, source, destination, proofProtocol(protocol), proofPortRange(sourcePort), proofPortRange(destPort))
						index++
					}
				}
			}
		}
	}
	return fixture.String()
}

func proofValues(values []string, fallback string) []string {
	if len(values) == 0 {
		return []string{fallback}
	}
	return values
}

func proofProtocol(value string) string {
	switch strings.ToLower(value) {
	case "tcp":
		return "6"
	case "udp":
		return "17"
	case "icmp":
		return "1"
	default:
		return "0"
	}
}

func proofPortRange(value string) string {
	if value == "" || value == "any" {
		return "0-65535"
	}
	if strings.Contains(value, "-") {
		return value
	}
	return value + "-" + value
}

func proofSafeTag(value string) string {
	var tag strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			tag.WriteRune(character)
		} else if tag.Len() > 0 {
			tag.WriteByte('_')
		}
	}
	return strings.Trim(tag.String(), "_")
}

func addQoSResponses(responses map[string]string, group flow.VPPObjectGroup) {
	for _, object := range group.Objects {
		mapID := proofStableID("qos-map:"+object.RuleID, 1, 999)
		responses[fmt.Sprintf("show qos egress map id %d", mapID)] = proofQoSMap(mapID, proofStableID("qos-class:"+object.Class, 1, 62), 46)
		responses["show qos mark lyroute-$LY_ROUTE_LAN_INTERFACE"] = fmt.Sprintf("lyroute-$LY_ROUTE_LAN_INTERFACE:\n  IP: map:%d\n", mapID)
	}
}

func sortedResponseCommands(responses map[string]string) []string {
	commands := make([]string, 0, len(responses))
	for command := range responses {
		commands = append(commands, command)
	}
	sort.Strings(commands)
	return commands
}
