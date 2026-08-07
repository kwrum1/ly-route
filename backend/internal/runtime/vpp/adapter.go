package vpp

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"sort"
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
	RequestID           string
	NativePath          NativePathRequest
	AddressAssignments  []AddressAssignment
	SmartQoSAssignments []AddressAssignment `json:"smart_qos_assignments,omitempty"`
	SmartQoSEnabled     bool                `json:"smart_qos_enabled,omitempty"`
	Interfaces          []InterfaceState
	Bonds               []BondState
	Proxy               proxy.CompiledEgress
	Flow                flow.CompiledIntent
	NAT                 nat.CompiledConfig
	Policy              trafficpolicy.Config
	Security            SecurityGeneration
	DNSInterception     bool
	DNSServiceNetworks  []DNSServiceNetwork `json:"dns_service_networks,omitempty"`
	DataplanePrepared   bool                `json:"-"`
}

type DNSTransparentInterception struct {
	LANInterface string   `json:"lan_interface"`
	IPv4Prefixes []string `json:"ipv4_prefixes,omitempty"`
	IPv6Prefixes []string `json:"ipv6_prefixes,omitempty"`
}

type ManagementLCP struct {
	Enabled                 bool   `json:"enabled"`
	VPPInterface            string `json:"vpp_interface"`
	HostInterface           string `json:"host_interface"`
	IPv4BroadcastLocal      bool   `json:"ipv4_broadcast_local,omitempty"`
	IPv4DHCPBroadcastBypass bool   `json:"ipv4_dhcp_broadcast_bypass,omitempty"`
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
	needsDataplane := plan.DNSInterception || len(plan.DNSServiceNetworks) > 0 || len(plan.NativePath.Assignments) > 0 || len(plan.AddressAssignments) > 0 || len(plan.Proxy.VPPSteering) > 0 || len(plan.Flow.VPPGroups) > 0 || len(plan.Flow.Targets) > 0 || len(plan.NAT.StaticMappings) > 0 || len(plan.NAT.PortMappings) > 0 || len(plan.Policy.WANGroups) > 0 || len(plan.Policy.RoutePolicies) > 0 || len(plan.Policy.SecurityACLs) > 0 || len(plan.Security.ACLs) > 0 || len(plan.Security.MACIP) > 0 || len(plan.Security.AttackRules) > 0
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
	for _, assignment := range plan.SmartQoSAssignments {
		linuxInterface := strings.TrimSpace(assignment.LinuxInterface)
		vppInterface := strings.TrimSpace(assignment.VPPInterface)
		attachment, proven := attachments[linuxInterface]
		prerequisites = append(prerequisites,
			prerequisite("smart_qos_assignment_management_excluded", linuxInterface, management != "" && (plan.NativePath.ManagementShared || linuxInterface != management), "smart QoS assignment references the management interface"),
			prerequisite("smart_qos_assignment_proven", linuxInterface, proven, "smart QoS assignment has no selected runtime-proven native attachment"),
			prerequisite("smart_qos_assignment_vpp_interface_matches", linuxInterface, proven && vppInterface == attachment.VPPInterface, "smart QoS assignment VPP interface does not match the selected native attachment"),
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
	seenLANControlPlanes := map[string]bool{}
	for _, assignment := range plan.AddressAssignments {
		if !strings.EqualFold(strings.TrimSpace(assignment.Role), "lan") {
			continue
		}
		vppInterface := strings.TrimSpace(assignment.VPPInterface)
		if vppInterface == "" || seenLANControlPlanes[vppInterface] {
			continue
		}
		seenLANControlPlanes[vppInterface] = true
		controlPlane := ManagementLCP{
			Enabled:                 true,
			VPPInterface:            vppInterface,
			HostInterface:           LANControlPlaneHostInterface(assignment.LinuxInterface),
			IPv4BroadcastLocal:      true,
			IPv4DHCPBroadcastBypass: true,
		}
		operations = append(operations, Operation{
			Name:           "vpp.lan-control-lcp",
			RequestID:      plan.RequestID,
			Resource:       assignment.ID,
			Payload:        controlPlane,
			VPPCtlCommands: managementLCPCommands(controlPlane),
		})
	}
	for _, assignment := range plan.AddressAssignments {
		operation := Operation{Name: "vpp.interface.address", RequestID: plan.RequestID, Resource: assignment.ID, Payload: assignment}
		operation.VPPCtlCommands = interfaceAddressCommands(assignment)
		operations = append(operations, operation)
	}
	if nativePath.SmartQoS {
		smartQoSAssignments := plan.SmartQoSAssignments
		if len(smartQoSAssignments) == 0 {
			smartQoSAssignments = plan.AddressAssignments
		}
		smartQoSOperations, smartQoSPrerequisites := buildSmartQoSOperations(plan.RequestID, smartQoSAssignments)
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
	for _, network := range plan.DNSServiceNetworks {
		if err := validateDNSServiceNetwork(network); err != nil {
			return nil, err
		}
		if !vppCommandTokensSafe(network.UnderlayRoute) {
			return nil, fmt.Errorf("DNS service network %q has no safe VPP underlay route", network.UpstreamID)
		}
		operation := Operation{Name: "vpp.dns-service.network", RequestID: plan.RequestID, Resource: network.UpstreamID, Payload: network}
		operation.VPPCtlCommands = dnsServiceNetworkCommands(network)
		operations = append(operations, operation)
	}
	// A proxy service network uses the selected WAN group table as its
	// underlay, so create its VPP handoff after WAN groups are present.
	for _, steering := range plan.Proxy.VPPSteering {
		operation := Operation{Name: steering.TargetKind, RequestID: plan.RequestID, Resource: steering.EgressID, Payload: steering}
		operation.VPPCtlCommands = proxySteeringCommands(steering)
		operations = append(operations, operation)
	}
	routePolicies := orderedRoutePoliciesForVPP(plan.Policy.RoutePolicies, wanGroups)
	routeOptions := buildRoutePolicyCommandOptions(routePolicies, wanGroups)
	if err := validateLargeRoutePolicyFallback(routePolicies, routeOptions); err != nil {
		return nil, err
	}
	for _, policy := range routePolicies {
		options := routeOptions[policy.ID]
		operation := Operation{Name: "vpp.route-policy", RequestID: plan.RequestID, Resource: policy.ID, Payload: policy}
		operation.VPPCtlCommands = routePolicyCommandsWithOptions(policy, wanGroups, options)
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
	// Route/ACL/ABF operations attach to the configured LAN dataplane
	// interface.  The first native attachment can be the management or WAN
	// port, so deriving the ingress from attachment order silently produced an
	// unresolved `lyroute-$LY_ROUTE_LAN_INTERFACE` in production.
	lanVPPInterface := ""
	for _, assignment := range plan.AddressAssignments {
		if strings.EqualFold(strings.TrimSpace(assignment.Role), "lan") {
			lanVPPInterface = strings.TrimSpace(assignment.VPPInterface)
			if lanVPPInterface != "" {
				break
			}
		}
	}
	if lanVPPInterface == "" && len(nativePath.Attachments) > 0 {
		lanVPPInterface = nativePath.Attachments[0].VPPInterface
	}
	if lanVPPInterface != "" {
		operations = rewriteOperationsInterface(operations, lanVPPInterface)
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
	commands := []string{"lcp lcp-sync on", "show lcp"}
	if lcp.Enabled {
		commands = append(commands, fmt.Sprintf("lcp create %s host-if %s", lcp.VPPInterface, lcp.HostInterface))
		if lcp.IPv4BroadcastLocal {
			commands = append(commands, "ip route add 255.255.255.255/32 via local", "show ip fib 255.255.255.255")
		}
		commands = append(commands, "show lcp")
	}
	return commands
}

// LANControlPlaneHostInterface returns the deterministic Linux interface paired
// with a VPP-owned LAN. The name is short enough for Linux IFNAMSIZ and stable
// across reboots so host services such as Kea and SmartDNS can bind safely.
func LANControlPlaneHostInterface(linuxInterface string) string {
	linuxInterface = strings.TrimSpace(strings.ToLower(linuxInterface))
	var suffix strings.Builder
	lossy := false
	for _, value := range linuxInterface {
		if value >= 'a' && value <= 'z' || value >= '0' && value <= '9' {
			suffix.WriteRune(value)
			continue
		}
		if value > unicode.MaxASCII {
			lossy = true
		}
		if suffix.Len() > 0 && !strings.HasSuffix(suffix.String(), "-") {
			suffix.WriteByte('-')
		}
	}
	cleaned := strings.Trim(suffix.String(), "-")
	if !lossy && cleaned != "" && len("lylan-"+cleaned) <= 15 {
		return "lylan-" + cleaned
	}
	digest := sha256.Sum256([]byte(linuxInterface))
	return fmt.Sprintf("lylan-%x", digest[:4])
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
		if cidr == "" || seenPrefixes[cidr] {
			continue
		}
		seenPrefixes[cidr] = true
		if strings.Contains(cidr, ":") {
			result.IPv6Prefixes = append(result.IPv6Prefixes, cidr)
		} else {
			result.IPv4Prefixes = append(result.IPv4Prefixes, cidr)
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
		fmt.Sprintf("ip table add %d", dnsIPv4TableID),
		fmt.Sprintf("ip route add table %d 0.0.0.0/0 via local", dnsIPv4TableID),
		fmt.Sprintf("abf policy add id %d acl %d via ip4-lookup-in-table %d", v4Policy, v4ACL, dnsIPv4TableID),
		fmt.Sprintf("abf attach ip4 policy %d priority 0 %s", v4Policy, interception.LANInterface),
		fmt.Sprintf("ip6 table add %d", dnsIPv6TableID),
		fmt.Sprintf("ip route add table %d ::/0 via local", dnsIPv6TableID),
	}
	for _, prefix := range interception.IPv4Prefixes {
		commands = append(commands, fmt.Sprintf("ip route add table %d %s via %s", dnsIPv4TableID, prefix, interception.LANInterface))
	}
	for _, prefix := range interception.IPv6Prefixes {
		commands = append(commands, fmt.Sprintf("ip route add table %d %s via %s", dnsIPv6TableID, prefix, interception.LANInterface))
	}
	return append(commands,
		fmt.Sprintf("set acl-plugin acl index %d permit src ::/0 dst ::/0 proto 17 sport 0-65535 dport 53-53, permit src ::/0 dst ::/0 proto 6 sport 0-65535 dport 53-53 tag ly-route-dns-transparent-v6", v6ACL),
		fmt.Sprintf("abf policy add id %d acl %d via ip6-lookup-in-table %d", v6Policy, v6ACL, dnsIPv6TableID),
		fmt.Sprintf("abf attach ip6 policy %d priority 0 %s", v6Policy, interception.LANInterface),
		fmt.Sprintf("show abf policy %d", v4Policy), fmt.Sprintf("show abf policy %d", v6Policy),
		fmt.Sprintf("show abf attach %s", interception.LANInterface), "show acl-plugin acl",
		fmt.Sprintf("show ip fib table %d", dnsIPv4TableID), fmt.Sprintf("show ip6 fib table %d", dnsIPv6TableID))
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
				// VPP restarts recreate the DPDK/VMXNET3 device with admin state
				// down. Explicitly bring every native DPDK port back up so a
				// reboot/replay does not silently lose the WAN carrier.
				fmt.Sprintf("set interface state %s up", attachment.VPPInterface),
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
	case attachment.Hook == NativeHookVMXNET3 && attachment.Mode == NativeModeVMXNET3VFIO:
		commands = []string{
			fmt.Sprintf("?create interface vmxnet3 %s", attachment.PCIAddress),
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
	resource := steering.EgressID
	if strings.TrimSpace(resource) == "" {
		resource = string(steering.Handoff)
	}
	if steering.TargetKind == "vpp.proxy-service.network" {
		network := steering.ServiceNetwork
		if strings.TrimSpace(network.EgressID) == "" {
			network = proxy.ServiceNetworkForEgressID(resource)
		}
		underlay := strings.TrimSpace(steering.UnderlayRoute)
		if underlay == "" {
			underlay = strings.TrimSpace(network.UnderlayRoute)
		}
		if underlay == "" {
			underlay = "local"
		}
		underlayPath := routePathVia("", "", underlay)
		return []string{
			fmt.Sprintf("?create tap id %d host-if-name %s no-gso", network.IngressTapID, network.IngressHostInterface),
			fmt.Sprintf("?set interface name tap%d %s", network.IngressTapID, network.IngressVPPInterface),
			fmt.Sprintf("?set interface state %s up", network.IngressVPPInterface),
			fmt.Sprintf("?set interface mtu packet %d %s", network.MTU, network.IngressVPPInterface),
			fmt.Sprintf("?set interface ip address %s %s", network.IngressVPPInterface, network.IngressCIDR),
			fmt.Sprintf("?create tap id %d host-if-name %s no-gso", network.EgressTapID, network.EgressHostInterface),
			fmt.Sprintf("?set interface name tap%d %s", network.EgressTapID, network.EgressVPPInterface),
			fmt.Sprintf("?set interface state %s up", network.EgressVPPInterface),
			fmt.Sprintf("?set interface mtu packet %d %s", network.MTU, network.EgressVPPInterface),
			fmt.Sprintf("?ip table add %d", network.OutboundTableID),
			fmt.Sprintf("?set interface ip table %s %d", network.EgressVPPInterface, network.OutboundTableID),
			fmt.Sprintf("?set interface ip address %s %s", network.EgressVPPInterface, network.EgressCIDR),
			fmt.Sprintf("?ip route del table %d 0.0.0.0/0", network.OutboundTableID),
			fmt.Sprintf("?ip route add table %d 0.0.0.0/0 via %s", network.OutboundTableID, underlayPath),
			"?nat44 plugin enable",
			fmt.Sprintf("?set interface nat44 in %s del", network.EgressVPPInterface),
			fmt.Sprintf("show interface address %s", network.IngressVPPInterface),
			fmt.Sprintf("show interface address %s", network.EgressVPPInterface),
			fmt.Sprintf("show ip fib table %d", network.OutboundTableID),
			"show nat44 interfaces",
			"show tap",
		}
	}
	interfaceName := "lyroute-$LY_ROUTE_LAN_INTERFACE"
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

func dnsServiceNetworkCommands(network DNSServiceNetwork) []string {
	underlayPath := routePathVia("", "", network.UnderlayRoute)
	return []string{
		fmt.Sprintf("?create tap id %d host-if-name %s no-gso", network.TapID, network.HostInterface),
		fmt.Sprintf("?set interface name tap%d %s", network.TapID, network.VPPInterface),
		fmt.Sprintf("?set interface state %s up", network.VPPInterface),
		fmt.Sprintf("?set interface mtu packet %d %s", network.MTU, network.VPPInterface),
		fmt.Sprintf("?ip table add %d", network.TableID),
		fmt.Sprintf("?set interface ip table %s %d", network.VPPInterface, network.TableID),
		fmt.Sprintf("?set interface ip address %s %s", network.VPPInterface, network.CIDR),
		fmt.Sprintf("?ip route del table %d 0.0.0.0/0", network.TableID),
		fmt.Sprintf("?ip route add table %d 0.0.0.0/0 via %s", network.TableID, underlayPath),
		"?nat44 plugin enable",
		fmt.Sprintf("?set interface nat44 in %s del", network.VPPInterface),
		fmt.Sprintf("show interface address %s", network.VPPInterface),
		fmt.Sprintf("show ip fib table %d", network.TableID),
		"show nat44 interfaces",
		"show tap",
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
		commands = append(commands,
			fmt.Sprintf("?set interface nat44 out %s del", mapping.WANInterface),
			fmt.Sprintf("?set interface nat44 out %s output-feature", mapping.WANInterface),
		)
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
		commands = append(commands,
			fmt.Sprintf("?set interface nat44 out %s del", mapping.WANInterface),
			fmt.Sprintf("?set interface nat44 out %s output-feature", mapping.WANInterface),
		)
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

// routePolicyCommandOptions describes the optional FIB-table implementation
// used for a destination-only route policy chain.  The legacy ACL path is
// deliberately retained for policies that contain source/port/protocol
// predicates; those predicates cannot be represented by a plain FIB lookup.
type routePolicyCommandOptions struct {
	optimizedIPv4 bool
	defaultVia    string
}

// orderedRoutePoliciesForVPP creates the lower-priority tables first.  A
// chained table can only install a lookup DPO after its next table exists.
// The policy priority in the generated ABF attachment remains unchanged, so
// this is only an implementation ordering detail.
func orderedRoutePoliciesForVPP(policies []trafficpolicy.RoutePolicy, wanGroups map[string]trafficpolicy.WANGroup) []trafficpolicy.RoutePolicy {
	ordered := append([]trafficpolicy.RoutePolicy(nil), policies...)
	options := buildRoutePolicyCommandOptions(ordered, wanGroups)
	if len(options) != len(ordered) || len(ordered) < 2 {
		return ordered
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Priority > ordered[j].Priority
	})
	return ordered
}

// buildRoutePolicyCommandOptions recognizes a complete, destination-only
// route chain.  It is intentionally conservative: if one policy needs an
// ACL predicate, every policy falls back to the ACL implementation so that
// the relative semantics cannot be changed by an optimization.
func buildRoutePolicyCommandOptions(policies []trafficpolicy.RoutePolicy, wanGroups map[string]trafficpolicy.WANGroup) map[string]routePolicyCommandOptions {
	options := make(map[string]routePolicyCommandOptions)
	if len(policies) == 0 {
		return options
	}
	ordered := append([]trafficpolicy.RoutePolicy(nil), policies...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Priority < ordered[j].Priority })
	for _, policy := range ordered {
		if !routePolicyFIBChainEligible(policy) || len(routePolicyIPv4Destinations(policy.Match.Destinations)) == 0 {
			return map[string]routePolicyCommandOptions{}
		}
	}
	last := ordered[len(ordered)-1]
	if !routePolicyHasIPv4CatchAll(last.Match.Destinations) {
		return map[string]routePolicyCommandOptions{}
	}
	for index, policy := range ordered {
		defaultVia := routePolicyTarget(policy, wanGroups)
		if index+1 < len(ordered) {
			defaultVia = fmt.Sprintf("ip4-lookup-in-table %d", stableID("route-table:"+ordered[index+1].ID, 50000, 49999))
		}
		options[policy.ID] = routePolicyCommandOptions{optimizedIPv4: true, defaultVia: defaultVia}
	}
	return options
}

// orderedRoutePolicySubsetForVPP preserves the dependency order of the full
// chain while emitting only the policies changed by an incremental apply.
// Without the full context, a one-policy repair would be mistaken for an
// incomplete chain and could fall back to a giant ACL.
func orderedRoutePolicySubsetForVPP(routes, context []trafficpolicy.RoutePolicy, options map[string]routePolicyCommandOptions, wanGroups map[string]trafficpolicy.WANGroup) []trafficpolicy.RoutePolicy {
	ordered := append([]trafficpolicy.RoutePolicy(nil), routes...)
	if len(options) != len(context) || len(ordered) < 2 {
		return orderedRoutePoliciesForVPP(ordered, wanGroups)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority == ordered[j].Priority {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Priority > ordered[j].Priority
	})
	return ordered
}

const maxRoutePolicyACLIPv4Prefixes = 256

// Large destination sets must never be serialized into one ACL command. A
// complete destination-only policy chain uses VPP FIB tables; if it cannot be
// proven safe, fail closed with a useful error instead of hanging vppctl.
func validateLargeRoutePolicyFallback(routes []trafficpolicy.RoutePolicy, options map[string]routePolicyCommandOptions) error {
	for _, route := range routes {
		prefixes := routePolicyIPv4Destinations(route.Match.Destinations)
		if len(prefixes) <= maxRoutePolicyACLIPv4Prefixes {
			continue
		}
		if option, optimized := options[route.ID]; optimized && option.optimizedIPv4 {
			continue
		}
		return fmt.Errorf("route policy %q has %d IPv4 destinations but no verified native FIB chain; refusing large ACL fallback", route.ID, len(prefixes))
	}
	return nil
}

func routePolicyFIBChainEligible(policy trafficpolicy.RoutePolicy) bool {
	if strings.ToLower(strings.TrimSpace(policy.Action)) != "route" {
		return false
	}
	match := policy.Match
	return routePolicyAnySelector(match.Sources) &&
		routePolicyAnySelector(match.Protocols) &&
		routePolicyAnySelector(match.SourcePorts) &&
		routePolicyAnySelector(match.DestPorts)
}

func routePolicyAnySelector(values []string) bool {
	if len(values) == 0 {
		return true
	}
	for _, value := range values {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "", "any", "0.0.0.0/0", "::/0":
			continue
		default:
			return false
		}
	}
	return true
}

func routePolicyIPv4Destinations(values []string) []string {
	seen := make(map[string]struct{})
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" || strings.EqualFold(value, "any") || value == "0.0.0.0/0" {
			seen["0.0.0.0/0"] = struct{}{}
			continue
		}
		if prefix, err := netip.ParsePrefix(value); err == nil && prefix.Addr().Is4() {
			seen[prefix.Masked().String()] = struct{}{}
			continue
		}
		if address, err := netip.ParseAddr(value); err == nil && address.Is4() {
			seen[(netip.PrefixFrom(address, 32)).String()] = struct{}{}
		}
	}
	if len(seen) == 0 && len(values) == 0 {
		seen["0.0.0.0/0"] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for prefix := range seen {
		result = append(result, prefix)
	}
	sort.Strings(result)
	return result
}

func routePolicyHasIPv4CatchAll(values []string) bool {
	for _, value := range routePolicyIPv4Destinations(values) {
		if value == "0.0.0.0/0" {
			return true
		}
	}
	return false
}

func routePolicyTarget(policy trafficpolicy.RoutePolicy, wanGroups map[string]trafficpolicy.WANGroup) string {
	if wanGroups != nil {
		if group, ok := wanGroups[policy.Egress]; ok {
			return fmt.Sprintf("ip4-lookup-in-table %d", wanGroupTableID(group.ID))
		}
	}
	return routeNextHop(policy)
}

// routePolicyCommands is kept as the compatibility wrapper used by focused
// unit tests and the route/WAN lifecycle.  Production plan construction uses
// routePolicyCommandsWithOptions so large provider datasets never become one
// VPP CLI line.
func routePolicyCommands(policy trafficpolicy.RoutePolicy, wanGroups map[string]trafficpolicy.WANGroup) []string {
	return routePolicyCommandsWithOptions(policy, wanGroups, routePolicyCommandOptions{})
}

func routePolicyCommandsWithOptions(policy trafficpolicy.RoutePolicy, wanGroups map[string]trafficpolicy.WANGroup, options routePolicyCommandOptions) []string {
	aclID := stableID("route-acl:"+policy.ID, 10000, 49999)
	policyID := stableID("route-abf:"+policy.ID, 10000, 8999)
	tableID := stableID("route-table:"+policy.ID, 50000, 49999)
	groupTableID := 0
	if _, ok := wanGroups[policy.Egress]; ok {
		groupTableID = wanGroupTableID(policy.Egress)
	}
	match := policy.Match
	if options.optimizedIPv4 {
		match = trafficpolicy.Match{
			Sources:      append([]string(nil), match.Sources...),
			Destinations: []string{"0.0.0.0/0"},
			Protocols:    []string{"any"},
			SourcePorts:  []string{"any"},
			DestPorts:    []string{"any"},
		}
	}
	commands := aclMatchCommands(aclID, policy.ID, match, routeACLAction(policy.Action))
	commands = append(commands,
		fmt.Sprintf("?ip table add %d", tableID),
		fmt.Sprintf("?set ip flow-hash table %d src dst sport dport proto", tableID),
	)
	if policy.Action != "deny" {
		routeTarget := routePolicyTarget(policy, wanGroups)
		if options.optimizedIPv4 {
			commands = append(commands, vppRouteBatchBegin)
			for _, prefix := range routePolicyIPv4Destinations(policy.Match.Destinations) {
				commands = append(commands, fmt.Sprintf("ip route add table %d %s via %s", tableID, prefix, routeTarget))
			}
			commands = append(commands, vppRouteBatchEnd)
			if strings.TrimSpace(options.defaultVia) != "" {
				routeTarget = options.defaultVia
			}
		}
		commands = append(commands, fmt.Sprintf("?ip route add table %d 0.0.0.0/0 via %s", tableID, routeTarget))
	}
	abfVia := routePolicyTarget(policy, wanGroups)
	if options.optimizedIPv4 {
		abfVia = fmt.Sprintf("ip4-lookup-in-table %d", tableID)
	} else if groupTableID > 0 {
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
	interfaceName := "lyroute-$LY_ROUTE_LAN_INTERFACE"
	wanIngressInterface := ""
	if len(wanIngressInterfaces) > 0 {
		wanIngressInterface = wanIngressInterfaces[0]
	}
	if acl.ID == "sec-acl-default-deny-wan" && strings.TrimSpace(wanIngressInterface) != "" {
		interfaceName = strings.TrimSpace(wanIngressInterface)
	}
	for _, direction := range securityDirections(acl.Match.Direction) {
		commands = append(commands, fmt.Sprintf("?set interface %s acl intfc %s ip4-table %d", direction, interfaceName, aclID))
	}
	commands = append(commands, fmt.Sprintf("show acl-plugin acl index %d", aclID), "show interface lyroute-$LY_ROUTE_LAN_INTERFACE")
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
		via := routePathVia(path.VPPInterface, path.NextHop, member)
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

// WANGroupTableID exposes the stable table identity to service-chain
// compilers that need to hand traffic into an already-built WAN group.
func WANGroupTableID(id string) int {
	return wanGroupTableID(id)
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
		return routePathVia(policy.Path.VPPInterface, policy.Path.NextHop, "")
	}
	if policy.NextHop != "" {
		return policy.NextHop
	}
	if policy.Egress != "" {
		return policy.Egress
	}
	return "local"
}

// configuredLANVPPInterface is only a last-resort recovery path for cleanup
// transactions created from an older persisted snapshot. Normal plans carry
// the resolved LAN interface in RouteWANGroupPlan. The explicit VPP variable
// lets an appliance remove a stale ABF attachment safely during an upgrade
// without guessing from an arbitrary physical port.
func configuredLANVPPInterface() string {
	for _, key := range []string{"LY_ROUTE_LAN_VPP_INTERFACE", "LY_ROUTE_LAN_INTERFACE"} {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" || strings.ContainsAny(value, " \t\r\n;$\"") {
			continue
		}
		if strings.HasPrefix(value, "lyroute-") {
			return value
		}
		return "lyroute-" + value
	}
	return ""
}

// routePathVia returns the VPP route path syntax for a resolved WAN path.
// VPP 25.10 accepts a PPPoE path only when its negotiated peer is supplied.
// An interface-only PPPoE ABF path can also crash `show abf policy`, so a
// temporarily incomplete session uses the native main-table lookup until the
// live peer readback is available. Ordinary interfaces keep their direct path.
func routePathVia(vppInterface, nextHop, fallback string) string {
	via := strings.TrimSpace(vppInterface)
	if via == "" {
		via = strings.TrimSpace(fallback)
	}
	if via == "" {
		return strings.TrimSpace(nextHop)
	}
	if nextHop = strings.TrimSpace(nextHop); nextHop != "" {
		return nextHop + " " + via
	}
	if strings.HasPrefix(strings.ToLower(via), "pppoe_session") {
		return "ip4-lookup-in-table 0"
	}
	return via
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
