package vpp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/proxy"
	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestNormalizeRetvalReturnsStructuredError(t *testing.T) {
	err := NormalizeRetval("vpp.qos.classify", "req-1", -7)
	var vppErr VPPError
	if !errors.As(err, &vppErr) {
		t.Fatalf("error = %T %v, want VPPError", err, err)
	}
	if vppErr.Operation != "vpp.qos.classify" || vppErr.RequestID != "req-1" || vppErr.Retval != -7 {
		t.Fatalf("VPPError = %#v", vppErr)
	}
	if !strings.Contains(err.Error(), "retval -7") {
		t.Fatalf("error text missing retval: %v", err)
	}
}

func TestConsumeMultipartStopsAtControlPing(t *testing.T) {
	stream := &fakeStream{replies: []Reply{{Payload: "first"}, {Payload: "second"}, {ControlPing: true}}}
	replies, err := ConsumeMultipart(context.Background(), Operation{Name: "dump", RequestID: "req-dump"}, stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(replies) != 2 || replies[0].Payload != "first" || replies[1].Payload != "second" {
		t.Fatalf("replies = %#v", replies)
	}
}

func TestBuildOperationsUsesCompiledProxyAndFlowTargets(t *testing.T) {
	plan := validPlan(t, "req-plan")
	operations := mustBuildOperations(t, plan)
	for _, required := range []string{"vpp.dataplane.attach", "vpp.interface.address", "vpp.abf.policy", "vpp.pbr.policy", "vpp.service-chain.egress-binding", "vpp.acl.drop", "vpp.behavior.rate", "vpp.qos.classify", "vpp.qos.record", "vpp.qos.store", "vpp.qos.egress-map", "vpp.qos.mark", "vpp.policer"} {
		if !hasOperation(operations, required) {
			t.Fatalf("operations missing %q: %#v", required, operations)
		}
	}
	if operations[0].Name != "vpp.dataplane.attach" || len(operations[0].VPPCtlCommands) == 0 {
		t.Fatalf("first operation = %#v, want VPP dataplane probe with vppctl commands", operations[0])
	}
	for _, operation := range operations {
		if len(operation.VPPCtlCommands) == 0 {
			t.Fatalf("operation %s has no vppctl commands: %#v", operation.Name, operation)
		}
	}
	assertOperationCommand(t, operations, "vpp.abf.policy", "abf policy add")
	assertOperationCommand(t, operations, "vpp.interface.address", "set interface ip address lyroute-enp2s0 10.10.10.2/24")
	assertOperationCommand(t, operations, "vpp.pbr.policy", "ip table add")
	assertOperationCommand(t, operations, "vpp.pbr.policy", "ip route add table")
	assertOperationCommand(t, operations, "vpp.acl.drop", "deny src 192.168.20.0/24 dst 10.0.0.0/8 proto 6 sport 0-65535 dport 443-443")
	assertOperationCommand(t, operations, "vpp.acl.drop", "set acl-plugin interface lyroute-enp2s0 input acl")
	assertOperationCommand(t, operations, "vpp.behavior.rate", "permit src any dst 203.0.113.10 proto 17 sport 0-65535 dport 0-65535")
	assertOperationCommand(t, operations, "vpp.behavior.rate", "policer output name ly_route_limit_video lyroute-enp2s0")
	assertOperationCommand(t, operations, "vpp.qos.record", "qos record ip")
	assertOperationCommand(t, operations, "vpp.qos.store", "qos store ip lyroute-enp2s0 value")
	assertOperationCommand(t, operations, "vpp.qos.egress-map", "qos egress map")
	assertOperationCommand(t, operations, "vpp.qos.egress-map", "]=48")
	assertOperationCommand(t, operations, "vpp.qos.mark", "qos mark ip")
	assertOperationCommand(t, operations, "vpp.policer", "policer add")
	assertOperationCommand(t, operations, "vpp.policer", "violate-action drop")
}

func TestBuildOperationsIncludesDHCPInterfaceAssignment(t *testing.T) {
	operations := mustBuildOperations(t, provenPlan(Plan{RequestID: "req", AddressAssignments: []AddressAssignment{{ID: "wan", LinuxInterface: "enp4s0", VPPInterface: "lyroute-enp4s0", Mode: "dhcp4", RemoveCIDRs: []string{"10.10.10.6/24"}}}}, "enp4s0"))
	assertOperationCommand(t, operations, "vpp.interface.address", "set interface ip address lyroute-enp4s0 10.10.10.6/24 del")
	assertOperationCommand(t, operations, "vpp.interface.address", "set dhcp client intfc lyroute-enp4s0")
}

func TestBuildOperationsIncludesNAT44Mappings(t *testing.T) {
	operations := mustBuildOperations(t, provenPlan(Plan{RequestID: "req-nat", NAT: nat.CompiledConfig{
		StaticMappings: []nat.StaticMapping{{ID: "static-main", ExternalAddress: "203.0.113.10", InternalAddress: "192.168.88.10", WANInterface: "wan0"}},
		PortMappings: []nat.PortMapping{
			{ID: "web", Protocol: "tcp", ExternalAddress: "203.0.113.10", ExternalPort: 8443, InternalHost: "192.168.88.20", InternalPort: 8443, WANInterface: "wan0", Hairpin: true},
			{ID: "dns", Protocol: "udp", ExternalAddress: "203.0.113.10", ExternalPort: 5353, InternalHost: "192.168.88.53", InternalPort: 53, WANInterface: "wan0"},
		},
	}}, "eth1"))
	if !hasOperation(operations, "vpp.nat44-ed.static-mapping") {
		t.Fatalf("operations missing nat44 mapping: %#v", operations)
	}
	assertOperationCommand(t, operations, "vpp.nat44-ed.static-mapping", "set interface nat44 out wan0")
	assertOperationCommand(t, operations, "vpp.nat44-ed.static-mapping", "nat44 plugin enable")
	assertOperationCommand(t, operations, "vpp.nat44-ed.static-mapping", "nat44 add address 203.0.113.10")
	assertOperationCommand(t, operations, "vpp.nat44-ed.static-mapping", "nat44 add static mapping local 192.168.88.10 external 203.0.113.10")
	assertOperationCommand(t, operations, "vpp.nat44-ed.static-mapping", "nat44 add static mapping tcp local 192.168.88.20 8443 external 203.0.113.10 8443 del")
	assertOperationCommand(t, operations, "vpp.nat44-ed.static-mapping", "nat44 add static mapping tcp local 192.168.88.20 8443 external 203.0.113.10 8443")
	assertOperationCommand(t, operations, "vpp.nat44-ed.static-mapping", "nat44 add static mapping udp local 192.168.88.53 53 external 203.0.113.10 5353 del")
	assertOperationCommand(t, operations, "vpp.nat44-ed.static-mapping", "nat44 add static mapping udp local 192.168.88.53 53 external 203.0.113.10 5353")
	assertOperationCommand(t, operations, "vpp.nat44-ed.static-mapping", "show nat44 static mappings | include 203.0.113.10")
	assertOperationCommand(t, operations, "vpp.nat44-ed.static-mapping", "show nat44 sessions")
	assertOperationCommand(t, operations, "vpp.nat44-ed.static-mapping", "show nat44 sessions | include hairpin")
}

func TestBuildOperationsIncludesRoutePolicyAndSecurityACL(t *testing.T) {
	operations := mustBuildOperations(t, provenPlan(Plan{RequestID: "req-policy", Policy: trafficpolicy.Config{
		RoutePolicies: []trafficpolicy.RoutePolicy{{ID: "video-egress", Priority: 10, Action: "route", Egress: "wan-primary", Match: trafficpolicy.Match{Sources: []string{"192.168.10.0/24"}, Destinations: []string{"0.0.0.0/0"}, Protocols: []string{"tcp"}, DestPorts: []string{"443"}}}},
		SecurityACLs:  []trafficpolicy.SecurityACL{{ID: "guest-block", Priority: 20, Action: "deny", Match: trafficpolicy.Match{Sources: []string{"192.168.20.0/24"}, Destinations: []string{"10.0.0.0/8"}, Protocols: []string{"any"}, Direction: "output"}}},
		WANGroups:     []trafficpolicy.WANGroup{{ID: "wan-primary", Members: []string{"wan0", "wan1"}, Weights: map[string]int{"wan0": 2, "wan1": 1}}},
	}}, "eth1"))
	assertOperationCommand(t, operations, "vpp.route-policy", "set acl-plugin acl")
	assertOperationCommand(t, operations, "vpp.route-policy", "ip table add")
	assertOperationCommand(t, operations, "vpp.route-policy", "ip route add table")
	assertOperationCommand(t, operations, "vpp.route-policy", "via ip4-lookup-in-table")
	assertOperationCommand(t, operations, "vpp.route-policy", "show ip fib table")
	assertOperationCommand(t, operations, "vpp.security-acl", "deny src 192.168.20.0/24 dst 10.0.0.0/8")
	assertOperationCommand(t, operations, "vpp.security-acl", "set interface output acl")
	assertOperationCommand(t, operations, "vpp.pbr.next-hop-group", "ip route add table")
	assertOperationCommand(t, operations, "vpp.pbr.next-hop-group", "via wan0 weight 2")
	assertOperationCommand(t, operations, "vpp.pbr.next-hop-group", "via wan1")
	if operationIndex(operations, "vpp.pbr.next-hop-group") > operationIndex(operations, "vpp.route-policy") {
		t.Fatalf("WAN group operation must precede route policy: %#v", operations)
	}
}

func TestRoutePolicyCommandsUseResolvedDirectWANPath(t *testing.T) {
	path := trafficpolicy.WANPath{VPPInterface: "lyroute-eth1", NextHop: "198.51.100.1"}
	commands := strings.Join(routePolicyCommands(trafficpolicy.RoutePolicy{
		ID: "direct-wan", Priority: 10, Action: "route", Egress: "wan-resource", Path: &path,
	}, nil), "\n")
	if !strings.Contains(commands, "via 198.51.100.1 lyroute-eth1") || strings.Contains(commands, "via wan-resource") || !strings.Contains(commands, "show abf attach lyroute-$LY_ROUTE_LAN_INTERFACE") {
		t.Fatalf("direct WAN route commands = %s", commands)
	}
}

func TestWANGroupCommandsImplementAllProductModes(t *testing.T) {
	primaryBackup := strings.Join(wanGroupCommands(trafficpolicy.WANGroup{ID: "failover", Mode: trafficpolicy.WANGroupPrimaryBackup, Members: []string{"wan0", "wan1"}, Weights: map[string]int{"wan0": 9, "wan1": 4}}), "\n")
	if strings.Contains(primaryBackup, "flow-hash") || !strings.Contains(primaryBackup, "via wan0 weight 1 preference 0") || !strings.Contains(primaryBackup, "via wan1 weight 1 preference 1") {
		t.Fatalf("primary-backup commands = %s", primaryBackup)
	}

	weighted := strings.Join(wanGroupCommands(trafficpolicy.WANGroup{ID: "weighted", Mode: trafficpolicy.WANGroupWeighted, Members: []string{"wan0", "wan1"}, Weights: map[string]int{"wan0": 3, "wan1": 1}}), "\n")
	if !strings.Contains(weighted, "flow-hash table") || !strings.Contains(weighted, "via wan0 weight 3 preference 0") {
		t.Fatalf("weighted commands = %s", weighted)
	}

	fiveTuple := strings.Join(wanGroupCommands(trafficpolicy.WANGroup{ID: "five-tuple", Mode: trafficpolicy.WANGroupFiveTuple, Members: []string{"wan0", "wan1"}, Weights: map[string]int{"wan0": 1, "wan1": 1}}), "\n")
	if !strings.Contains(fiveTuple, "flow-hash table") || !strings.Contains(fiveTuple, "via wan1 weight 1 preference 0") {
		t.Fatalf("five-tuple commands = %s", fiveTuple)
	}
}

func TestAdapterApplyUsesSeparateChannelPerConcurrentRequest(t *testing.T) {
	client := &fakeClient{}
	adapter := Adapter{Client: client}
	ctx := context.Background()
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := adapter.Apply(ctx, validPlan(t, fmt.Sprintf("req-%d", index)))
			errCh <- err
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if client.opened() != 2 {
		t.Fatalf("opened channels = %d, want one per concurrent request", client.opened())
	}
	if client.mixedRequests() {
		t.Fatal("a fake channel observed mixed request IDs")
	}
}

func validPlan(t *testing.T, requestID string) Plan {
	t.Helper()
	compiledProxy, err := proxy.CompileEgress(proxy.NewProxyEgress("proxy-media", "xray-tproxy-outbound"))
	if err != nil {
		t.Fatal(err)
	}
	compiledFlow, err := flow.CompileIntent(flow.NewIntent("default", []flow.Rule{
		{ID: "drop-guest", Granularity: flow.RuleGranularity, Match: flow.Match{Sources: []string{"192.168.20.0/24"}, Destinations: []string{"10.0.0.0/8"}, Protocols: []string{"tcp"}, DestPorts: []string{"443"}, Direction: "uplink"}, Actions: []flow.Action{flow.Drop()}},
		{ID: "limit-video", Granularity: flow.RuleGranularity, Match: flow.Match{Destinations: []string{"203.0.113.10"}, Protocols: []string{"udp"}, Direction: "downlink"}, Actions: []flow.Action{flow.Police(20_000_000, 2_000_000)}},
		flow.NewRule("classify-video", flow.RuleGranularity, flow.Classify("video")),
		flow.NewClassRule("remark-bulk", "bulk", flow.Remark("AF11")),
		flow.NewClassRule("remark-voice", "control", flow.Remark("CS6")),
		flow.NewClassRule("police-bulk", "bulk", flow.Police(10_000_000, 1_000_000)),
	}))
	if err != nil {
		t.Fatal(err)
	}
	return provenPlan(Plan{RequestID: requestID, AddressAssignments: []AddressAssignment{{ID: "lan-eth1", LinuxInterface: "enp2s0", VPPInterface: "lyroute-enp2s0", CIDR: "10.10.10.2/24", Role: "lan"}}, Proxy: compiledProxy, Flow: compiledFlow}, "enp2s0")
}

func provenPlan(plan Plan, interfaces ...string) Plan {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	assignments := make([]NativeAssignment, 0, len(interfaces))
	for _, name := range interfaces {
		assignments = append(assignments, NativeAssignment{LinuxInterface: name, Explicit: true, Proof: provenAFXDPProof(now)})
	}
	plan.NativePath = NativePathRequest{ManagementInterface: "eth0", Assignments: assignments, Now: now}
	return plan
}

func mustBuildOperations(t *testing.T, plan Plan) []Operation {
	t.Helper()
	operations, err := BuildOperations(plan)
	if err != nil {
		t.Fatal(err)
	}
	return operations
}

func hasOperation(operations []Operation, name string) bool {
	for _, operation := range operations {
		if operation.Name == name {
			return true
		}
	}
	return false
}

func operationIndex(operations []Operation, name string) int {
	for index, operation := range operations {
		if operation.Name == name {
			return index
		}
	}
	return len(operations)
}

func assertOperationCommand(t *testing.T, operations []Operation, name, commandPart string) {
	t.Helper()
	foundOperation := false
	for _, operation := range operations {
		if operation.Name != name {
			continue
		}
		foundOperation = true
		for _, command := range operation.VPPCtlCommands {
			if strings.Contains(command, commandPart) {
				return
			}
		}
	}
	if !foundOperation {
		t.Fatalf("operation %s not found", name)
	}
	t.Fatalf("operation %s commands missing %q: %#v", name, commandPart, operations)
}

type fakeClient struct {
	mu       sync.Mutex
	channels []*fakeChannel
}

func (client *fakeClient) OpenChannel(context.Context) (Channel, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	channel := &fakeChannel{}
	client.channels = append(client.channels, channel)
	return channel, nil
}

func (client *fakeClient) opened() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return len(client.channels)
}

func (client *fakeClient) mixedRequests() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	for _, channel := range client.channels {
		if channel.mixedRequests() {
			return true
		}
	}
	return false
}

type fakeChannel struct {
	mu         sync.Mutex
	requestIDs map[string]struct{}
}

func (channel *fakeChannel) Do(_ context.Context, operation Operation) (Reply, error) {
	channel.mu.Lock()
	defer channel.mu.Unlock()
	if channel.requestIDs == nil {
		channel.requestIDs = map[string]struct{}{}
	}
	channel.requestIDs[operation.RequestID] = struct{}{}
	return Reply{Operation: operation.Name}, nil
}

func (channel *fakeChannel) Close() error { return nil }

func (channel *fakeChannel) mixedRequests() bool {
	channel.mu.Lock()
	defer channel.mu.Unlock()
	return len(channel.requestIDs) > 1
}

type fakeStream struct {
	replies []Reply
	index   int
}

func (stream *fakeStream) Recv(context.Context) (Reply, error) {
	if stream.index >= len(stream.replies) {
		return Reply{}, errors.New("unexpected end of fake stream")
	}
	reply := stream.replies[stream.index]
	stream.index++
	return reply, nil
}

func TestBuildOperationsAddsGatewayDNSTransparentInterception(t *testing.T) {
	plan := provenPlan(Plan{
		RequestID:       "req-dns-transparent",
		DNSInterception: true,
		AddressAssignments: []AddressAssignment{
			{ID: "lan4", LinuxInterface: "enp2s0", VPPInterface: "lyroute-enp2s0", CIDR: "192.168.88.1/24", Role: "lan"},
			{ID: "lan6", LinuxInterface: "enp2s0", VPPInterface: "lyroute-enp2s0", CIDR: "2001:db8:88::1/64", Role: "lan"},
		},
	}, "enp2s0")
	operations := mustBuildOperations(t, plan)
	commands := ""
	for _, operation := range operations {
		if operation.Name == "vpp.dns-transparent-interception" {
			commands = strings.Join(operation.VPPCtlCommands, "\n")
			break
		}
	}
	for _, required := range []string{"dport 53-53", "abf attach ip4", "abf attach ip6", "via local", "ip6-lookup-in-table 101", "2001:db8:88::1/64 via lyroute-enp2s0"} {
		if !strings.Contains(commands, required) {
			t.Fatalf("transparent DNS commands are missing %q:\n%s", required, commands)
		}
	}
}

func TestBuildOperationsLocksGatewayDNSWithoutLAN(t *testing.T) {
	_, err := BuildOperations(provenPlan(Plan{RequestID: "req-dns-no-lan", DNSInterception: true}, "enp2s0"))
	var locked *DataplaneLockedError
	if !errors.As(err, &locked) || !strings.Contains(err.Error(), "dns_lan_interface_present") {
		t.Fatalf("expected DNS LAN prerequisite lock, got %v", err)
	}
}

func TestBuildOperationsCreatesSharedManagementLCP(t *testing.T) {
	plan := provenPlan(Plan{RequestID: "req-shared-management", AddressAssignments: []AddressAssignment{{ID: "lan", LinuxInterface: "eth0", VPPInterface: "lyroute-eth0", CIDR: "10.10.10.254/24", Role: "lan"}}}, "eth0")
	plan.NativePath.ManagementShared = true
	operations := mustBuildOperations(t, plan)
	for _, operation := range operations {
		if operation.Name != "vpp.management-lcp" {
			continue
		}
		management, ok := operation.Payload.(ManagementLCP)
		if !ok || !management.Enabled || management.VPPInterface != "lyroute-eth0" || management.HostInterface != "lymgmt0" {
			t.Fatalf("shared management LCP = %#v", operation.Payload)
		}
		if commands := strings.Join(operation.VPPCtlCommands, "\n"); !strings.Contains(commands, "lcp create lyroute-eth0 host-if lymgmt0") || !strings.Contains(commands, "lcp lcp-sync on") {
			t.Fatalf("shared management commands = %s", commands)
		}
		return
	}
	t.Fatal("shared management LCP operation is missing")
}

func TestBuildOperationsLocksSharedManagementOutsideLAN(t *testing.T) {
	plan := provenPlan(Plan{RequestID: "req-shared-management-invalid", AddressAssignments: []AddressAssignment{{ID: "wan", LinuxInterface: "eth0", VPPInterface: "lyroute-eth0", CIDR: "10.10.10.254/24", Role: "wan"}}}, "eth0")
	plan.NativePath.ManagementShared = true
	_, err := BuildOperations(plan)
	var locked *DataplaneLockedError
	if !errors.As(err, &locked) || !strings.Contains(err.Error(), "shared_management_lan_binding") {
		t.Fatalf("expected shared management LAN lock, got %v", err)
	}
}

func TestBuildOperationsActivatesBuiltInSmartQoSOnLANAndWAN(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	proof := provenAFXDPProof(now)
	proof.SmartQoSPluginAvailable = true
	plan := Plan{
		RequestID: "req-smart-qos",
		AddressAssignments: []AddressAssignment{
			{ID: "lan", LinuxInterface: "eth1", VPPInterface: "lyroute-eth1", CIDR: "192.168.10.1/24", Role: "lan", BandwidthKbps: 100000},
			{ID: "wan", LinuxInterface: "eth2", VPPInterface: "lyroute-eth2", CIDR: "203.0.113.2/30", Role: "wan", BandwidthKbps: 50000},
		},
		NativePath: NativePathRequest{ManagementInterface: "eth0", RequireSmartQoS: true, Now: now, Assignments: []NativeAssignment{
			{LinuxInterface: "eth1", Explicit: true, Candidates: []CapabilityProof{proof}},
			{LinuxInterface: "eth2", Explicit: true, Candidates: []CapabilityProof{proof}},
		}},
	}
	operations := mustBuildOperations(t, plan)
	assertOperationCommand(t, operations, "vpp.smart-qos", "interface lyroute-eth1 rate 100000 host-isolation destination")
	assertOperationCommand(t, operations, "vpp.smart-qos", "interface lyroute-eth2 rate 50000 host-isolation source")
}

func TestBuildOperationsLocksSmartQoSWithoutExplicitLineBandwidth(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	proof := provenAFXDPProof(now)
	proof.SmartQoSPluginAvailable = true
	plan := Plan{
		RequestID: "req-smart-qos-no-bandwidth",
		AddressAssignments: []AddressAssignment{
			{ID: "lan", LinuxInterface: "eth1", VPPInterface: "lyroute-eth1", CIDR: "192.168.10.1/24", Role: "lan"},
			{ID: "wan", LinuxInterface: "eth2", VPPInterface: "lyroute-eth2", CIDR: "203.0.113.2/30", Role: "wan"},
		},
		NativePath: NativePathRequest{ManagementInterface: "eth0", RequireSmartQoS: true, Now: now, Assignments: []NativeAssignment{
			{LinuxInterface: "eth1", Explicit: true, Candidates: []CapabilityProof{proof}},
			{LinuxInterface: "eth2", Explicit: true, Candidates: []CapabilityProof{proof}},
		}},
	}
	_, err := BuildOperations(plan)
	var locked *DataplaneLockedError
	if !errors.As(err, &locked) || !strings.Contains(err.Error(), "smart_qos_bandwidth_configured") {
		t.Fatalf("expected explicit bandwidth prerequisite lock, got %v", err)
	}
}
