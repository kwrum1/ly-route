package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ly-route/backend/internal/persistence"
)

const serviceChainIntentResourceType = "orchestrator_service_chain_intent"

type ServiceChainIntentRecord struct {
	ID        string          `json:"id"`
	Payload   json.RawMessage `json:"payload"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func (repository *Repository) SaveServiceChainIntent(ctx context.Context, id string, payload json.RawMessage) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if id == "" || len(payload) == 0 || !json.Valid(payload) {
		return fmt.Errorf("%w: invalid service-chain intent", ErrInvalidServiceChain)
	}
	document := persistence.ConfigDocument{ResourceType: serviceChainIntentResourceType, ResourceID: id, Payload: append(json.RawMessage(nil), payload...), UpdatedAt: repository.now().UTC()}
	if err := repository.store.SaveConfig(ctx, document); err != nil {
		return fmt.Errorf("save service-chain intent: %w", err)
	}
	return nil
}

func (repository *Repository) ServiceChainIntents(ctx context.Context) ([]ServiceChainIntentRecord, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	documents, err := repository.store.Configs(ctx, serviceChainIntentResourceType)
	if err != nil {
		return nil, fmt.Errorf("read service-chain intents: %w", err)
	}
	records := make([]ServiceChainIntentRecord, 0, len(documents))
	for _, document := range documents {
		if !json.Valid(document.Payload) {
			return nil, fmt.Errorf("read service-chain intent %q: invalid JSON", document.ResourceID)
		}
		records = append(records, ServiceChainIntentRecord{ID: document.ResourceID, Payload: append(json.RawMessage(nil), document.Payload...), UpdatedAt: document.UpdatedAt})
	}
	return records, nil
}

func (repository *Repository) DeleteServiceChainIntent(ctx context.Context, id string) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if err := repository.store.DeleteConfig(ctx, serviceChainIntentResourceType, id); err != nil && !errors.Is(err, persistence.ErrNotFound) {
		return fmt.Errorf("delete service-chain intent: %w", err)
	}
	return nil
}
