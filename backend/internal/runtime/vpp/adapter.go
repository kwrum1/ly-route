package vpp

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/proxy"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

type Plan struct {
	RequestID          string
	NativePath         NativePathRequest
	AddressAssignments []AddressAssignment
	Interfaces         []InterfaceState
	Bonds              []BondState
	Proxy              proxy.CompiledEgress
	Flow               flow.CompiledIntent
	NAT                nat.CompiledConfig
	Policy             trafficpolicy.Config
	Security           SecurityGeneration
	DNSInterception    bool
	DataplanePrepared  bool `json:"-"`
}

type DNSTransparentInterception struct {
	LANInterface string   `json:"lan_interface"`
	IPv6Prefixes []string `json:"ipv6_prefixes,omitempty"`
}

type ManagementLCP struct {
	Enabled       bool   `json:"enabled"`
	VPPInterface  string `json:"vpp_interface"`
	HostInterface string `json:"host_interface"`
}

type AddressAssignment struct {
	ID             string   `json:"id"`
	LinuxInterface string   `json:"linux_interface"`
	VPPInterface   string   `json:"vpp_interface"`
	CIDR           string   `json:"cidr"`
	Mode           string   `json:"mode,omitempty"`
	RemoveCIDRs    []string `json:"remove_cidrs,omitempty"`
	Role           string   `json:"role,omitempty"`
	BandwidthKbps  uint64   `json:"bandwidth_kbps,omitempty"`
}

type SmartQoSInterface struct {
	VPPInterface  string `json:"vpp_interface"`
	Role          string `json:"role"`
	RateKbps      uint64 `json:"rate_kbps"`
	HostIsolation string `json:"host_isolation"`
}

type Operation struct {
	Name           string
	RequestID      string
	Resource       string
	Payload        any
	VPPCtlCommands []string `json:"vppctl_commands,omitempty"`
}

type Reply struct {
	Operation   string
	Retval      int32
	Done        bool
	ControlPing bool
	Payload     any
}

type Receipt struct {
	RequestID  string      `json:"request_id"`
	Operations []Operation `json:"operations"`
}

type Health struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

type Snapshot struct {
	RequestID     string                      `json:"request_id"`
	TransactionID string                      `json:"transaction_id"`
	ReadbackAt    time.Time                   `json:"readback_at"`
	Interfaces    []InterfaceState            `json:"interfaces,omitempty"`
	Bonds         []BondState                 `json:"bonds,omitempty"`
	RoutePolicies []trafficpolicy.RoutePolicy `json:"route_policies,omitempty"`
	WANGroups     []trafficpolicy.WANGroup    `json:"wan_groups,omitempty"`
	ACLs          []trafficpolicy.SecurityACL `json:"acls,omitempty"`
	QoS           []flow.VPPObjectGroup       `json:"qos,omitempty"`
	NAT           nat.CompiledConfig          `json:"nat,omitempty"`
	Hash          string                      `json:"hash"`
}

type Client interface {
	OpenChannel(context.Context) (Channel, error)
}

type Channel interface {
	Do(context.Context, Operation) (Reply, error)
	Close() error
}

type MultipartStream interface {
	Recv(context.Context) (Reply, error)
}

type Adapter struct {
	Client Client
}

type UnsupportedOperationError struct {
	Name     string
	Resource string
}

func (err *UnsupportedOperationError) Error() string {
	return fmt.Sprintf("unsupported production VPP operation %q for resource %q", err.Name, err.Resource)
}

func (err *UnsupportedOperationError) UnsupportedOperation() string { return err.Name }

type VPPError struct {
	Operation string
	RequestID string
	Retval    int32
	Err       error
}

func (e VPPError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("vpp operation %s request %s failed: %v", e.Operation, e.RequestID, e.Err)
	}
	return fmt.Sprintf("vpp operation %s request %s returned retval %d", e.Operation, e.RequestID, e.Retval)
}

func (e VPPError) Unwrap() error { return e.Err }

func (a Adapter) Apply(ctx context.Context, plan Plan) (Receipt, error) {
	operations, err := BuildOperations(plan)
	if err != nil {
		return Receipt{}, err
	}
	if len(operations) == 0 {
		return Receipt{RequestID: plan.RequestID, Operations: operations}, nil
	}
	if a.Client == nil {
		return Receipt{}, VPPError{Operation: "connect", RequestID: plan.RequestID, Err: errors.New("vpp client is not configured")}
	}
	channel, err := a.Client.OpenChannel(ctx)
	if err != nil {
		return Receipt{}, VPPError{Operation: "open_channel", RequestID: plan.RequestID, Err: err}
	}
	defer channel.Close()

	for _, operation := range operations {
		reply, err := channel.Do(ctx, operation)
		if err != nil {
			return Receipt{}, VPPError{Operation: operation.Name, RequestID: operation.RequestID, Err: err}
		}
		if err := NormalizeRetval(operation.Name, operation.RequestID, reply.Retval); err != nil {
			return Receipt{}, err
		}
	}
	return Receipt{RequestID: plan.RequestID, Operations: operations}, nil
}

func (a Adapter) ExecuteOperations(ctx context.Context, operations []Operation) error {
	if len(operations) == 0 {
		return nil
	}
	if a.Client == nil {
		return fmt.Errorf("%w: vpp client is not configured", ErrVPPUnavailable)
	}
	channel, err := a.Client.OpenChannel(ctx)
	if err != nil {
		return fmt.Errorf("%w: open operation channel: %v", ErrVPPUnavailable, err)
	}
	defer channel.Close()
	var operationErrors []error
	for _, operation := range operations {
		if _, err := doOperation(ctx, channel, operation); err != nil {
			operationErrors = append(operationErrors, err)
		}
	}
	return errors.Join(operationErrors...)
}

func (a Adapter) HealthCheck(context.Context) (Health, error) {
	if a.Client == nil {
		return Health{Available: false, Reason: "vpp client is not configured"}, nil
	}
	return Health{Available: true}, nil
}

func (a Adapter) Snapshot(ctx context.Context, request SnapshotRequest) (Snapshot, error) {
	return a.snapshot(ctx, request)
}

func (a Adapter) Rollback(context.Context, Snapshot) error {
	return errors.New("vpp live rollback is not implemented in this foundation slice")
}

func BuildOperations(plan Plan) ([]Operation, error) {
	operations := []Operation{}
	needsDataplane := plan.DNSInterception || len(plan.NativePath.Assignments) > 0 || len(plan.AddressAssignments) > 0 || len(plan.Proxy.VPPSteering) > 0 || len(plan.Flow.VPPGroups) > 0 || len(plan.Flow.Targets) > 0 || len(plan.NAT.StaticMappings) > 0 || len(plan.NAT.PortMappings) > 0 || len(plan.Policy.WANGroups) > 0 || len(plan.Policy.RoutePolicies) > 0 || len(plan.Policy.SecurityACLs) > 0 || len(plan.Security.ACLs) > 0 || len(plan.Security.MACIP) > 0 || len(plan.Security.AttackRules) > 0
	if !needsDataplane {
		return operations, nil
	}
	nativePath, err := SelectNativePath(plan.NativePath)
	if err != nil {
		return nil, err
	}
	if nativePath.Tier == DataplaneTierDPDK && !plan.DataplanePrepared {
		prerequisites := append([]PrerequisiteResult(nil), nativePath.Prerequisites...)
		prerequisites = append(prerequisites, prerequisite(
			"dpdk_runtime_adapter_ready", "", false,
			"DPDK was selected as the common tier but PCI/VFIO bind, VPP restart, readback, and rollback are not yet transactionally integrated",
		))
		return nil, &DataplaneLockedError{Prerequisites: prerequisites, Candidates: nativePath.Candidates}
	}
	attachments := make(map[string]NativeAttachment, len(nativePath.Attachments))
	for _, attachment := range nativePath.Attachments {
		attachments[strings.TrimSpace(attachment.LinuxInterface)] = attachment
	}
	prerequisites := append([]PrerequisiteResult(nil), nativePath.Prerequisites...)
	management := strings.TrimSpace(plan.NativePath.ManagementInterface)
	for _, assignment := range plan.AddressAssignments {
		linuxInterface := strings.TrimSpace(assignment.LinuxInterface)
		vppInterface := strings.TrimSpace(assignment.VPPInterface)
		attachment, proven := attachments[linuxInterface]
		prerequisites = append(prerequisites,
			prerequisite("address_assignment_management_excluded", linuxInterface, management != "" && (plan.NativePath.ManagementShared || linuxInterface != management), "address assignment references the management interface"),
			prerequisite("address_assignment_proven", linuxInterface, proven, "address assignment has no selected runtime-proven native attachment"),
			prerequisite("address_assignment_vpp_interface_matches", linuxInterface, proven && vppInterface == attachment.VPPInterface, "address assignment VPP interface does not match the selected native attachment"),
		)
	}
	for _, result := range prerequisites {
		if !result.Passed {
			return nil, &DataplaneLockedError{Prerequisites: prerequisites}
		}
	}
	for _, attachment := range nativePath.Attachments {
		operations = append(operations, DataplaneAttachOperation(plan.RequestID, attachment))
	}
	for _, assignment := range plan.AddressAssignments {
		operation := Operation{Name: "vpp.interface.address", RequestID: plan.RequestID, Resource: assignment.ID, Payload: assignment}
		operation.VPPCtlCommands = interfaceAddressCommands(assignment)
		operations = append(operations, operation)
	}
	if nativePath.SmartQoS {
		smartQoSOperations, smartQoSPrerequisites := buildSmartQoSOperations(plan.RequestID, plan.AddressAssignments)
		prerequisites = append(prerequisites, smartQoSPrerequisites...)
		operations = append(operations, smartQoSOperations...)
	}
	managementLCP, lcpReady := managementLCPOperation(plan)
	if plan.NativePath.ManagementShared && !lcpReady {
		return nil, &DataplaneLockedError{Prerequisites: append(prerequisites, prerequisite("shared_management_lan_binding", management, false, "shared management requires the management interface to be the configured LAN"))}
	}
	if management != "" {
		operations = append(operations, Operation{Name: "vpp.management-lcp", RequestID: plan.RequestID, Resource: "management-network", Payload: managementLCP, VPPCtlCommands: managementLCPCommands(managementLCP)})
	}
	if plan.DNSInterception {
		interception, ok := dnsTransparentInterception(plan.AddressAssignments)
		if !ok {
			return nil, &DataplaneLockedError{Prerequisites: append(prerequisites, prerequisite("dns_lan_interface_present", "", false, "transparent DNS interception requires one configured LAN interface"))}
		}
		operations = append(operations, Operation{Name: "vpp.dns-transparent-interception", RequestID: plan.RequestID, Resource: "gateway-dns", Payload: interception, VPPCtlCommands: dnsTransparentCommands(interception)})
	}
	wanGroups := map[string]trafficpolicy.WANGroup{}
	for _, group := range plan.Policy.WANGroups {
		wanGroups[group.ID] = group
	}
	for _, steering := range plan.Proxy.VPPSteering {
		operation := Operation{Name: steering.TargetKind, RequestID: plan.RequestID, Resource: steering.EgressID, Payload: steering}
		operation.VPPCtlCommands = proxySteeringCommands(steering)
		operations = append(operations, operation)
	}
	for _, group := range plan.Flow.VPPGroups {
		if len(group.Objects) == 0 {
			continue
		}
		operation := Operation{Name: group.Kind, RequestID: plan.RequestID, Resource: group.Kind, Payload: group}
		operation.VPPCtlCommands = flowGroupCommands(group)
		operations = append(operations, operation)
	}
	for _, target := range plan.Flow.Targets {
		operation := Operation{Name: target.Kind, RequestID: plan.RequestID, Resource: target.RuleID, Payload: target}
		operation.VPPCtlCommands = flowTargetCommands(target)
		operations = append(operations, operation)
	}
	for _, mapping := range plan.NAT.StaticMappings {
		operation := Operation{Name: "vpp.nat44-ed.static-mapping", RequestID: plan.RequestID, Resource: mapping.ID, Payload: mapping}
		operation.VPPCtlCommands = natStaticMappingCommands(mapping)
		operations = append(operations, operation)
	}
	for _, mapping := range plan.NAT.PortMappings {
		operation := Operation{Name: "vpp.nat44-ed.static-mapping", RequestID: plan.RequestID, Resource: mapping.ID, Payload: mapping}
		operation.VPPCtlCommands = natPortMappingCommands(mapping)
		operations = append(operations, operation)
	}
	for _, group := range plan.Policy.WANGroups {
		operation := Operation{Name: "vpp.pbr.next-hop-group", RequestID: plan.RequestID, Resource: group.ID, Payload: group}
		operation.VPPCtlCommands = wanGroupCommands(group)
		operations = append(operations, operation)
	}
	for _, policy := range plan.Policy.RoutePolicies {
		operation := Operation{Name: "vpp.route-policy", RequestID: plan.RequestID, Resource: policy.ID, Payload: policy}
		operation.VPPCtlCommands = routePolicyCommands(policy, wanGroups)
		operations = append(operations, operation)
	}
	wanIngressInterface := ""
	for _, assignment := range plan.AddressAssignments {
		if strings.EqualFold(strings.TrimSpace(assignment.Role), "wan") {
			wanIngressInterface = strings.TrimSpace(assignment.VPPInterface)
			break
		}
	}
	for _, acl := range plan.Policy.SecurityACLs {
		// The built-in rule protects WAN ingress. A PPPoE interface is created
		// only after negotiation, so attaching this rule to LAN would lock out
		// all clients. NAT44-ED remains stateful while the dynamic WAN has no
		// attachable interface; the PPPoE lifecycle owns its later WAN binding.
		if acl.ID == "sec-acl-default-deny-wan" && wanIngressInterface == "" {
			continue
		}
		operation := Operation{Name: "vpp.security-acl", RequestID: plan.RequestID, Resource: acl.ID, Payload: acl}
		operation.VPPCtlCommands = securityACLCommands(acl, wanIngressInterface)
		operations = append(operations, operation)
	}
	if len(plan.Security.ACLs) > 0 || len(plan.Security.MACIP) > 0 || len(plan.Security.AttackRules) > 0 {
		securityCommands, err := securityGenerationCommands(plan.Security)
		if err != nil {
			return nil, err
		}
		operations = append(operations, Operation{Name: "vpp.security-generation", RequestID: plan.RequestID, Resource: plan.Security.ID, Payload: plan.Security, VPPCtlCommands: securityCommands})
	}
	if len(nativePath.Attachments) > 0 {
		operations = rewriteOperationsInterface(operations, nativePath.Attachments[0].VPPInterface)
	}
	for _, operation := range operations {
		if len(operation.VPPCtlCommands) == 0 {
			return nil, &UnsupportedOperationError{Name: operation.Name, Resource: operation.Resource}
		}
		for _, command := range operation.VPPCtlCommands {
			if commandReferencesInterface(command, management) && !plan.NativePath.ManagementShared {
				prerequisites = append(prerequisites, prerequisite("management_excluded_from_operations", management, false, "operation "+operation.Name+" references the management interface"))
			}
		}
	}
	for _, result := range prerequisites {
		if !result.Passed {
			return nil, &DataplaneLockedError{Prerequisites: prerequisites}
		}
	}
	return operations, nil
}

func buildSmartQoSOperations(requestID string, assignments []AddressAssignment) ([]Operation, []PrerequisiteResult) {
	operations := []Operation{}
	prerequisites := []PrerequisiteResult{}
	seen := map[string]bool{}
	hasLAN := false
	hasWAN := false
	for _, assignment := range assignments {
		role := strings.ToLower(strings.TrimSpace(assignment.Role))
		if role != "lan" && role != "wan" {
			continue
		}
		vppInterface := strings.TrimSpace(assignment.VPPInterface)
		if vppInterface == "" || seen[vppInterface] {
			continue
		}
		seen[vppInterface] = true
		if role == "lan" {
			hasLAN = true
		} else {
			hasWAN = true
		}
		configured := assignment.BandwidthKbps >= 64 && assignment.BandwidthKbps <= 400_000_000
		prerequisites = append(prerequisites, prerequisite(
			"smart_qos_bandwidth_configured", vppInterface, configured,
			"LAN download or WAN upload bandwidth_kbps must be between 64 and 400000000",
		))
		if !configured {
			continue
		}
		isolation := "source"
		if role == "lan" {
			isolation = "destination"
		}
		payload := SmartQoSInterface{VPPInterface: vppInterface, Role: role, RateKbps: assignment.BandwidthKbps, HostIsolation: isolation}
		operations = append(operations, Operation{
			Name:      "vpp.smart-qos",
			RequestID: requestID,
			Resource:  vppInterface,
			Payload:   payload,
			VPPCtlCommands: []string{
				fmt.Sprintf("set ly-route smart-qos interface %s rate %d host-isolation %s", vppInterface, assignment.BandwidthKbps, isolation),
				"show ly-route smart-qos",
			},
		})
	}
	prerequisites = append(prerequisites,
		prerequisite("smart_qos_lan_present", "", hasLAN, "built-in smart QoS requires one logical LAN output"),
		prerequisite("smart_qos_wan_present", "", hasWAN, "built-in smart QoS requires at least one WAN output"),
	)
	return operations, prerequisites
}

func managementLCPOperation(plan Plan) (ManagementLCP, bool) {
	management := strings.TrimSpace(plan.NativePath.ManagementInterface)
	result := ManagementLCP{Enabled: plan.NativePath.ManagementShared, VPPInterface: "lyroute-" + management, HostInterface: "lymgmt0"}
	if !plan.NativePath.ManagementShared {
		return result, management != ""
	}
	for _, assignment := range plan.AddressAssignments {
		if strings.TrimSpace(assignment.LinuxInterface) != management || !strings.EqualFold(strings.TrimSpace(assignment.Role), "lan") {
			continue
		}
		if name := strings.TrimSpace(assignment.VPPInterface); name != "" {
			result.VPPInterface = name
		}
		return result, result.VPPInterface != ""
	}
	return result, false
}

func managementLCPCommands(lcp ManagementLCP) []string {
	commands := []string{"show lcp"}
	if lcp.Enabled {
		commands = append(commands, fmt.Sprintf("lcp create %s host-if %s", lcp.VPPInterface, lcp.HostInterface), "lcp lcp-sync on", "show lcp")
	}
	return commands
}

func dnsTransparentInterception(assignments []AddressAssignment) (DNSTransparentInterception, bool) {
	result := DNSTransparentInterception{}
	seenPrefixes := map[string]bool{}
	for _, assignment := range assignments {
		if strings.ToLower(strings.TrimSpace(assignment.Role)) != "lan" {
			continue
		}
		name := strings.TrimSpace(assignment.VPPInterface)
		if name == "" {
			name = nativeLANInterface(strings.TrimSpace(assignment.LinuxInterface))
		}
		if result.LANInterface == "" {
			result.LANInterface = name
		}
		if name != result.LANInterface {
			return DNSTransparentInterception{}, false
		}
		cidr := strings.TrimSpace(assignment.CIDR)
		if strings.Contains(cidr, ":") && !seenPrefixes[cidr] {
			seenPrefixes[cidr] = true
			result.IPv6Prefixes = append(result.IPv6Prefixes, cidr)
		}
	}
	return result, result.LANInterface != ""
}

func dnsTransparentCommands(interception DNSTransparentInterception) []string {
	v4Policy := stableID("dns-transparent-v4", 9000, 999)
	v6Policy := stableID("dns-transparent-v6", 9000, 999)
	v4ACL := stableID("dns-transparent-v4-acl", 50000, 9999)
	v6ACL := stableID("dns-transparent-v6-acl", 50000, 9999)
	commands := []string{
		fmt.Sprintf("set acl-plugin acl index %d permit src 0.0.0.0/0 dst 0.0.0.0/0 proto 17 sport 0-65535 dport 53-53, permit src 0.0.0.0/0 dst 0.0.0.0/0 proto 6 sport 0-65535 dport 53-53 tag ly-route-dns-transparent-v4", v4ACL),
		fmt.Sprintf("abf policy add id %d acl %d via local", v4Policy, v4ACL),
		fmt.Sprintf("abf attach ip4 policy %d priority 0 %s", v4Policy, interception.LANInterface),
		"ip6 table add 101", "ip route add table 101 ::/0 via local",
	}
	for _, prefix := range interception.IPv6Prefixes {
		commands = append(commands, fmt.Sprintf("ip route add table 101 %s via %s", prefix, interception.LANInterface))
	}
	return append(commands,
		fmt.Sprintf("set acl-plugin acl index %d permit src ::/0 dst ::/0 proto 17 sport 0-65535 dport 53-53, permit src ::/0 dst ::/0 proto 6 sport 0-65535 dport 53-53 tag ly-route-dns-transparent-v6", v6ACL),
		fmt.Sprintf("abf policy add id %d acl %d via ip6-lookup-in-table 101", v6Policy, v6ACL),
		fmt.Sprintf("abf attach ip6 policy %d priority 0 %s", v6Policy, interception.LANInterface),
		fmt.Sprintf("show abf policy %d", v4Policy), fmt.Sprintf("show abf policy %d", v6Policy),
		fmt.Sprintf("show abf attach %s", interception.LANInterface), "show acl-plugin acl", "show ip fib table 101")
}

func commandReferencesInterface(command, interfaceName string) bool {
	if interfaceName == "" {
		return false
	}
	for _, field := range strings.Fields(command) {
		token := strings.Trim(field, "?[](),;")
		if token == interfaceName || token == "lyroute-"+interfaceName || token == "host-"+interfaceName {
			return true
		}
	}
	return false
}

func rewriteOperationsInterface(operations []Operation, vppInterface string) []Operation {
	if strings.TrimSpace(vppInterface) == "" {
		return operations
	}
	for index := range operations {
		for commandIndex, command := range operations[index].VPPCtlCommands {
			command = strings.ReplaceAll(command, "lyroute-$LY_ROUTE_LAN_INTERFACE", vppInterface)
			command = strings.ReplaceAll(command, "host-$LY_ROUTE_LAN_INTERFACE", vppInterface)
			operations[index].VPPCtlCommands[commandIndex] = command
		}
	}
	return operations
}

func interfaceAddressCommands(assignment AddressAssignment) []string {
	name := strings.TrimSpace(assignment.VPPInterface)
	if name == "" {
		name = nativeLANInterface(strings.TrimSpace(assignment.LinuxInterface))
	}
	if strings.ToLower(strings.TrimSpace(assignment.Mode)) == "dhcp4" {
		if name == "" {
			return nil
		}
		commands := []string{fmt.Sprintf("set interface state %s up", name)}
		for _, cidr := range assignment.RemoveCIDRs {
			cidr = strings.TrimSpace(cidr)
			if cidr != "" {
				commands = append(commands, fmt.Sprintf("?set interface ip address %s %s del", name, cidr))
			}
		}
		commands = append(commands,
			fmt.Sprintf("?set dhcp client del intfc %s", name),
			fmt.Sprintf("set dhcp client intfc %s", name),
			"show dhcp client",
			fmt.Sprintf("show interface address %s", name),
		)
		return commands
	}
	cidr := strings.TrimSpace(assignment.CIDR)
	if name == "" || cidr == "" {
		return nil
	}
	return []string{
		fmt.Sprintf("set interface state %s up", name),
		fmt.Sprintf("?set interface ip address %s %s", name, cidr),
		fmt.Sprintf("show interface address %s", name),
	}
}

func DataplaneAttachOperation(requestID string, attachment NativeAttachment) Operation {
	if attachment.Tier == DataplaneTierDPDK {
		return Operation{
			Name:      "vpp.dataplane.attach",
			RequestID: requestID,
			Resource:  attachment.LinuxInterface,
			Payload:   attachment,
			VPPCtlCommands: []string{
				fmt.Sprintf("show hardware-interfaces %s", attachment.VPPInterface),
				fmt.Sprintf("show interface %s", attachment.VPPInterface),
			},
		}
	}
	commands := []string(nil)
	switch {
	case attachment.Hook == NativeHookAFXDP && attachment.Mode == NativeModeZeroCopy:
		commands = []string{
			fmt.Sprintf("?create interface af_xdp host-if %s name %s zero-copy", attachment.LinuxInterface, attachment.VPPInterface),
			fmt.Sprintf("set interface state %s up", attachment.VPPInterface),
			fmt.Sprintf("show hardware-interfaces %s", attachment.VPPInterface),
			fmt.Sprintf("show interface %s", attachment.VPPInterface),
		}
	case attachment.Hook == NativeHookRDMA && attachment.Mode == NativeModeRDMADV:
		commands = []string{
			fmt.Sprintf("?create interface rdma host-if %s name %s mode dv", attachment.LinuxInterface, attachment.VPPInterface),
			fmt.Sprintf("set interface state %s up", attachment.VPPInterface),
			fmt.Sprintf("show hardware-interfaces %s", attachment.VPPInterface),
			fmt.Sprintf("show interface %s", attachment.VPPInterface),
		}
	}
	return Operation{
		Name:           "vpp.dataplane.attach",
		RequestID:      requestID,
		Resource:       attachment.LinuxInterface,
		Payload:        attachment,
		VPPCtlCommands: commands,
	}
}

func proxySteeringCommands(steering proxy.VPPSteeringInstruction) []string {
	interfaceName := "lyroute-$LY_ROUTE_LAN_INTERFACE"
	resource := steering.EgressID
	if strings.TrimSpace(resource) == "" {
		resource = string(steering.Handoff)
	}
	policyID := stableID("abf:"+resource, 1000, 8999)
	aclID := stableID("acl:"+resource, 1000, 8999)
	tableID := stableID("pbr:"+resource, 10000, 49999)
	priority := steering.Order
	if priority <= 0 {
		priority = 100
	}

	switch steering.TargetKind {
	case "vpp.abf.policy":
		return []string{
			fmt.Sprintf("?set acl-plugin acl index %d permit src 0.0.0.0/0 dst 0.0.0.0/0 tag ly-route-%s", aclID, safeTag(resource)),
			fmt.Sprintf("?abf policy add id %d acl %d via local", policyID, aclID),
			fmt.Sprintf("?abf attach ip4 policy %d priority %d %s", policyID, priority, interfaceName),
			fmt.Sprintf("show acl-plugin acl index %d", aclID),
			fmt.Sprintf("show abf policy %d", policyID),
			fmt.Sprintf("show abf attach %s", interfaceName),
		}
	case "vpp.pbr.policy":
		return []string{
			fmt.Sprintf("?ip table add %d", tableID),
			fmt.Sprintf("?ip route add table %d 0.0.0.0/0 via local", tableID),
			fmt.Sprintf("?set ip flow-hash table %d src dst sport dport proto", tableID),
			fmt.Sprintf("show ip table %d", tableID),
			fmt.Sprintf("show ip fib table %d", tableID),
		}
	case "vpp.service-chain.egress-binding":
		return []string{
			fmt.Sprintf("show interface %s", interfaceName),
			fmt.Sprintf("show abf attach %s", interfaceName),
			fmt.Sprintf("show ip table %d", tableID),
		}
	default:
		return nil
	}
}

func flowGroupCommands(group flow.VPPObjectGroup) []string {
	if len(group.Objects) == 0 {
		return nil
	}
	commands := make([]string, 0, len(group.Objects)*2)
	seen := map[string]struct{}{}
	for _, object := range group.Objects {
		target := flow.Target{Kind: group.Kind, RuleID: object.RuleID, Granularity: object.Granularity, Action: object.Action, Class: object.Class, DSCP: object.DSCP, RemarkBehavior: object.RemarkBehavior, Policer: object.Policer, Match: object.Match, Attachments: object.Attachments}
		for _, command := range flowTargetCommands(target) {
			if _, ok := seen[command]; ok {
				continue
			}
			seen[command] = struct{}{}
			commands = append(commands, command)
		}
	}
	return commands
}

func flowTargetCommands(target flow.Target) []string {
	interfaceName := "lyroute-$LY_ROUTE_LAN_INTERFACE"
	resource := target.RuleID
	if strings.TrimSpace(resource) == "" {
		resource = target.Kind
	}
	mapID := stableID("qos-map:"+resource, 1, 999)
	policerName := "ly_route_" + safeTag(resource)
	qosValue := qosClassValue(target)
	dscp := dscpValue(target.DSCP)
	policerRate := uint64(1_000_000)
	policerBurst := uint64(100_000)
	if target.Policer != nil {
		if target.Policer.RateBPS > 0 {
			policerRate = target.Policer.RateBPS / 1000
			if policerRate == 0 {
				policerRate = 1
			}
		}
		if target.Policer.BurstBPS > 0 {
			policerBurst = target.Policer.BurstBPS / 1000
			if policerBurst == 0 {
				policerBurst = 1
			}
		}
	}

	switch target.Kind {
	case "vpp.acl.drop":
		aclID := stableID("flow-acl-drop:"+resource, 10000, 49999)
		commands := aclMatchCommands(aclID, resource, policyMatch(target.Match), "deny")
		for _, attachment := range target.Attachments {
			commands = append(commands, flowAttachACLCommand(aclID, attachment))
		}
		commands = append(commands, fmt.Sprintf("show acl-plugin acl index %d", aclID))
		return commands
	case "vpp.behavior.rate":
		aclID := stableID("flow-acl-rate:"+resource, 10000, 49999)
		commands := aclMatchCommands(aclID, resource, policyMatch(target.Match), "permit")
		commands = append(commands, fmt.Sprintf("?policer add name %s type 1r2c cir %d cb %d rate kbps conform-action transmit exceed-action drop violate-action drop", policerName, policerRate, policerBurst))
		for _, attachment := range target.Attachments {
			commands = append(commands, flowAttachACLCommand(aclID, attachment))
			commands = append(commands, flowAttachPolicerCommand(policerName, attachment))
		}
		commands = append(commands, fmt.Sprintf("show acl-plugin acl index %d", aclID), fmt.Sprintf("show policer name %s", policerName))
		return commands
	case "vpp.qos.classify":
		return []string{
			fmt.Sprintf("qos record ip %s", interfaceName),
			fmt.Sprintf("qos store ip %s value %d", interfaceName, qosValue),
			fmt.Sprintf("show qos record %s", interfaceName),
			fmt.Sprintf("show qos store %s", interfaceName),
		}
	case "vpp.qos.record":
		return []string{
			fmt.Sprintf("qos record ip %s", interfaceName),
			fmt.Sprintf("show qos record %s", interfaceName),
		}
	case "vpp.qos.store":
		return []string{
			fmt.Sprintf("qos store ip %s value %d", interfaceName, qosValue),
			fmt.Sprintf("show qos store %s", interfaceName),
		}
	case "vpp.qos.egress-map":
		return []string{
			fmt.Sprintf("qos egress map id %d [ip][%d]=%d", mapID, qosValue, dscp),
			fmt.Sprintf("show qos egress map id %d", mapID),
		}
	case "vpp.qos.mark":
		return []string{
			fmt.Sprintf("qos egress map id %d [ip][%d]=%d", mapID, qosValue, dscp),
			fmt.Sprintf("qos mark ip %s id %d", interfaceName, mapID),
			fmt.Sprintf("show qos egress map id %d", mapID),
			fmt.Sprintf("show qos mark %s", interfaceName),
		}
	case "vpp.policer":
		return []string{
			fmt.Sprintf("?policer add name %s type 1r2c cir %d cb %d rate kbps conform-action transmit exceed-action drop violate-action drop", policerName, policerRate, policerBurst),
			fmt.Sprintf("?policer input name %s %s", policerName, interfaceName),
			fmt.Sprintf("show policer name %s", policerName),
		}
	default:
		return nil
	}
}

func flowACLMatch(match flow.Match) string {
	return fmt.Sprintf("src %s dst %s proto %s sport %s dport %s", firstOrAny(match.Sources), firstOrAny(match.Destinations), firstOrAny(match.Protocols), portRange(firstOrAny(match.SourcePorts)), portRange(firstOrAny(match.DestPorts)))
}

func flowAttachACLCommand(aclID int, attachment string) string {
	if strings.HasPrefix(attachment, "output:") {
		return fmt.Sprintf("?set acl-plugin interface %s output acl %d", nativeLANInterface(strings.TrimPrefix(attachment, "output:")), aclID)
	}
	return fmt.Sprintf("?set acl-plugin interface %s input acl %d", nativeLANInterface(strings.TrimPrefix(attachment, "input:")), aclID)
}

func flowAttachPolicerCommand(policerName, attachment string) string {
	if strings.HasPrefix(attachment, "output:") {
		return fmt.Sprintf("?policer output name %s %s", policerName, nativeLANInterface(strings.TrimPrefix(attachment, "output:")))
	}
	return fmt.Sprintf("?policer input name %s %s", policerName, nativeLANInterface(strings.TrimPrefix(attachment, "input:")))
}

func nativeLANInterface(name string) string {
	if strings.TrimSpace(name) == "host-$LY_ROUTE_LAN_INTERFACE" {
		return "lyroute-$LY_ROUTE_LAN_INTERFACE"
	}
	return name
}

func firstOrAny(values []string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "any"
}

func natStaticMappingCommands(mapping nat.StaticMapping) []string {
	commands := []string{
		"?nat44 plugin enable",
		"?set interface nat44 in lyroute-$LY_ROUTE_LAN_INTERFACE",
	}
	if mapping.WANInterface != "" {
		commands = append(commands, fmt.Sprintf("?set interface nat44 out %s", mapping.WANInterface))
	}
	commands = append(commands,
		fmt.Sprintf("?nat44 add static mapping local %s external %s", mapping.InternalAddress, mapping.ExternalAddress),
		"show nat44 static mappings",
		"show nat44 sessions",
	)
	return commands
}

func natPortMappingCommands(mapping nat.PortMapping) []string {
	commands := []string{
		"?nat44 plugin enable",
		"?set interface nat44 in lyroute-$LY_ROUTE_LAN_INTERFACE",
	}
	if mapping.WANInterface != "" {
		commands = append(commands, fmt.Sprintf("?set interface nat44 out %s", mapping.WANInterface))
	}
	if mapping.Hairpin {
		commands = append(commands, fmt.Sprintf("?nat44 add address %s", mapping.ExternalAddress), "show nat44 addresses")
	}
	mappingCommand := fmt.Sprintf("nat44 add static mapping %s local %s %d external %s %d", mapping.Protocol, mapping.InternalHost, mapping.InternalPort, mapping.ExternalAddress, mapping.ExternalPort)
	commands = append(commands,
		"show nat44 static mappings",
		fmt.Sprintf("?%s del", mappingCommand),
		fmt.Sprintf("?%s", mappingCommand),
		"show nat44 static mappings",
		fmt.Sprintf("show nat44 static mappings | include %s", mapping.ExternalAddress),
		fmt.Sprintf("show nat44 static mappings | include %s", safeTag(mapping.ID)),
		"show nat44 sessions",
	)
	if mapping.Hairpin {
		commands = append(commands, "show nat44 sessions | include hairpin")
	}
	return commands
}

func routePolicyCommands(policy trafficpolicy.RoutePolicy, wanGroups map[string]trafficpolicy.WANGroup) []string {
	aclID := stableID("route-acl:"+policy.ID, 10000, 49999)
	policyID := stableID("route-abf:"+policy.ID, 10000, 8999)
	tableID := stableID("route-table:"+policy.ID, 50000, 49999)
	groupTableID := 0
	if _, ok := wanGroups[policy.Egress]; ok {
		groupTableID = wanGroupTableID(policy.Egress)
	}
	commands := aclMatchCommands(aclID, policy.ID, policy.Match, routeACLAction(policy.Action))
	commands = append(commands,
		fmt.Sprintf("?ip table add %d", tableID),
		fmt.Sprintf("?set ip flow-hash table %d src dst sport dport proto", tableID),
	)
	if policy.Action != "deny" {
		routeTarget := routeNextHop(policy)
		if groupTableID > 0 {
			routeTarget = fmt.Sprintf("ip4-lookup-in-table %d", groupTableID)
		}
		commands = append(commands, fmt.Sprintf("?ip route add table %d 0.0.0.0/0 via %s", tableID, routeTarget))
	}
	abfVia := routeNextHop(policy)
	if groupTableID > 0 {
		abfVia = fmt.Sprintf("ip4-lookup-in-table %d", groupTableID)
	}
	commands = append(commands,
		fmt.Sprintf("?abf policy add id %d acl %d via %s", policyID, aclID, abfVia),
		fmt.Sprintf("?abf attach ip4 policy %d priority %d lyroute-$LY_ROUTE_LAN_INTERFACE", policyID, vppABFPriority(policy.Priority)),
		fmt.Sprintf("show acl-plugin acl index %d", aclID),
		fmt.Sprintf("show abf policy %d", policyID),
		"show abf attach lyroute-$LY_ROUTE_LAN_INTERFACE",
		fmt.Sprintf("show ip fib table %d", tableID),
	)
	if groupTableID > 0 {
		commands = append(commands, fmt.Sprintf("show ip fib table %d", groupTableID))
	}
	return commands
}

func vppABFPriority(priority int) int {
	// DNS fixed-egress overrides reserve the negative logical range beginning at
	// -100000. VPP accepts only non-negative ABF priorities, so preserve the
	// generated rule order in a dedicated low range ahead of ordinary PBR.
	if priority <= -100000 {
		mapped := priority + 100000
		if mapped < 0 {
			return 0
		}
		if mapped > 255 {
			return 255
		}
		return mapped
	}
	if priority < 0 {
		return 0
	}
	if priority > 255 {
		return 255
	}
	return priority
}

func securityACLCommands(acl trafficpolicy.SecurityACL, wanIngressInterfaces ...string) []string {
	aclID := stableID("security-acl:"+acl.ID, 50000, 49999)
	commands := aclMatchCommands(aclID, acl.ID, acl.Match, acl.Action)
	direction := "input"
	interfaceName := "lyroute-$LY_ROUTE_LAN_INTERFACE"
	wanIngressInterface := ""
	if len(wanIngressInterfaces) > 0 {
		wanIngressInterface = wanIngressInterfaces[0]
	}
	if acl.ID == "sec-acl-default-deny-wan" && strings.TrimSpace(wanIngressInterface) != "" {
		interfaceName = strings.TrimSpace(wanIngressInterface)
	}
	if acl.Match.Direction == "output" {
		direction = "output"
	}
	commands = append(commands,
		fmt.Sprintf("?set interface %s acl intfc %s ip4-table %d", direction, interfaceName, aclID),
		fmt.Sprintf("show acl-plugin acl index %d", aclID),
		"show interface lyroute-$LY_ROUTE_LAN_INTERFACE",
	)
	return commands
}

func wanGroupCommands(group trafficpolicy.WANGroup) []string {
	tableID := wanGroupTableID(group.ID)
	commands := []string{fmt.Sprintf("?ip table add %d", tableID)}
	if group.Mode != trafficpolicy.WANGroupPrimaryBackup {
		commands = append(commands, fmt.Sprintf("?set ip flow-hash table %d src dst sport dport proto", tableID))
	}
	for index, member := range group.Members {
		path := group.Paths[member]
		via := strings.TrimSpace(path.VPPInterface)
		if via == "" {
			via = member
		}
		if nextHop := strings.TrimSpace(path.NextHop); nextHop != "" {
			via = nextHop + " " + via
		}
		weight := group.Weights[member]
		if weight < 1 {
			weight = 1
		}
		preference := 0
		if group.Mode == trafficpolicy.WANGroupPrimaryBackup {
			weight = 1
			preference = index
		}
		commands = append(commands, fmt.Sprintf("?ip route add table %d 0.0.0.0/0 via %s weight %d preference %d", tableID, via, weight, preference))
	}
	commands = append(commands, fmt.Sprintf("show ip fib table %d", tableID))
	return commands
}

func wanGroupTableID(id string) int {
	return stableID("wan-group:"+id, 50000, 49999)
}

func aclMatchCommands(aclID int, resource string, match trafficpolicy.Match, action string) []string {
	rules := []string{}
	for _, source := range nonEmptyList(match.Sources, "0.0.0.0/0") {
		for _, destination := range nonEmptyList(match.Destinations, "0.0.0.0/0") {
			for _, protocol := range nonEmptyList(match.Protocols, "any") {
				protocol = aclProtocolValue(protocol)
				for _, sourcePort := range nonEmptyList(match.SourcePorts, "any") {
					for _, destPort := range nonEmptyList(match.DestPorts, "any") {
						rules = append(rules, fmt.Sprintf("%s src %s dst %s proto %s sport %s dport %s", action, source, destination, protocol, portRange(sourcePort), portRange(destPort)))
					}
				}
			}
		}
	}
	return []string{fmt.Sprintf("?set acl-plugin acl index %d %s tag ly-route-%s", aclID, strings.Join(rules, ", "), safeTag(resource))}
}

func aclProtocolValue(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "any":
		return "0"
	case "icmp":
		return "1"
	case "tcp":
		return "6"
	case "udp":
		return "17"
	case "icmpv6":
		return "58"
	default:
		return protocol
	}
}

func routeACLAction(action string) string {
	if action == "deny" {
		return "deny"
	}
	return "permit"
}

func routeNextHop(policy trafficpolicy.RoutePolicy) string {
	if policy.Path != nil {
		if nextHop := strings.TrimSpace(policy.Path.NextHop); nextHop != "" {
			return nextHop + " " + strings.TrimSpace(policy.Path.VPPInterface)
		}
		if via := strings.TrimSpace(policy.Path.VPPInterface); via != "" {
			return via
		}
	}
	if policy.NextHop != "" {
		return policy.NextHop
	}
	if policy.Egress != "" {
		return policy.Egress
	}
	return "local"
}

func nonEmptyList(items []string, fallback string) []string {
	if len(items) == 0 {
		return []string{fallback}
	}
	return items
}

func portRange(value string) string {
	if value == "" || value == "any" {
		return "0-65535"
	}
	if strings.Contains(value, "-") {
		return value
	}
	return value + "-" + value
}

func stableID(value string, minimum, span int) int {
	if span <= 0 {
		return minimum
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return minimum + int(binary.BigEndian.Uint32(digest[:4])%uint32(span))
}

func safeTag(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
			continue
		}
		if builder.Len() > 0 {
			builder.WriteByte('_')
		}
	}
	result := strings.Trim(builder.String(), "_")
	if result == "" {
		return "default"
	}
	return result
}

func qosClassValue(target flow.Target) int {
	class := strings.ToLower(strings.TrimSpace(target.Class))
	if class == "" {
		class = strings.ToLower(strings.TrimSpace(target.RuleID))
	}
	return stableID("qos-class:"+class, 1, 62)
}

func dscpValue(value string) int {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0
	}
	if parsed, err := strconv.Atoi(trimmed); err == nil && parsed >= 0 && parsed <= 63 {
		return parsed
	}
	if value, ok := map[string]int{
		"CS0":  0,
		"CS1":  8,
		"AF11": 10,
		"AF12": 12,
		"AF13": 14,
		"CS2":  16,
		"AF21": 18,
		"AF22": 20,
		"AF23": 22,
		"CS3":  24,
		"AF31": 26,
		"AF32": 28,
		"AF33": 30,
		"CS4":  32,
		"AF41": 34,
		"AF42": 36,
		"AF43": 38,
		"CS5":  40,
		"EF":   46,
		"CS6":  48,
		"CS7":  56,
	}[strings.ToUpper(trimmed)]; ok {
		return value
	}
	return 0
}

func NormalizeRetval(operation, requestID string, retval int32) error {
	if retval == 0 {
		return nil
	}
	return VPPError{Operation: strings.TrimSpace(operation), RequestID: strings.TrimSpace(requestID), Retval: retval}
}

func ConsumeMultipart(ctx context.Context, operation Operation, stream MultipartStream) ([]Reply, error) {
	var replies []Reply
	for {
		reply, err := stream.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return replies, nil
		}
		if err != nil {
			return nil, VPPError{Operation: operation.Name, RequestID: operation.RequestID, Err: err}
		}
		if err := NormalizeRetval(operation.Name, operation.RequestID, reply.Retval); err != nil {
			return nil, err
		}
		if reply.Done || reply.ControlPing {
			return replies, nil
		}
		replies = append(replies, reply)
	}
}
