package dataplane

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"ly-route/backend/internal/runtime/vpp"
)

func TestDPDKTransactionAppliesOnlyAfterReadback(t *testing.T) {
	host := &fakeHost{}
	transaction := Transaction{Host: host, Now: fixedClock}

	receipt, err := transaction.Apply(context.Background(), dpdkRequest("txn-1"))

	if err != nil {
		t.Fatal(err)
	}
	want := []string{"snapshot", "stop-vpp", "configure-dpdk", "start-vpp", "verify-dpdk"}
	if !reflect.DeepEqual(host.trace, want) || receipt.Status != "applied" || receipt.Tier != vpp.DataplaneTierDPDK || receipt.Attachments != 1 || !receipt.Changed {
		t.Fatalf("trace=%v receipt=%#v", host.trace, receipt)
	}
}

func TestDPDKTransactionReusesIdenticalDeviceWidePathWithoutRestart(t *testing.T) {
	host := &fakeHost{}
	transaction := Transaction{Host: host, Now: fixedClock}
	if _, err := transaction.Apply(context.Background(), dpdkRequest("txn-first")); err != nil {
		t.Fatal(err)
	}
	traceAfterFirst := append([]string(nil), host.trace...)
	receipt, err := transaction.Apply(context.Background(), dpdkRequest("txn-second"))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "already_applied" || receipt.Changed || !reflect.DeepEqual(host.trace, traceAfterFirst) {
		t.Fatalf("receipt=%#v trace=%v, want unchanged trace %v", receipt, host.trace, traceAfterFirst)
	}
	if _, err := transaction.Rollback(context.Background(), "txn-second"); err == nil {
		t.Fatal("reused path unexpectedly owned a rollback snapshot")
	}
}

func TestDPDKTransactionReusesPersistedPathAfterControlPlaneRestart(t *testing.T) {
	host := &fakeHost{}
	first := Transaction{Host: host, Now: fixedClock}
	if _, err := first.Apply(context.Background(), dpdkRequest("txn-first")); err != nil {
		t.Fatal(err)
	}
	traceAfterFirst := append([]string(nil), host.trace...)
	restarted := Transaction{Host: host, Now: fixedClock}
	receipt, err := restarted.Apply(context.Background(), dpdkRequest("txn-after-restart"))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "already_applied" || receipt.Changed || !reflect.DeepEqual(host.trace, traceAfterFirst) {
		t.Fatalf("receipt=%#v trace=%v, want persisted path reuse", receipt, host.trace)
	}
}

func TestDPDKTransactionRejectsDifferentPathWhileDeviceWidePathIsActive(t *testing.T) {
	host := &fakeHost{}
	transaction := Transaction{Host: host, Now: fixedClock}
	if _, err := transaction.Apply(context.Background(), dpdkRequest("txn-first")); err != nil {
		t.Fatal(err)
	}
	different := dpdkRequest("txn-different")
	different.Path.SmartQoS = true
	if _, err := transaction.Apply(context.Background(), different); !errors.Is(err, ErrTransactionFailed) {
		t.Fatalf("different active path error = %v", err)
	}
}

func TestDPDKTransactionRestoresSnapshotOnEveryPostStopFailure(t *testing.T) {
	for _, phase := range []string{"configure-dpdk", "start-vpp", "verify-dpdk"} {
		t.Run(phase, func(t *testing.T) {
			host := &fakeHost{fail: phase}
			_, err := (&Transaction{Host: host, Now: fixedClock}).Apply(context.Background(), dpdkRequest("txn-fail"))
			if !errors.Is(err, ErrTransactionFailed) || len(host.trace) == 0 || host.trace[len(host.trace)-1] != "restore" {
				t.Fatalf("error=%v trace=%v", err, host.trace)
			}
		})
	}
}

func TestDPDKCommittedTransactionCanBeRolledBack(t *testing.T) {
	host := &fakeHost{}
	transaction := Transaction{Host: host, Now: fixedClock}
	if _, err := transaction.Apply(context.Background(), dpdkRequest("txn-rollback")); err != nil {
		t.Fatal(err)
	}
	receipt, err := transaction.Rollback(context.Background(), "txn-rollback")
	if err != nil || !receipt.RolledBack || host.trace[len(host.trace)-1] != "restore" {
		t.Fatalf("error=%v receipt=%#v trace=%v", err, receipt, host.trace)
	}
}

func TestNativeTierDoesNotTouchPrivilegedHost(t *testing.T) {
	host := &fakeHost{}
	request := Request{TransactionID: "txn-native", Path: vpp.NativePath{Tier: vpp.DataplaneTierNative, Attachments: []vpp.NativeAttachment{{LinuxInterface: "eth1"}}}}
	if _, err := (&Transaction{Host: host, Now: fixedClock}).Apply(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(host.trace) != 0 {
		t.Fatalf("privileged trace = %v", host.trace)
	}
}

type fakeHost struct {
	trace  []string
	fail   string
	active *ActiveState
}

func (host *fakeHost) call(name string) error {
	host.trace = append(host.trace, name)
	if host.fail == name {
		return errors.New("injected " + name + " failure")
	}
	return nil
}

func (host *fakeHost) Snapshot(_ context.Context, request Request) (Snapshot, error) {
	err := host.call("snapshot")
	return Snapshot{TransactionID: request.TransactionID, Devices: []DeviceState{{LinuxInterface: "eth1", PCIAddress: "0000:03:00.0", KernelDriver: "ixgbe", LinkUp: true}}}, err
}
func (host *fakeHost) StopVPP(context.Context) error { return host.call("stop-vpp") }
func (host *fakeHost) ConfigureDPDK(context.Context, vpp.NativePath, Snapshot) error {
	return host.call("configure-dpdk")
}
func (host *fakeHost) StartVPP(context.Context) error { return host.call("start-vpp") }
func (host *fakeHost) VerifyDPDK(context.Context, vpp.NativePath) error {
	return host.call("verify-dpdk")
}
func (host *fakeHost) Restore(context.Context, Snapshot) error { return host.call("restore") }
func (host *fakeHost) LoadActiveState(context.Context) (ActiveState, bool, error) {
	if host.active == nil {
		return ActiveState{}, false, nil
	}
	return *host.active, true, nil
}
func (host *fakeHost) SaveActiveState(_ context.Context, state ActiveState) error {
	host.active = &state
	return nil
}
func (host *fakeHost) ClearActiveState(context.Context) error {
	host.active = nil
	return nil
}

func dpdkRequest(id string) Request {
	return Request{TransactionID: id, Path: vpp.NativePath{Tier: vpp.DataplaneTierDPDK, Attachments: []vpp.NativeAttachment{{LinuxInterface: "eth1", VPPInterface: "lyroute-eth1", Tier: vpp.DataplaneTierDPDK, Hook: vpp.NativeHookDPDK, Mode: vpp.NativeModeDPDKVFIO, PCIAddress: "0000:03:00.0", KernelDriver: "ixgbe", IOMMUGroup: "17"}}}}
}

func fixedClock() time.Time { return time.Date(2026, 7, 30, 14, 0, 0, 0, time.UTC) }
