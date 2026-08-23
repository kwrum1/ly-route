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
	// RetiredRoutePolicyIDs keeps disabled policy identities available to
	// reconciliation when an older runtime snapshot has already lost them.
	RetiredRoutePolicyIDs []string `json:"retired_route_policy_ids,omitempty"`
	Security              SecurityGeneration
	DNSInterception       bool
	DNSServiceNetworks    []DNSServiceNetwork `json:"dns_service_networks,omitempty"`
	DataplanePrepared     bool                `json:"-"`
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
	if planRequiresNAT(plan) {
		operations = append(operations, Operation{
			Name:           "vpp.nat44.initialize",
			RequestID:      plan.RequestID,
			Resource:       string(plan.NAT.Behavior),
			Payload:        plan.NAT.Behavior,
			VPPCtlCommands: natInitializeCommands(plan.NAT.Behavior),
		})
	}
	// VPPGroups are the executable, deduplicated representation of Targets.
	// Executing both creates the same ACL/policer twice for matched rules.
	for _, mapping := range plan.NAT.StaticMappings {
		operation := Operation{Name: "vpp.nat44-ed.static-mapping", RequestID: plan.RequestID, Resource: mapping.ID, Payload: mapping}
		operation.VPPCtlCommands = natStaticMappingCommandsForBehavior(plan.NAT.Behavior, mapping)
		operations = append(operations, operation)
	}
	for _, mapping := range plan.NAT.PortMappings {
		operation := Operation{Name: "vpp.nat44-ed.static-mapping", RequestID: plan.RequestID, Resource: mapping.ID, Payload: mapping}
		operation.VPPCtlCommands = natPortMappingCommandsForBehavior(plan.NAT.Behavior, mapping)
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
		operation.VPPCtlCommands = dnsServiceNetworkCommands(network, plan.NAT.Behavior)
		operations = append(operations, operation)
	}
	// A proxy service network uses the selected WAN group table as its
	// underlay, so create its VPP handoff after WAN groups are present.
	for _, steering := range plan.Proxy.VPPSteering {
		operation := Operation{Name: steering.TargetKind, RequestID: plan.RequestID, Resource: steering.EgressID, Payload: steering}
		operation.VPPCtlCommands = proxySteeringCommands(steering, plan.NAT.Behavior)
		operations = append(operations, operation)
	}
	routePolicies := orderedRoutePoliciesForVPP(plan.Policy.RoutePolicies, wanGroups)
	routeOptions := buildRoutePolicyCommandOptions(routePolicies, wanGroups)
	addRoutePolicyLocalDestinations(routeOptions, routePolicies, plan.AddressAssignments)
	if err := validateLargeRoutePolicyFallback(routePolicies, routeOptions, wanGroups); err != nil {
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
	commands := []string{
		"?set ly-route dns-intercept disable",
		fmt.Sprintf("?ip route del table %d 0.0.0.0/0", dnsIPv4TableID),
		fmt.Sprintf("?ip table add %d", dnsIPv4TableID),
		fmt.Sprintf("?ip route add table %d 0.0.0.0/0 via local", dnsIPv4TableID),
	}
	for _, prefix := range interception.IPv4Prefixes {
		commands = append(commands, fmt.Sprintf("?ip route add table %d %s via %s", dnsIPv4TableID, prefix, interception.LANInterface))
	}
	return append(commands,
		fmt.Sprintf("set ly-route dns-intercept interface %s table %d", interception.LANInterface, dnsIPv4TableID),
		"show ly-route dns-intercept",
		fmt.Sprintf("show ip fib table %d", dnsIPv4TableID))
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
				commands = append(commands, fmt.Sprintf("?set interface ip address del %s %s", name, cidr))
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
				// VPP restarts recreate DPDK devices with admin state down.
				// Explicitly bring every DPDK port back up so a
				// reboot/replay does not silently lose the WAN carrier.
				fmt.Sprintf("set interface state %s up", attachment.VPPInterface),
				fmt.Sprintf("show hardware-interfaces %s", attachment.VPPInterface),
				fmt.Sprintf("show interface %s", attachment.VPPInterface),
			},
		}
	}
	commands := []string(nil)
	switch {
	case attachment.Hook == NativeHookAFPacket && attachment.Mode == NativeModeAFPacket:
		commands = []string{
			// AF_PACKET is an acceptance-only fallback. Its virtual path does not
			// reliably complete checksum/GSO offloads, so packets must leave VPP
			// with their checksums already materialized.
			fmt.Sprintf("?create host-interface name %s cksum-gso-disable", attachment.LinuxInterface),
			fmt.Sprintf("?set interface name host-%s %s", attachment.LinuxInterface, attachment.VPPInterface),
		}
		// VPP assigns a locally administered address to AF_PACKET host interfaces.
		// Linux peers learn the physical NIC address, so their frames otherwise fail
		// VPP's L2 destination check. The probe persists that address in the
		// attachment, allowing replay to restore both LAN and WAN deterministically.
		if strings.TrimSpace(attachment.MACAddress) != "" {
			commands = append(commands, fmt.Sprintf("set interface mac address %s %s", attachment.VPPInterface, attachment.MACAddress))
		}
		commands = append(commands,
			fmt.Sprintf("set interface state %s up", attachment.VPPInterface),
			fmt.Sprintf("show hardware-interfaces %s", attachment.VPPInterface),
			fmt.Sprintf("show interface %s", attachment.VPPInterface),
		)
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

func proxySteeringCommands(steering proxy.VPPSteeringInstruction, behavior nat.Behavior) []string {
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
		commands := []string{
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
		}
		underlayFields := strings.Fields(underlay)
		if behavior == nat.BehaviorFullCone && len(underlayFields) > 0 {
			wanInterface := underlayFields[len(underlayFields)-1]
			commands = append(commands,
				fmt.Sprintf("?set interface nat44 ei in %s out %s output-feature del", network.EgressVPPInterface, wanInterface),
				fmt.Sprintf("?set interface nat44 ei in %s out %s", network.EgressVPPInterface, wanInterface),
				fmt.Sprintf("?nat44 ei add interface address %s", wanInterface),
			)
		} else {
			commands = append(commands,
				fmt.Sprintf("?set interface nat44 in %s del", network.EgressVPPInterface),
			)
		}
		commands = append(commands,
			fmt.Sprintf("show interface address %s", network.IngressVPPInterface),
			fmt.Sprintf("show interface address %s", network.EgressVPPInterface),
			fmt.Sprintf("show ip fib table %d", network.OutboundTableID),
		)
		if behavior == nat.BehaviorFullCone {
			commands = append(commands, "show nat44 ei interfaces")
		} else {
			commands = append(commands, "show nat44 interfaces")
		}
		return append(commands,
			"show tap",
		)
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

func dnsServiceNetworkCommands(network DNSServiceNetwork, behavior nat.Behavior) []string {
	underlayPath := routePathVia("", "", network.UnderlayRoute)
	underlayInterface := dnsServiceNetworkUnderlayInterface(network)
	commands := []string{
		fmt.Sprintf("?create tap id %d host-if-name %s no-gso", network.TapID, network.HostInterface),
		fmt.Sprintf("?set interface name tap%d %s", network.TapID, network.VPPInterface),
		fmt.Sprintf("?set interface state %s up", network.VPPInterface),
		fmt.Sprintf("?set interface mtu packet %d %s", network.MTU, network.VPPInterface),
		fmt.Sprintf("?ip table add %d", network.TableID),
		fmt.Sprintf("?set interface ip table %s %d", network.VPPInterface, network.TableID),
		fmt.Sprintf("?set interface ip address %s %s", network.VPPInterface, network.CIDR),
		fmt.Sprintf("?ip route del table %d 0.0.0.0/0", network.TableID),
		fmt.Sprintf("?ip route add table %d 0.0.0.0/0 via %s", network.TableID, underlayPath),
		fmt.Sprintf("show interface address %s", network.VPPInterface),
		fmt.Sprintf("show ip fib table %d", network.TableID),
		"show tap",
	}
	if behavior == nat.BehaviorFullCone {
		return append(commands,
			fmt.Sprintf("?set interface nat44 ei in %s out %s output-feature del", network.VPPInterface, underlayInterface),
			fmt.Sprintf("set interface nat44 ei in %s out %s", network.VPPInterface, underlayInterface),
			fmt.Sprintf("?nat44 ei add interface address %s", underlayInterface),
			"show nat44 ei interfaces",
		)
	}
	return append(commands,
		fmt.Sprintf("?set interface nat44 in %s out %s output-feature del", network.VPPInterface, underlayInterface),
		fmt.Sprintf("set interface nat44 in %s out %s", network.VPPInterface, underlayInterface),
		"show nat44 interfaces",
	)
}

func dnsServiceNetworkUnderlayInterface(network DNSServiceNetwork) string {
	fields := strings.Fields(network.UnderlayRoute)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
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
	policerRate, policerBurst := policerValues(target)

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
		commands := []string{}
		commands = append(commands, flowRateRuleCommands(target)...)
		commands = append(commands, "show ly-route flow-rate")
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

func flowRateRuleCommands(target flow.Target) []string {
	commands := make([]string, 0)
	clause := 0
	rateKbps, burstBytes := policerValues(target)
	for _, attachment := range target.Attachments {
		direction := flowAttachmentDirection(attachment)
		interfaceName := flowAttachmentInterface(attachment)
		for _, source := range nonEmptyList(target.Match.Sources, "0.0.0.0/0") {
			for _, destination := range nonEmptyList(target.Match.Destinations, "0.0.0.0/0") {
				for _, protocol := range nonEmptyList(target.Match.Protocols, "any") {
					for _, sourcePort := range nonEmptyList(target.Match.SourcePorts, "any") {
						for _, destinationPort := range nonEmptyList(target.Match.DestPorts, "any") {
							clauseSource, clauseDestination := source, destination
							clauseSourcePort, clauseDestinationPort := sourcePort, destinationPort
							if direction == "output" {
								clauseSource, clauseDestination = clauseDestination, clauseSource
								clauseSourcePort, clauseDestinationPort = clauseDestinationPort, clauseSourcePort
							}
							clause++
							commands = append(commands, fmt.Sprintf(
								"set ly-route flow-rate rule %s_%d interface %s direction %s source %s destination %s protocol %s source-port %s destination-port %s rate-kbps %d burst-bytes %d",
								safeTag(target.RuleID), clause, interfaceName, direction,
								aclAddressValue(clauseSource), aclAddressValue(clauseDestination), flowRateProtocolName(protocol),
								portRange(clauseSourcePort), portRange(clauseDestinationPort), rateKbps, burstBytes,
							))
						}
					}
				}
			}
		}
	}
	return commands
}

func flowRateProtocolName(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "", "any", "0":
		return "any"
	case "tcp", "6":
		return "tcp"
	case "udp", "17":
		return "udp"
	case "icmp", "1":
		return "icmp"
	default:
		return strings.ToLower(strings.TrimSpace(protocol))
	}
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

func planRequiresNAT(plan Plan) bool {
	if len(plan.NAT.StaticMappings) > 0 || len(plan.NAT.PortMappings) > 0 || len(plan.DNSServiceNetworks) > 0 {
		return true
	}
	for _, steering := range plan.Proxy.VPPSteering {
		if steering.TargetKind == "vpp.proxy-service.network" {
			return true
		}
	}
	return false
}

func natInitializeCommands(behavior nat.Behavior) []string {
	if behavior == nat.BehaviorFullCone {
		return []string{
			"?nat44 plugin disable",
			"?nat44 ei plugin enable",
		}
	}
	return []string{
		"?nat44 ei plugin disable",
		"?nat44 plugin enable",
	}
}

func natStaticMappingCommands(mapping nat.StaticMapping) []string {
	return natStaticMappingCommandsForBehavior(nat.BehaviorEndpointDependent, mapping)
}

func natStaticMappingCommandsForBehavior(behavior nat.Behavior, mapping nat.StaticMapping) []string {
	if behavior == nat.BehaviorFullCone {
		commands := []string{}
		if mapping.WANInterface != "" {
			commands = append(commands,
				fmt.Sprintf("?set interface nat44 ei in %s output-feature del", mapping.WANInterface),
				fmt.Sprintf("?set interface nat44 ei in %s output-feature", mapping.WANInterface),
				fmt.Sprintf("?nat44 ei add interface address %s", mapping.WANInterface),
			)
		}
		commands = append(commands,
			fmt.Sprintf("?nat44 ei add static mapping local %s external %s", mapping.InternalAddress, mapping.ExternalAddress),
			"show nat44 ei static mappings",
			"show nat44 ei sessions",
		)
		return commands
	}
	commands := []string{}
	if mapping.WANInterface != "" {
		commands = append(commands,
			fmt.Sprintf("?set interface nat44 in %s output-feature del", mapping.WANInterface),
			fmt.Sprintf("?set interface nat44 in %s output-feature", mapping.WANInterface),
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
	return natPortMappingCommandsForBehavior(nat.BehaviorEndpointDependent, mapping)
}

func natPortMappingCommandsForBehavior(behavior nat.Behavior, mapping nat.PortMapping) []string {
	if behavior == nat.BehaviorFullCone {
		commands := []string{}
		if mapping.WANInterface != "" {
			commands = append(commands,
				fmt.Sprintf("?set interface nat44 ei in %s output-feature del", mapping.WANInterface),
				fmt.Sprintf("?set interface nat44 ei in %s output-feature", mapping.WANInterface),
				fmt.Sprintf("?nat44 ei add interface address %s", mapping.WANInterface),
			)
		}
		mappingCommand := fmt.Sprintf("nat44 ei add static mapping %s local %s %d external %s %d", mapping.Protocol, mapping.InternalHost, mapping.InternalPort, mapping.ExternalAddress, mapping.ExternalPort)
		commands = append(commands,
			"show nat44 ei static mappings",
			fmt.Sprintf("?%s del", mappingCommand),
			fmt.Sprintf("?%s", mappingCommand),
			"show nat44 ei static mappings",
			"show nat44 ei sessions",
		)
		return commands
	}
	commands := []string{}
	if mapping.WANInterface != "" {
		commands = append(commands,
			fmt.Sprintf("?set interface nat44 in %s output-feature del", mapping.WANInterface),
			fmt.Sprintf("?set interface nat44 in %s output-feature", mapping.WANInterface),
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
	optimizedIPv4     bool
	defaultVia        string
	localDestinations []string
}

// addRoutePolicyLocalDestinations keeps a catch-all policy from stealing
// traffic addressed to the gateway/LAN itself.  This is a routing invariant,
// not a proxy-specific exception: the LAN prefix must remain reachable by
// the normal local FIB even when the user deliberately selects an any-to-any
// egress policy.
func addRoutePolicyLocalDestinations(options map[string]routePolicyCommandOptions, policies []trafficpolicy.RoutePolicy, assignments []AddressAssignment) {
	addRoutePolicyLocalDestinationPrefixes(options, policies, routePolicyLocalDestinations(assignments))
}

func addRoutePolicyLocalDestinationPrefixes(options map[string]routePolicyCommandOptions, policies []trafficpolicy.RoutePolicy, prefixes []string) {
	if len(prefixes) == 0 {
		return
	}
	for _, policy := range policies {
		id := strings.TrimSpace(policy.ID)
		if id == "" {
			continue
		}
		option := options[id]
		option.localDestinations = append([]string(nil), prefixes...)
		options[id] = option
	}
}

func routePolicyLocalDestinations(assignments []AddressAssignment) []string {
	seen := map[string]struct{}{}
	for _, assignment := range assignments {
		if !strings.EqualFold(strings.TrimSpace(assignment.Role), "lan") {
			continue
		}
		prefix, err := netip.ParsePrefix(strings.TrimSpace(assignment.CIDR))
		if err != nil || !prefix.Addr().Is4() {
			continue
		}
		seen[prefix.Masked().String()] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for prefix := range seen {
		result = append(result, prefix)
	}
	sort.Strings(result)
	return result
}

// orderedRoutePoliciesForVPP creates the lower-priority tables first.  A
// chained table can only install a lookup DPO after its next table exists.
// The policy priority in the generated ABF attachment remains unchanged, so
// this is only an implementation ordering detail.
func orderedRoutePoliciesForVPP(policies []trafficpolicy.RoutePolicy, wanGroups map[string]trafficpolicy.WANGroup) []trafficpolicy.RoutePolicy {
	ordered := append([]trafficpolicy.RoutePolicy(nil), policies...)
	options := buildRoutePolicyCommandOptions(ordered, wanGroups)
	if len(options) == 0 || len(ordered) < 2 {
		return ordered
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority == ordered[j].Priority {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Priority > ordered[j].Priority
	})
	return ordered
}

// buildRoutePolicyCommandOptions recognizes the destination-only suffix of an
// ordered policy list. Earlier source/protocol exceptions are evaluated first
// by their normal ACL rules; once they miss, the suffix can safely continue in
// native FIB tables without changing policy precedence.
func buildRoutePolicyCommandOptions(policies []trafficpolicy.RoutePolicy, wanGroups map[string]trafficpolicy.WANGroup) map[string]routePolicyCommandOptions {
	options := make(map[string]routePolicyCommandOptions)
	if len(policies) == 0 {
		return options
	}
	ordered := append([]trafficpolicy.RoutePolicy(nil), policies...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Priority < ordered[j].Priority })
	chainStart := 0
	chainEnd := -1
	for index, policy := range ordered {
		if !routePolicyFIBChainEligible(policy) || len(routePolicyIPv4Destinations(policy.Match.Destinations)) == 0 {
			if chainEnd >= 0 {
				// A lower-precedence non-FIB rule cannot be reached after the
				// effective catch-all, but it must not invalidate the chain that
				// was already established above it.
				break
			}
			chainStart = index + 1
			continue
		}
		// The first destination-only catch-all marks the effective end of the
		// chain. Consecutive destination-only entries may still be part of the
		// same native chain; a later ordinary rule closes it above.
		if chainEnd < 0 && routePolicyHasIPv4CatchAll(policy.Match.Destinations) {
			chainEnd = index + 1
		} else if chainEnd >= 0 {
			chainEnd = index + 1
		}
	}
	if chainEnd < 0 {
		return map[string]routePolicyCommandOptions{}
	}
	chain := ordered[chainStart:chainEnd]
	if len(chain) < 2 || !routePolicyHasIPv4CatchAll(chain[len(chain)-1].Match.Destinations) {
		return map[string]routePolicyCommandOptions{}
	}
	// Equal priorities at the ACL/FIB boundary have no deterministic ordering,
	// so keep the conservative ACL path in that ambiguous case.
	if chainStart > 0 && ordered[chainStart-1].Priority == chain[0].Priority {
		return map[string]routePolicyCommandOptions{}
	}
	for index, policy := range chain {
		defaultVia := routePolicyTarget(policy, wanGroups)
		if index+1 < len(chain) {
			defaultVia = fmt.Sprintf("ip4-lookup-in-table %d", stableID("route-table:"+chain[index+1].ID, 50000, 49999))
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
	if len(options) == 0 {
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

// Large destination sets must never be serialized into one ACL command. The
// pre-NAT Radix classifier is the primary native path because it preserves the
// policy priority of every rule independently. A complete destination-only
// FIB chain remains a useful optimization, but it must never be a prerequisite
// for accepting a large GeoIP rule: an unrelated UI rule can legally sit
// between the GeoIP policy and the terminal catch-all.
func validateLargeRoutePolicyFallback(routes []trafficpolicy.RoutePolicy, options map[string]routePolicyCommandOptions, wanGroups map[string]trafficpolicy.WANGroup) error {
	for _, route := range routes {
		prefixes := routePolicyIPv4Destinations(route.Match.Destinations)
		if len(prefixes) <= maxRoutePolicyACLIPv4Prefixes {
			continue
		}
		option := options[route.ID]
		if _, nativeRadix := compileRoutePolicyRadixPlan(route, wanGroups, option); nativeRadix {
			continue
		}
		if option.optimizedIPv4 {
			continue
		}
		return fmt.Errorf("route policy %q has %d IPv4 destinations but no verified native Radix/FIB path; refusing large ACL fallback", route.ID, len(prefixes))
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
	if commands, ok := routePolicyRadixCommands(policy, wanGroups, options, policyID, tableID); ok {
		return commands
	}
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
	commands := routePolicyACLCommands(aclID, policy.ID, match, routeACLAction(policy.Action), options)
	commands = append(commands,
		fmt.Sprintf("?ip table add %d", tableID),
		fmt.Sprintf("?set ip flow-hash table %d src dst sport dport proto", tableID),
	)
	if policy.Action != "deny" {
		routeTarget := routePolicyTarget(policy, wanGroups)
		if options.optimizedIPv4 {
			commands = append(commands, vppRouteBatchBegin)
			for _, prefix := range options.localDestinations {
				commands = append(commands, fmt.Sprintf("ip route add table %d %s via lyroute-$LY_ROUTE_LAN_INTERFACE", tableID, prefix))
			}
			for _, prefix := range routePolicyIPv4Destinations(policy.Match.Destinations) {
				commands = append(commands, fmt.Sprintf("ip route add table %d %s via %s", tableID, prefix, routeTarget))
			}
			commands = append(commands, vppRouteBatchEnd)
			if strings.TrimSpace(options.defaultVia) != "" {
				routeTarget = options.defaultVia
			}
		}
		if strings.HasPrefix(routeTarget, "ip4-lookup-in-table ") || strings.HasPrefix(routeTarget, "ip6-lookup-in-table ") {
			// VPP accepts the table before the prefix for regular next hops, but
			// lookup DPOs require the documented prefix-before-table form.
			commands = append(commands, fmt.Sprintf("?ip route add 0.0.0.0/0 table %d via %s", tableID, routeTarget))
		} else {
			commands = append(commands, fmt.Sprintf("?ip route add table %d 0.0.0.0/0 via %s", tableID, routeTarget))
		}
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
		// VPP 25.x may render a point-to-point ABF path as
		// "<next-hop> if_index:<n>". Keep the interface inventory in the
		// same transaction so readback can prove that the path belongs to
		// the intended service interface instead of accepting an arbitrary
		// unresolved/stale path.
		"show interface",
		"show abf attach lyroute-$LY_ROUTE_LAN_INTERFACE",
		fmt.Sprintf("show ip fib table %d", tableID),
	)
	if groupTableID > 0 {
		commands = append(commands, fmt.Sprintf("show ip fib table %d", groupTableID))
	}
	return commands
}

func routePolicyRadixCommands(policy trafficpolicy.RoutePolicy, wanGroups map[string]trafficpolicy.WANGroup, options routePolicyCommandOptions, policyID, tableID int) ([]string, bool) {
	plan, ok := compileRoutePolicyRadixPlan(policy, wanGroups, options)
	if !ok {
		return nil, false
	}
	commands := []string{
		fmt.Sprintf("?ip table add %d", tableID),
		fmt.Sprintf("?set ip flow-hash table %d src dst sport dport proto", tableID),
	}
	if strings.HasPrefix(plan.routeTarget, "ip4-lookup-in-table ") {
		commands = append(commands, fmt.Sprintf("?ip route add 0.0.0.0/0 table %d via %s", tableID, plan.routeTarget))
	} else {
		commands = append(commands, fmt.Sprintf("?ip route add table %d 0.0.0.0/0 via %s", tableID, plan.routeTarget))
	}
	classifier, applied, err := buildPreNATRoutePolicyCommandsForTable(
		policy, policyID, tableID, "lyroute-$LY_ROUTE_LAN_INTERFACE", plan.lanPrefix, plan.skipNAT,
	)
	if err != nil || !applied {
		return nil, false
	}
	commands = append(commands, classifier...)
	commands = append(commands, fmt.Sprintf("show ip fib table %d", tableID))
	return commands, true
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
	directions := securityDirections(acl.Match.Direction)
	if acl.ID == "sec-acl-default-deny-wan" && strings.TrimSpace(wanIngressInterface) != "" {
		directions = []string{"input"}
	}
	for _, direction := range directions {
		commands = append(commands, fmt.Sprintf("?set interface %s acl intfc %s ip4-table %d", direction, interfaceName, aclID))
	}
	commands = append(commands, fmt.Sprintf("show acl-plugin acl index %d", aclID), "show interface lyroute-$LY_ROUTE_LAN_INTERFACE")
	return commands
}

func wanGroupCommands(group trafficpolicy.WANGroup) []string {
	tableID := wanGroupTableID(group.ID)
	commands := []string{
		fmt.Sprintf("?ip table add %d", tableID),
		fmt.Sprintf("?ip route del table %d 0.0.0.0/0", tableID),
		fmt.Sprintf("?ip route del table %d 0.0.0.0/1", tableID),
		fmt.Sprintf("?ip route del table %d 128.0.0.0/1", tableID),
	}
	if group.Mode != trafficpolicy.WANGroupPrimaryBackup {
		commands = append(commands, fmt.Sprintf("?set ip flow-hash table %d src dst sport dport proto", tableID))
	}
	for _, prefix := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		command := fmt.Sprintf("?ip route add %s table %d", prefix, tableID)
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
			command += fmt.Sprintf(" via %s weight %d preference %d", via, weight, preference)
		}
		commands = append(commands, command)
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
			source = aclAddressValue(source)
			destination = aclAddressValue(destination)
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

func routePolicyACLCommands(aclID int, resource string, match trafficpolicy.Match, action string, options routePolicyCommandOptions) []string {
	commands := aclMatchCommands(aclID, resource, match, action)
	if action != "permit" || options.optimizedIPv4 || len(options.localDestinations) == 0 || !routePolicyHasIPv4CatchAll(match.Destinations) || len(commands) == 0 {
		return commands
	}
	denies := make([]string, 0)
	for _, source := range nonEmptyList(match.Sources, "0.0.0.0/0") {
		for _, destination := range options.localDestinations {
			for _, protocol := range nonEmptyList(match.Protocols, "any") {
				for _, sourcePort := range nonEmptyList(match.SourcePorts, "any") {
					for _, destPort := range nonEmptyList(match.DestPorts, "any") {
						denies = append(denies, fmt.Sprintf("deny src %s dst %s proto %s sport %s dport %s", aclAddressValue(source), aclAddressValue(destination), aclProtocolValue(protocol), portRange(sourcePort), portRange(destPort)))
					}
				}
			}
		}
	}
	if len(denies) == 0 {
		return commands
	}
	tagIndex := strings.LastIndex(commands[0], " tag ")
	prefix := fmt.Sprintf("?set acl-plugin acl index %d ", aclID)
	if tagIndex < 0 || !strings.HasPrefix(commands[0], prefix) {
		return commands
	}
	// VPP ACLs are first-match. The local deny must precede the catch-all
	// permit or packets to the gateway/LAN are still sent into NAT and ABF.
	rules := commands[0][len(prefix):tagIndex]
	commands[0] = prefix + strings.Join(denies, ", ") + ", " + rules + commands[0][tagIndex:]
	return commands
}

func aclMatchCommandsWithFallback(aclID int, resource string, match trafficpolicy.Match, action string) []string {
	commands := aclMatchCommands(aclID, resource, match, action)
	if action != "permit" || len(commands) == 0 {
		return commands
	}
	command := commands[0]
	tagIndex := strings.LastIndex(command, " tag ")
	if tagIndex < 0 {
		return commands
	}
	// ACLs attached by a rate rule are evaluated by both IP feature arcs.
	// A v4-only catch-all does not match IPv6 and VPP defaults an unmatched
	// ACL packet to deny, which would turn an IPv4 rate rule into an IPv6
	// outage. Keep nonmatching traffic explicitly permitted in both families.
	fallbacks := []string{
		"permit src 0.0.0.0/0 dst 0.0.0.0/0 proto 0 sport 0-65535 dport 0-65535",
		"permit src ::/0 dst ::/0 proto 0 sport 0-65535 dport 0-65535",
	}
	missing := make([]string, 0, len(fallbacks))
	for _, fallback := range fallbacks {
		if !strings.Contains(command, fallback) {
			missing = append(missing, fallback)
		}
	}
	if len(missing) == 0 {
		return commands
	}
	commands[0] = command[:tagIndex] + ", " + strings.Join(missing, ", ") + command[tagIndex:]
	return commands
}

func aclAddressValue(address string) string {
	address = strings.TrimSpace(address)
	if address == "" || strings.EqualFold(address, "any") {
		return "0.0.0.0/0"
	}
	return address
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
	if strings.HasPrefix(strings.ToLower(via), "pppoe_session") || strings.HasPrefix(strings.ToLower(via), "pppoe-runtime:") {
		return via
	}
	if nextHop = strings.TrimSpace(nextHop); nextHop != "" {
		return nextHop + " " + via
	}
	if strings.HasPrefix(strings.ToLower(via), "pppoe_session") || strings.HasPrefix(strings.ToLower(via), "pppoe-runtime:") {
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
