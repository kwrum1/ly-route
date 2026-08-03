package apply

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"ly-route/backend/internal/runtime/dataplane"
	"ly-route/backend/internal/runtime/vpp"
)

func TestProductionDataplaneWrapperRollsBackDPDKWhenGatewayFails(t *testing.T) {
	trace := []string{}
	inner := &fakeGatewayRunner{trace: &trace, fail: true}
	controller := &fakeDataplaneController{trace: &trace}
	transaction := &productionGatewayWithDataplane{inner: inner, dataplane: controller, dpdkActive: map[string]bool{}}
	plan := dpdkGatewayPlan("txn-dpdk-wrapper")

	_, err := transaction.Run(context.Background(), plan)

	if err == nil || !reflect.DeepEqual(trace, []string{"dataplane-apply", "gateway-run", "dataplane-rollback"}) {
		t.Fatalf("error=%v trace=%v", err, trace)
	}
}

func TestProductionDataplaneWrapperMarksPlanPreparedBeforeGateway(t *testing.T) {
	trace := []string{}
	inner := &fakeGatewayRunner{trace: &trace}
	controller := &fakeDataplaneController{trace: &trace}
	transaction := &productionGatewayWithDataplane{inner: inner, dataplane: controller, dpdkActive: map[string]bool{}}
	plan := dpdkGatewayPlan("txn-dpdk-commit")

	if _, err := transaction.Run(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if !inner.prepared || !reflect.DeepEqual(trace, []string{"dataplane-apply", "gateway-run"}) {
		t.Fatalf("prepared=%v trace=%v", inner.prepared, trace)
	}
	if err := transaction.Rollback(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(trace, []string{"dataplane-apply", "gateway-run", "gateway-rollback", "dataplane-rollback"}) {
		t.Fatalf("rollback trace=%v", trace)
	}
}

type fakeGatewayRunner struct {
	trace    *[]string
	fail     bool
	prepared bool
}

func (runner *fakeGatewayRunner) Run(_ context.Context, plan Plan) (GatewayTransactionResult, error) {
	*runner.trace = append(*runner.trace, "gateway-run")
	runner.prepared = plan.GatewayPlan.DataplanePrepared
	if runner.fail {
		return GatewayTransactionResult{}, errors.New("gateway failure")
	}
	return GatewayTransactionResult{}, nil
}
func (runner *fakeGatewayRunner) Rollback(context.Context, Plan) error {
	*runner.trace = append(*runner.trace, "gateway-rollback")
	return nil
}

type fakeDataplaneController struct{ trace *[]string }

func (controller *fakeDataplaneController) Apply(context.Context, dataplane.Request) (dataplane.Receipt, error) {
	*controller.trace = append(*controller.trace, "dataplane-apply")
	return dataplane.Receipt{Changed: true}, nil
}
func (controller *fakeDataplaneController) Rollback(context.Context, string) (dataplane.Receipt, error) {
	*controller.trace = append(*controller.trace, "dataplane-rollback")
	return dataplane.Receipt{}, nil
}

func dpdkGatewayPlan(id string) Plan {
	now := fixedGatewayPlanClock()()
	proof := vpp.CapabilityProof{Tier: vpp.DataplaneTierDPDK, Hook: vpp.NativeHookDPDK, Mode: vpp.NativeModeDPDKVFIO, Source: vpp.ProofSourceRuntimeProbe, RuntimeVerified: true, HighPerformance: true, ObservedAt: now.Add(-1), ValidUntil: now.Add(1), PCIAddress: "0000:03:00.0", KernelDriver: "ixgbe", IOMMUGroup: "17", IOMMUProtected: true, VFIOAvailable: true, HugepagesAvailable: true, DPDKPluginAvailable: true}
	return Plan{Request: Request{TransactionID: id}, GatewayPlan: vpp.Plan{NativePath: vpp.NativePathRequest{ManagementInterface: "eth0", Now: now, Assignments: []vpp.NativeAssignment{{LinuxInterface: "eth1", Explicit: true, Candidates: []vpp.CapabilityProof{proof}}}}}}
}
