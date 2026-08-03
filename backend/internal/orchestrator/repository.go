package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/product"
)

const (
	topologyResourceType = "orchestrator_topology"
	topologyResourceID   = "active"
)

type RepositoryOptions struct {
	Now func() time.Time
}

type Repository struct {
	store *persistence.Store
	now   func() time.Time
	mu    sync.Mutex
}

func NewRepository(store *persistence.Store, options RepositoryOptions) (*Repository, error) {
	if store == nil {
		return nil, ErrRepositoryUnavailable
	}
	if store.ProductID() != product.Orchestrator().ID() {
		return nil, &persistence.ProductMismatchError{Expected: product.Orchestrator().ID(), Actual: store.ProductID()}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Repository{store: store, now: now}, nil
}

func (repository *Repository) Replace(ctx context.Context, topology Topology) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	validated, err := ParseTopology(topology.View().input())
	if err != nil {
		return fmt.Errorf("replace orchestrator topology: %w", err)
	}
	return repository.replace(ctx, validated)
}

func (repository *Repository) Current(ctx context.Context) (Topology, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.current(ctx)
}

func (repository *Repository) Snapshot(ctx context.Context) (Topology, string, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.snapshot(ctx)
}

func (repository *Repository) Checksum(ctx context.Context) (string, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	document, err := repository.store.Config(ctx, topologyResourceType, topologyResourceID)
	if errors.Is(err, persistence.ErrNotFound) {
		return "", ErrTopologyNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read orchestrator topology checksum: %w", err)
	}
	return document.PayloadHash, nil
}

func (repository *Repository) Delete(ctx context.Context) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, err := repository.store.Config(ctx, policyResourceType, policyResourceID); err == nil {
		return fmt.Errorf("%w: active policy requires topology", ErrDeletedPolicyReference)
	} else if !errors.Is(err, persistence.ErrNotFound) {
		return fmt.Errorf("read active policy before topology delete: %w", err)
	}
	if err := repository.store.DeleteConfig(ctx, topologyResourceType, topologyResourceID); err != nil {
		if errors.Is(err, persistence.ErrNotFound) {
			return ErrTopologyNotFound
		}
		return fmt.Errorf("delete orchestrator topology: %w", err)
	}
	return nil
}

func (repository *Repository) CreateGroup(ctx context.Context, group Group) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	topology, checksum, err := repository.snapshot(ctx)
	if err != nil {
		return err
	}
	groups := topology.groupValues()
	for _, current := range groups {
		if current.Name() == group.Name() {
			return fmt.Errorf("%w: %q", ErrDuplicateGroup, group.Name())
		}
	}
	groups = append(groups, group)
	updated, err := topology.withGroups(groups)
	if err != nil {
		return err
	}
	return repository.replaceIfChecksum(ctx, updated, checksum)
}

func (repository *Repository) ReplaceGroup(ctx context.Context, name string, group Group) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	topology, checksum, err := repository.snapshot(ctx)
	if err != nil {
		return err
	}
	groups := topology.groupValues()
	found := false
	for index, current := range groups {
		if current.Name() == name {
			groups[index] = group
			found = true
		}
	}
	if !found {
		return fmt.Errorf("%w: %q", ErrGroupNotFound, name)
	}
	updated, err := topology.withGroups(groups)
	if err != nil {
		return err
	}
	return repository.replaceIfChecksum(ctx, updated, checksum)
}

func (repository *Repository) DeleteGroup(ctx context.Context, name string) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	topology, checksum, err := repository.snapshot(ctx)
	if err != nil {
		return err
	}
	groups := topology.groupValues()
	filtered := make([]Group, 0, len(groups))
	for _, group := range groups {
		if group.Name() != name {
			filtered = append(filtered, group)
		}
	}
	if len(filtered) == len(groups) {
		return fmt.Errorf("%w: %q", ErrGroupNotFound, name)
	}
	updated, err := topology.withGroups(filtered)
	if err != nil {
		return err
	}
	return repository.replaceIfChecksum(ctx, updated, checksum)
}

func (repository *Repository) replace(ctx context.Context, topology Topology) error {
	if err := repository.validatePersistedPolicy(ctx, topology); err != nil {
		return err
	}
	document, err := repository.document(topology)
	if err != nil {
		return err
	}
	if err := repository.store.ReplaceConfigsForTypes(ctx, []string{topologyResourceType}, []persistence.ConfigDocument{document}); err != nil {
		return fmt.Errorf("replace orchestrator topology: %w", err)
	}
	return nil
}

func (repository *Repository) replaceIfChecksum(ctx context.Context, topology Topology, expectedHash string) error {
	if err := repository.validatePersistedPolicy(ctx, topology); err != nil {
		return err
	}
	document, err := repository.document(topology)
	if err != nil {
		return err
	}
	if err := repository.store.ReplaceConfigIfHash(ctx, document, expectedHash); err != nil {
		if errors.Is(err, persistence.ErrWriteConflict) {
			return ErrTopologyConflict
		}
		return fmt.Errorf("replace orchestrator topology: %w", err)
	}
	return nil
}

func (repository *Repository) validatePersistedPolicy(ctx context.Context, topology Topology) error {
	document, err := repository.store.Config(ctx, policyResourceType, policyResourceID)
	if errors.Is(err, persistence.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read active policy before topology mutation: %w", err)
	}
	if err := persistence.VerifyPayload(document.Payload, document.PayloadHash); err != nil {
		return fmt.Errorf("verify active policy before topology mutation: %w", err)
	}
	var view PolicyView
	if err := json.Unmarshal(document.Payload, &view); err != nil {
		return fmt.Errorf("decode active policy before topology mutation: %w", err)
	}
	if _, err := ParsePolicy(topology, view.input()); err != nil {
		return fmt.Errorf("topology mutation invalidates active policy: %w", err)
	}
	return nil
}

func (repository *Repository) document(topology Topology) (persistence.ConfigDocument, error) {
	payload, checksum, err := persistence.MarshalPayload(topology.View())
	if err != nil {
		return persistence.ConfigDocument{}, fmt.Errorf("encode orchestrator topology: %w", err)
	}
	return persistence.ConfigDocument{
		ResourceType: topologyResourceType,
		ResourceID:   topologyResourceID,
		Payload:      payload,
		PayloadHash:  checksum,
		UpdatedAt:    repository.now().UTC(),
	}, nil
}

func (repository *Repository) current(ctx context.Context) (Topology, error) {
	topology, _, err := repository.snapshot(ctx)
	return topology, err
}

func (repository *Repository) snapshot(ctx context.Context) (Topology, string, error) {
	document, err := repository.store.Config(ctx, topologyResourceType, topologyResourceID)
	if errors.Is(err, persistence.ErrNotFound) {
		return Topology{}, "", ErrTopologyNotFound
	}
	if err != nil {
		return Topology{}, "", fmt.Errorf("read orchestrator topology: %w", err)
	}
	if err := persistence.VerifyPayload(document.Payload, document.PayloadHash); err != nil {
		return Topology{}, "", fmt.Errorf("read orchestrator topology: %w", err)
	}
	var view TopologyView
	if err := json.Unmarshal(document.Payload, &view); err != nil {
		return Topology{}, "", fmt.Errorf("decode orchestrator topology: %w", err)
	}
	topology, err := ParseTopology(view.input())
	if err != nil {
		return Topology{}, "", fmt.Errorf("decode orchestrator topology: %w", err)
	}
	return topology, document.PayloadHash, nil
}
