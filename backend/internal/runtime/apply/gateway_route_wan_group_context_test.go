package apply

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ly-route/backend/internal/runtime/trafficpolicy"
	"ly-route/backend/internal/runtime/vpp"
)

func TestProductionRouteApplyUsesUnchangedDesiredWANGroup(t *testing.T) {
	group := trafficpolicy.WANGroup{ID: "wan-weighted", Members: []string{"wan-a", "wan-b"}}
	route := trafficpolicy.RoutePolicy{
		ID:       "route-default",
		Priority: 50,
		Action:   "nat",
		Egress:   group.ID,
		Match: trafficpolicy.Match{
			Sources:      []string{"192.168.50.0/24"},
			Destinations: []string{"0.0.0.0/0"},
			Protocols:    []string{"any"},
			SourcePorts:  []string{"any"},
			DestPorts:    []string{"any"},
		},
	}
	client := &routeContextCaptureClient{fail: errors.New("stop after route operation")}
	adapter := &productionGatewayAdapter{
		name:       "routes",
		reconciler: &productionGatewayReconciler{adapter: vpp.Adapter{Client: client}},
	}
	diff := vpp.GatewayDiff{Routes: vpp.RouteWANGroupPlan{
		TransactionID:       "txn-route-context",
		IngressVPPInterface: "lyroute-lan0",
		LocalDestinations:   []string{"192.168.50.0/24"},
		Routes:              []trafficpolicy.RoutePolicy{route},
		RoutePolicyContext:  []trafficpolicy.RoutePolicy{route},
	}}
	desired := vpp.Plan{Policy: trafficpolicy.Config{
		RoutePolicies: []trafficpolicy.RoutePolicy{route},
		WANGroups:     []trafficpolicy.WANGroup{group},
	}}

	_, _ = adapter.applyDiff(context.Background(), diff, vpp.Snapshot{}, desired)

	if len(client.operations) == 0 {
		t.Fatal("route apply emitted no VPP operation")
	}
	commands := strings.Join(client.operations[0].VPPCtlCommands, "\n")
	want := fmt.Sprintf("ip4-lookup-in-table %d", vpp.WANGroupTableID(group.ID))
	if !strings.Contains(commands, want) {
		t.Fatalf("route commands = %q, want unchanged desired WAN group target %q", commands, want)
	}
}

type routeContextCaptureClient struct {
	operations []vpp.Operation
	fail       error
}

func (client *routeContextCaptureClient) OpenChannel(context.Context) (vpp.Channel, error) {
	return &routeContextCaptureChannel{client: client}, nil
}

type routeContextCaptureChannel struct {
	client *routeContextCaptureClient
}

func (channel *routeContextCaptureChannel) Do(_ context.Context, operation vpp.Operation) (vpp.Reply, error) {
	channel.client.operations = append(channel.client.operations, operation)
	if len(channel.client.operations) == 1 {
		return vpp.Reply{}, channel.client.fail
	}
	return vpp.Reply{}, nil
}

func (*routeContextCaptureChannel) Close() error { return nil }
