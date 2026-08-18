package vpp

import (
	"context"
	"os"
	"testing"

	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/proxy"
)

func TestVPPCTLProxyABFLifecycleIntegration(t *testing.T) {
	binary := os.Getenv("LY_ROUTE_VPPCTL_INTEGRATION_BINARY")
	if binary == "" {
		t.Skip("LY_ROUTE_VPPCTL_INTEGRATION_BINARY is not set")
	}
	steering := proxy.VPPSteeringInstruction{
		EgressID: "integration-proxy", Handoff: proxy.VPPToHost,
		TargetKind: "vpp.abf.policy", Order: 20,
	}
	operation := Operation{
		Name: steering.TargetKind, RequestID: "integration-proxy-apply", Resource: steering.EgressID,
		Payload: steering, VPPCtlCommands: proxySteeringCommands(steering, nat.BehaviorEndpointDependent),
	}
	if os.Getenv("LY_ROUTE_PROXY_ABF_ACTION") == "delete" {
		operation.Name += ".rollback-delete"
		operation.RequestID = "integration-proxy-delete"
		operation.VPPCtlCommands = proxySteeringDeleteCommands(steering)
	} else if os.Getenv("LY_ROUTE_PROXY_ABF_ACTION") != "apply" {
		t.Fatal("LY_ROUTE_PROXY_ABF_ACTION must be apply or delete")
	}
	channel, err := NewProductionVPPCTLClient(binary).OpenChannel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	reply, err := channel.Do(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	envelope, ok := reply.Payload.(VPPCTLReplyPayload)
	if !ok || len(envelope.CommandResults) == 0 {
		t.Fatalf("missing proxy ABF lifecycle evidence: %#v", reply.Payload)
	}
}
