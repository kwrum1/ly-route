package httpapi

import (
	"context"
	"errors"
	"testing"

	"ly-route/backend/internal/runtime/apply"
	serviceRuntime "ly-route/backend/internal/runtime/service"
)

func TestRollbackRuntimePlanJoinsGatewayAndServiceErrors(t *testing.T) {
	// Given
	gatewayErr := errors.New("gateway cleanup failed")
	serviceErr := errors.New("service cleanup failed")
	controller := &httpServiceController{rollbackErrs: map[serviceRuntime.ServiceName]error{serviceRuntime.SmartDNS: serviceErr}}
	server := &Server{
		services:           &serviceRuntime.Runtime{Controller: controller},
		gatewayTransaction: rollbackGateway{err: gatewayErr},
	}
	plan := RuntimePlan{RuntimeArtifacts: []serviceRuntime.RenderedArtifact{serviceRuntime.NewArtifact(serviceRuntime.SmartDNS, "/etc/smartdns.conf", "{}", "reload")}}

	// When
	err := server.rollbackRuntimePlan(context.Background(), plan, apply.Plan{Previous: apply.PreviousState{Available: true}})

	// Then
	if !errors.Is(err, gatewayErr) || !errors.Is(err, serviceErr) {
		t.Fatalf("rollback error = %v, want Gateway and service errors", err)
	}
}

type rollbackGateway struct{ err error }

func (gateway rollbackGateway) Run(context.Context, apply.Plan) (apply.GatewayTransactionResult, error) {
	return apply.GatewayTransactionResult{}, nil
}

func (gateway rollbackGateway) Rollback(context.Context, apply.Plan) error { return gateway.err }
