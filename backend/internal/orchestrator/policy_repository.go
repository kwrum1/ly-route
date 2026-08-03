package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"ly-route/backend/internal/persistence"
)

const (
	policyResourceType = "orchestrator_policy"
	policyResourceID   = "active"
)

var ErrPolicyNotFound = errors.New("orchestration policy not found")

type PolicyRepository interface {
	ReplacePolicy(context.Context, Policy) error
	PolicySnapshot(context.Context) (Policy, string, error)
	DeletePolicy(context.Context) error
}

var _ PolicyRepository = (*Repository)(nil)

func (repository *Repository) ReplacePolicy(ctx context.Context, policy Policy) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	topology, _, err := repository.snapshot(ctx)
	if err != nil {
		return err
	}
	validated, err := ParsePolicy(topology, policy.View().input())
	if err != nil {
		return err
	}
	payload, checksum, err := persistence.MarshalPayload(validated.View())
	if err != nil {
		return fmt.Errorf("encode orchestration policy: %w", err)
	}
	document := persistence.ConfigDocument{ResourceType: policyResourceType, ResourceID: policyResourceID, Payload: payload, PayloadHash: checksum, UpdatedAt: repository.now().UTC()}
	if err := repository.store.ReplaceConfigsForTypes(ctx, []string{policyResourceType}, []persistence.ConfigDocument{document}); err != nil {
		return fmt.Errorf("replace orchestration policy: %w", err)
	}
	return nil
}

func (repository *Repository) PolicySnapshot(ctx context.Context) (Policy, string, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	topology, _, err := repository.snapshot(ctx)
	if err != nil {
		return Policy{}, "", err
	}
	document, err := repository.store.Config(ctx, policyResourceType, policyResourceID)
	if errors.Is(err, persistence.ErrNotFound) {
		return Policy{}, "", ErrPolicyNotFound
	}
	if err != nil {
		return Policy{}, "", fmt.Errorf("read orchestration policy: %w", err)
	}
	if err := persistence.VerifyPayload(document.Payload, document.PayloadHash); err != nil {
		return Policy{}, "", err
	}
	var view PolicyView
	if err := json.Unmarshal(document.Payload, &view); err != nil {
		return Policy{}, "", fmt.Errorf("decode orchestration policy: %w", err)
	}
	policy, err := ParsePolicy(topology, view.input())
	if err != nil {
		return Policy{}, "", fmt.Errorf("decode orchestration policy: %w", err)
	}
	return policy, document.PayloadHash, nil
}

func (repository *Repository) DeletePolicy(ctx context.Context) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if err := repository.store.DeleteConfig(ctx, policyResourceType, policyResourceID); err != nil {
		if errors.Is(err, persistence.ErrNotFound) {
			return ErrPolicyNotFound
		}
		return fmt.Errorf("delete orchestration policy: %w", err)
	}
	return nil
}

func (view PolicyView) input() PolicyInput {
	return PolicyInput{SchemaVersion: view.SchemaVersion, IPObjects: policyObjectInputs(view.IPObjects), Groups: policyGroupInputs(view.Groups), Default: view.Default}
}
func policyObjectInputs(views []IPObjectView) []IPObjectInput {
	result := make([]IPObjectInput, 0, len(views))
	for _, view := range views {
		result = append(result, IPObjectInput{ID: view.ID, Prefixes: append([]string(nil), view.Prefixes...)})
	}
	return result
}
func policyGroupInputs(views []PolicyGroupView) []PolicyGroupInput {
	result := make([]PolicyGroupInput, 0, len(views))
	for _, view := range views {
		result = append(result, PolicyGroupInput{ID: view.ID, Position: view.Position, Rules: append([]PolicyRuleInput(nil), policyRuleInputs(view.Rules)...)})
	}
	return result
}
func policyRuleInputs(views []PolicyRuleView) []PolicyRuleInput {
	result := make([]PolicyRuleInput, 0, len(views))
	for _, view := range views {
		result = append(result, PolicyRuleInput{ID: view.ID, Sequence: view.Sequence, Match: view.Match, Action: view.Action})
	}
	return result
}
