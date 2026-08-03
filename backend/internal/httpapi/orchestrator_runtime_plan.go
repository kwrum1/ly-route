package httpapi

import (
	"context"
	"errors"
	"os"
	"strings"

	"ly-route/backend/internal/runtime/apply"
	"ly-route/backend/internal/runtime/flow"
	serviceRuntime "ly-route/backend/internal/runtime/service"
	"ly-route/backend/internal/runtime/trafficpolicy"
	"ly-route/backend/internal/runtime/vpp"
)

func (server *Server) buildOrchestratorRuntimePlan(ctx context.Context, requestID string) (RuntimePlan, error) {
	flowIntent, hasFlowIntent, err := server.runtimeFlowIntent(ctx)
	if err != nil {
		return RuntimePlan{}, err
	}
	var compiledFlow flow.CompiledIntent
	if hasFlowIntent {
		compiledFlow, err = flow.CompileIntent(flowIntent)
		if err != nil {
			return RuntimePlan{}, err
		}
	}
	compiledPolicy, err := server.currentOrchestratorSecurityConfig(ctx)
	if err != nil {
		return RuntimePlan{}, err
	}

	proofPath := strings.TrimSpace(os.Getenv("LY_ROUTE_VPP_CAPABILITY_PROOF"))
	if proofPath == "" {
		proofPath = "/var/lib/ly-route/vpp-native-capabilities.json"
	}
	nativePath := vpp.LoadNativePathRequestWithSharedManagement(proofPath, server.managementInterfaceID(ctx), server.runtimeDataInterfaces(ctx), server.now().UTC(), server.managementNetworkShared(ctx))
	nativePath.RequireSmartQoS = false
	dataplanePrepared := false
	if composition, ok := server.gatewayTransaction.(apply.ProductionGatewayComposition); ok {
		dataplanePrepared = composition.HasDataplaneController()
	}
	gatewayPlan := vpp.Plan{RequestID: requestID, NativePath: nativePath, DataplanePrepared: dataplanePrepared, Flow: compiledFlow, Policy: compiledPolicy}
	operations, buildErr := vpp.BuildOperations(gatewayPlan)
	dataplaneState := "native_ready"
	var dataplaneProof []vpp.PrerequisiteResult
	var warnings []string
	if buildErr != nil {
		var locked *vpp.DataplaneLockedError
		if !errors.As(buildErr, &locked) {
			return RuntimePlan{}, buildErr
		}
		operations = nil
		dataplaneState = locked.Code()
		dataplaneProof = locked.Prerequisites
		warnings = append(warnings, buildErr.Error())
	}

	artifacts := []serviceRuntime.RenderedArtifact{}
	if len(operations) > 0 {
		artifacts, err = serviceRuntime.RenderVPPOperations(operations)
		if err != nil {
			return RuntimePlan{}, err
		}
	}
	components := server.runtimeStatusComponents(ctx, artifacts, len(operations) > 0)
	return RuntimePlan{
		FlowIntent: flowIntent, CompiledFlow: compiledFlow, CompiledPolicy: compiledPolicy,
		ServiceArtifacts: summarizeRuntimeArtifacts(artifacts), RuntimeArtifacts: artifacts,
		VppOperations: operations, Components: components, Warnings: warnings,
		DataplaneState: dataplaneState, DataplaneProof: dataplaneProof, GatewayPlan: gatewayPlan,
	}, nil
}

func (server *Server) currentOrchestratorSecurityConfig(ctx context.Context) (trafficpolicy.Config, error) {
	securityItems, err := server.desiredItems(ctx, "security_acl")
	if err != nil {
		return trafficpolicy.Config{}, err
	}
	objectGroupItems, err := server.desiredItems(ctx, "object_group")
	if err != nil {
		return trafficpolicy.Config{}, err
	}
	return trafficpolicy.CompileConfigWithDomainIPSet(nil, securityItems, objectGroupItems, nil)
}
