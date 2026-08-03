package orchestratorapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"ly-route/backend/internal/orchestrator"
	"ly-route/backend/internal/runtime/vpp"
)

type ServiceChainReconcileResult struct {
	IntentID       string                     `json:"intent_id"`
	State          string                     `json:"state"`
	BypassedGroups []string                   `json:"bypassed_groups,omitempty"`
	Health         []orchestrator.GroupHealth `json:"health,omitempty"`
	Receipt        vpp.Receipt                `json:"receipt"`
	ReconciledAt   time.Time                  `json:"reconciled_at"`
	Error          string                     `json:"error,omitempty"`
}

type reconcileState struct {
	once    sync.Once
	mu      sync.RWMutex
	results []ServiceChainReconcileResult
}

func (handler *Handler) StartServiceChainReconciler(ctx context.Context, interval time.Duration) {
	if interval <= 0 || handler.runtime == nil {
		return
	}
	handler.reconcile.once.Do(func() {
		go func() {
			handler.ReconcileServiceChains(ctx)
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					handler.ReconcileServiceChains(ctx)
				}
			}
		}()
	})
}

func (handler *Handler) ReconcileServiceChains(ctx context.Context) []ServiceChainReconcileResult {
	repository, ok := handler.repository.(ServiceChainIntentRepository)
	if !ok || handler.runtime == nil {
		return nil
	}
	intents, err := repository.ServiceChainIntents(ctx)
	if err != nil {
		results := []ServiceChainReconcileResult{{State: "failed", ReconciledAt: time.Now().UTC(), Error: "service-chain intents are unavailable"}}
		handler.storeReconcileResults(results)
		return results
	}
	results := make([]ServiceChainReconcileResult, 0, len(intents))
	for _, intent := range intents {
		results = append(results, handler.reconcileServiceChainIntent(ctx, intent))
	}
	sort.Slice(results, func(i, j int) bool { return results[i].IntentID < results[j].IntentID })
	handler.storeReconcileResults(results)
	return results
}

func (handler *Handler) reconcileServiceChainIntent(ctx context.Context, intent orchestrator.ServiceChainIntentRecord) ServiceChainReconcileResult {
	result := ServiceChainReconcileResult{IntentID: intent.ID, State: "failed", ReconciledAt: time.Now().UTC()}
	fail := func(err error) ServiceChainReconcileResult {
		result.Error = err.Error()
		return result
	}
	var input serviceChainApplyRequest
	if err := json.Unmarshal(intent.Payload, &input); err != nil {
		return fail(fmt.Errorf("persisted service-chain intent is invalid"))
	}
	topology, _, err := handler.repository.Snapshot(ctx)
	if err != nil {
		return fail(err)
	}
	policyRepository, ok := handler.policyRepository()
	if !ok {
		return fail(orchestrator.ErrRepositoryUnavailable)
	}
	policy, _, err := policyRepository.PolicySnapshot(ctx)
	if err != nil {
		return fail(err)
	}
	flow, err := orchestrator.ParseFlow(input.Flow)
	if err != nil {
		return fail(err)
	}
	prelude, err := orchestrator.ParsePrelude(input.Prelude)
	if err != nil {
		return fail(err)
	}
	path, err := orchestrator.CompilePolicy(policy, flow, prelude)
	if err != nil || path.Exit != orchestrator.PathExitLAN {
		return fail(fmt.Errorf("persisted service-chain policy no longer resolves to LAN"))
	}
	desired, err := orchestrator.CompileServiceChain(topology, flow, path, input.Bindings)
	if err != nil {
		return fail(err)
	}
	unavailable := map[string]bool{}
	if healthRuntime, ok := handler.runtime.(ServiceChainHealthRuntime); ok {
		unavailable, result.Health, err = healthRuntime.ServiceChainUnavailable(ctx, input.Bindings)
		if err != nil {
			return fail(err)
		}
	}
	active, err := orchestrator.CompileServiceChainWithHealth(topology, flow, path, input.Bindings, unavailable)
	if err != nil {
		return fail(err)
	}
	active.ID = desired.ID
	transactionID := fmt.Sprintf("reconcile-%s-%d", intent.ID, result.ReconciledAt.UnixNano())
	var applied vpp.ServiceChainApplyResult
	if transitionRuntime, ok := handler.runtime.(ServiceChainTransitionRuntime); ok {
		applied, err = transitionRuntime.ApplyServiceChainTransition(ctx, transactionID, desired, active, input.Attachments)
	} else {
		applied, err = handler.runtime.ApplyServiceChain(ctx, transactionID, active, input.Attachments)
	}
	if err != nil {
		return fail(err)
	}
	result.State = "applied"
	result.BypassedGroups = append([]string(nil), active.BypassedGroups...)
	result.Receipt = applied.Receipt
	return result
}

func (handler *Handler) storeReconcileResults(results []ServiceChainReconcileResult) {
	handler.reconcile.mu.Lock()
	defer handler.reconcile.mu.Unlock()
	handler.reconcile.results = append([]ServiceChainReconcileResult(nil), results...)
}

func (handler *Handler) handleServiceChainStatus(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeAPIError(writer, http.StatusMethodNotAllowed, fmt.Errorf("method is not allowed"))
		return
	}
	if !handler.authorize(writer, request, PermissionRead) {
		return
	}
	handler.reconcile.mu.RLock()
	results := append([]ServiceChainReconcileResult(nil), handler.reconcile.results...)
	handler.reconcile.mu.RUnlock()
	writeJSON(writer, http.StatusOK, map[string]any{"items": results})
}
