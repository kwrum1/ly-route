package dataplane

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"ly-route/backend/internal/runtime/vpp"
)

var ErrTransactionFailed = errors.New("dataplane transaction failed")

type DeviceState struct {
	LinuxInterface string `json:"linux_interface"`
	PCIAddress     string `json:"pci_address"`
	KernelDriver   string `json:"kernel_driver"`
	LinkUp         bool   `json:"link_up"`
}

type Snapshot struct {
	TransactionID string        `json:"transaction_id"`
	StartupConfig []byte        `json:"startup_config"`
	Devices       []DeviceState `json:"devices"`
}

type Receipt struct {
	TransactionID string            `json:"transaction_id"`
	Tier          vpp.DataplaneTier `json:"tier"`
	Status        string            `json:"status"`
	AppliedAt     time.Time         `json:"applied_at"`
	Attachments   int               `json:"attachments"`
	RolledBack    bool              `json:"rolled_back,omitempty"`
	Changed       bool              `json:"changed"`
}

type Request struct {
	TransactionID string
	Path          vpp.NativePath
}

type ActiveState struct {
	Path      vpp.NativePath `json:"path"`
	Snapshot  Snapshot       `json:"snapshot"`
	AppliedAt time.Time      `json:"applied_at"`
}

type ActiveStateStore interface {
	LoadActiveState(context.Context) (ActiveState, bool, error)
	SaveActiveState(context.Context, ActiveState) error
	ClearActiveState(context.Context) error
}

// Host owns privileged Linux/VPP operations. Its implementation must make
// ConfigureDPDK atomic and Restore idempotent so recovery is safe to repeat.
type Host interface {
	Snapshot(context.Context, Request) (Snapshot, error)
	StopVPP(context.Context) error
	ConfigureDPDK(context.Context, vpp.NativePath, Snapshot) error
	StartVPP(context.Context) error
	VerifyDPDK(context.Context, vpp.NativePath) error
	Restore(context.Context, Snapshot) error
}

type Transaction struct {
	Host Host
	Now  func() time.Time

	mu          sync.Mutex
	active      map[string]Snapshot
	current     *vpp.NativePath
	initialized bool
}

func (transaction *Transaction) Apply(ctx context.Context, request Request) (Receipt, error) {
	if request.TransactionID == "" {
		return Receipt{}, fmt.Errorf("%w: transaction ID is required", ErrTransactionFailed)
	}
	if request.Path.Tier == vpp.DataplaneTierNative {
		transaction.mu.Lock()
		defer transaction.mu.Unlock()
		if err := transaction.loadCurrent(ctx); err != nil {
			return Receipt{}, fmt.Errorf("%w: load active state: %w", ErrTransactionFailed, err)
		}
		if transaction.current != nil {
			if !SameNativePath(*transaction.current, request.Path) {
				return Receipt{}, fmt.Errorf("%w: requested native path differs from the active device-wide path", ErrTransactionFailed)
			}
			receipt := transaction.receipt(request, false)
			receipt.Status = "already_applied"
			return receipt, nil
		}
		return transaction.receipt(request, false), nil
	}
	if request.Path.Tier != vpp.DataplaneTierDPDK || len(request.Path.Attachments) == 0 {
		return Receipt{}, fmt.Errorf("%w: unsupported or empty tier %q", ErrTransactionFailed, request.Path.Tier)
	}
	if transaction.Host == nil {
		return Receipt{}, fmt.Errorf("%w: privileged host adapter is unavailable", ErrTransactionFailed)
	}
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	if err := transaction.loadCurrent(ctx); err != nil {
		return Receipt{}, fmt.Errorf("%w: load active state: %w", ErrTransactionFailed, err)
	}
	if transaction.current != nil {
		if !SameNativePath(*transaction.current, request.Path) {
			return Receipt{}, fmt.Errorf("%w: requested DPDK path differs from the active device-wide path", ErrTransactionFailed)
		}
		if err := transaction.Host.VerifyDPDK(ctx, request.Path); err == nil {
			receipt := transaction.receipt(request, false)
			receipt.Status = "already_applied"
			return receipt, nil
		}
		// active.json survives a control-plane restart, while VPP itself may
		// have restarted with an empty graph. Reapply the persisted path so the
		// subsequent gateway transaction sees real hardware interfaces.
		transaction.current = nil
	}

	snapshot, err := transaction.Host.Snapshot(ctx, request)
	if err != nil {
		return Receipt{}, fmt.Errorf("%w: snapshot: %w", ErrTransactionFailed, err)
	}
	mutated := false
	fail := func(phase string, cause error) (Receipt, error) {
		if !mutated {
			return Receipt{}, fmt.Errorf("%w: %s: %w", ErrTransactionFailed, phase, cause)
		}
		rollbackErr := transaction.Host.Restore(ctx, snapshot)
		return Receipt{}, fmt.Errorf("%w: %s: %w", ErrTransactionFailed, phase, errors.Join(cause, rollbackErr))
	}
	if err := transaction.Host.StopVPP(ctx); err != nil {
		return fail("stop-vpp", err)
	}
	mutated = true
	if err := transaction.Host.ConfigureDPDK(ctx, request.Path, snapshot); err != nil {
		return fail("configure-dpdk", err)
	}
	if err := transaction.Host.StartVPP(ctx); err != nil {
		return fail("start-vpp", err)
	}
	if err := transaction.Host.VerifyDPDK(ctx, request.Path); err != nil {
		return fail("readback", err)
	}
	if store, ok := transaction.Host.(ActiveStateStore); ok {
		if err := store.SaveActiveState(ctx, ActiveState{Path: request.Path, Snapshot: snapshot, AppliedAt: transaction.now()}); err != nil {
			return fail("persist-state", err)
		}
	}

	if transaction.active == nil {
		transaction.active = map[string]Snapshot{}
	}
	transaction.active[request.TransactionID] = snapshot
	current := request.Path
	transaction.current = &current
	receipt := transaction.receipt(request, false)
	receipt.Changed = true
	return receipt, nil
}

func (transaction *Transaction) Rollback(ctx context.Context, transactionID string) (Receipt, error) {
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	snapshot, found := transaction.active[transactionID]
	delete(transaction.active, transactionID)
	if !found {
		return Receipt{}, fmt.Errorf("%w: no active snapshot for %q", ErrTransactionFailed, transactionID)
	}
	if err := transaction.Host.Restore(ctx, snapshot); err != nil {
		return Receipt{}, fmt.Errorf("%w: rollback: %w", ErrTransactionFailed, err)
	}
	transaction.current = nil
	if store, ok := transaction.Host.(ActiveStateStore); ok {
		if err := store.ClearActiveState(ctx); err != nil {
			return Receipt{}, fmt.Errorf("%w: clear active state: %w", ErrTransactionFailed, err)
		}
	}
	tier := vpp.DataplaneTierDPDK
	if transaction.current != nil {
		tier = transaction.current.Tier
	}
	return Receipt{TransactionID: transactionID, Tier: tier, Status: "rolled_back", AppliedAt: transaction.now(), Attachments: len(snapshot.Devices), RolledBack: true, Changed: true}, nil
}

func (transaction *Transaction) loadCurrent(ctx context.Context) error {
	if transaction.initialized {
		return nil
	}
	store, ok := transaction.Host.(ActiveStateStore)
	if !ok {
		transaction.initialized = true
		return nil
	}
	state, found, err := store.LoadActiveState(ctx)
	if err != nil {
		return err
	}
	transaction.initialized = true
	if found {
		path := state.Path
		transaction.current = &path
	}
	return nil
}

func SameNativePath(left, right vpp.NativePath) bool {
	if left.Tier != right.Tier || left.SmartQoS != right.SmartQoS || len(left.Attachments) != len(right.Attachments) {
		return false
	}
	for index := range left.Attachments {
		a := left.Attachments[index]
		b := right.Attachments[index]
		if a.LinuxInterface != b.LinuxInterface || a.VPPInterface != b.VPPInterface || a.Tier != b.Tier || a.Hook != b.Hook || a.Mode != b.Mode || a.PCIAddress != b.PCIAddress || a.IOMMUGroup != b.IOMMUGroup {
			return false
		}
	}
	return true
}

func (transaction *Transaction) receipt(request Request, rolledBack bool) Receipt {
	return Receipt{TransactionID: request.TransactionID, Tier: request.Path.Tier, Status: "applied", AppliedAt: transaction.now(), Attachments: len(request.Path.Attachments), RolledBack: rolledBack}
}

func (transaction *Transaction) now() time.Time {
	if transaction.Now == nil {
		return time.Now().UTC()
	}
	return transaction.Now().UTC()
}
