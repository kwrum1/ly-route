package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/product"
)

func TestRepository_failed_group_changes_preserve_topology_checksum(t *testing.T) {
	// Given
	ctx := context.Background()
	repository := newTestRepository(t, ctx)
	topology, err := ParseTopology(validTopologyInput())
	if err != nil {
		t.Fatalf("ParseTopology: %v", err)
	}
	if err := repository.Replace(ctx, topology); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	checksum, err := repository.Checksum(ctx)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	tests := []struct {
		name    string
		group   GroupInput
		wantErr error
	}{
		{
			name:    "management membership",
			group:   GroupInput{Name: "bad-management", Ports: []DirectedPortInput{{Interface: "eth0", Direction: DirectionLANFacing}, {Interface: "eth8", Direction: DirectionWANFacing}}},
			wantErr: ErrManagementMembership,
		},
		{
			name:    "logical ownership reuse",
			group:   GroupInput{Name: "bad-shared", Ports: []DirectedPortInput{{Interface: "eth3", Direction: DirectionLANFacing}, {Interface: "eth8", Direction: DirectionWANFacing}}},
			wantErr: ErrSharedInterface,
		},
		{
			name:    "bond membership",
			group:   GroupInput{Name: "bad-bond", Ports: []DirectedPortInput{{Interface: "bond-lan", Direction: DirectionLANFacing}, {Interface: "eth8", Direction: DirectionWANFacing}}},
			wantErr: ErrGroupBond,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// When
			group, parseErr := ParseGroup(test.group)
			if parseErr != nil {
				t.Fatalf("ParseGroup: %v", parseErr)
			}
			err := repository.CreateGroup(ctx, group)

			// Then
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("CreateGroup error = %v, want %v", err, test.wantErr)
			}
			after, checksumErr := repository.Checksum(ctx)
			if checksumErr != nil {
				t.Fatalf("Checksum after rejection: %v", checksumErr)
			}
			if after != checksum {
				t.Fatalf("checksum after rejection = %q, want unchanged %q", after, checksum)
			}
		})
	}
}

func TestRepository_rejects_group_creation_before_topology_with_zero_writes(t *testing.T) {
	// Given
	ctx := context.Background()
	repository := newTestRepository(t, ctx)
	group, err := ParseGroup(validTopologyInput().Groups[0])
	if err != nil {
		t.Fatalf("ParseGroup: %v", err)
	}

	// When
	err = repository.CreateGroup(ctx, group)

	// Then
	if !errors.Is(err, ErrTopologyNotFound) {
		t.Fatalf("CreateGroup error = %v, want ErrTopologyNotFound", err)
	}
	if _, err := repository.Checksum(ctx); !errors.Is(err, ErrTopologyNotFound) {
		t.Fatalf("Checksum error = %v, want ErrTopologyNotFound", err)
	}
}

func TestNewRepository_rejects_gateway_product_store(t *testing.T) {
	// Given
	ctx := context.Background()
	store, err := persistence.OpenForProduct(ctx, filepath.Join(t.TempDir(), "gateway.db"), product.Gateway().ID())
	if err != nil {
		t.Fatalf("OpenForProduct: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// When
	_, err = NewRepository(store, RepositoryOptions{})

	// Then
	if !errors.Is(err, persistence.ErrProductMismatch) {
		t.Fatalf("NewRepository error = %v, want ErrProductMismatch", err)
	}
}

func TestRepository_Replace_rejects_zero_topology_with_zero_writes(t *testing.T) {
	// Given
	ctx := context.Background()
	repository := newTestRepository(t, ctx)

	// When
	err := repository.Replace(ctx, Topology{})

	// Then
	if !errors.Is(err, ErrInvalidName) {
		t.Fatalf("Replace error = %v, want ErrInvalidName", err)
	}
	if _, err := repository.Checksum(ctx); !errors.Is(err, ErrTopologyNotFound) {
		t.Fatalf("Checksum error = %v, want ErrTopologyNotFound", err)
	}
}

func TestRepository_stale_group_update_returns_conflict_without_overwrite(t *testing.T) {
	// Given
	ctx := context.Background()
	repository := newTestRepository(t, ctx)
	topology, err := ParseTopology(validTopologyInput())
	if err != nil {
		t.Fatalf("ParseTopology: %v", err)
	}
	if err := repository.Replace(ctx, topology); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	stale, staleChecksum, err := repository.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	winner, err := ParseGroup(GroupInput{Name: "inline-north", Ports: []DirectedPortInput{{Interface: "eth6", Direction: DirectionLANFacing}, {Interface: "eth7", Direction: DirectionWANFacing}}})
	if err != nil {
		t.Fatalf("ParseGroup winner: %v", err)
	}
	if err := repository.CreateGroup(ctx, winner); err != nil {
		t.Fatalf("CreateGroup winner: %v", err)
	}
	loser, err := ParseGroup(GroupInput{Name: "inline-south", Ports: []DirectedPortInput{{Interface: "eth8", Direction: DirectionLANFacing}, {Interface: "eth9", Direction: DirectionWANFacing}}})
	if err != nil {
		t.Fatalf("ParseGroup loser: %v", err)
	}
	staleUpdate, err := stale.withGroups(append(stale.groupValues(), loser))
	if err != nil {
		t.Fatalf("build stale update: %v", err)
	}
	winnerChecksum, err := repository.Checksum(ctx)
	if err != nil {
		t.Fatalf("Checksum winner: %v", err)
	}

	// When
	err = repository.replaceIfChecksum(ctx, staleUpdate, staleChecksum)

	// Then
	if !errors.Is(err, ErrTopologyConflict) {
		t.Fatalf("replaceIfChecksum error = %v, want ErrTopologyConflict", err)
	}
	after, err := repository.Checksum(ctx)
	if err != nil || after != winnerChecksum {
		t.Fatalf("checksum after stale update = %q, %v; want %q", after, err, winnerChecksum)
	}
}

func TestRepository_persists_canonical_no_group_and_multi_group_topologies(t *testing.T) {
	// Given
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "orchestrator.db")
	store, err := persistence.OpenForProduct(ctx, dsn, product.Orchestrator().ID())
	if err != nil {
		t.Fatalf("OpenForProduct: %v", err)
	}
	repository, err := NewRepository(store, RepositoryOptions{Now: func() time.Time {
		return time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	input := validTopologyInput()
	input.Groups = append(input.Groups, GroupInput{Name: "inline-west", Ports: []DirectedPortInput{{Interface: "eth6", Direction: DirectionLANFacing}, {Interface: "eth7", Direction: DirectionWANFacing}}})
	topology, err := ParseTopology(input)
	if err != nil {
		t.Fatalf("ParseTopology: %v", err)
	}

	// When
	if err := repository.Replace(ctx, topology); err != nil {
		t.Fatalf("Replace multi-group topology: %v", err)
	}
	wantChecksum, err := repository.Checksum(ctx)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopenedStore, err := persistence.OpenForProduct(ctx, dsn, product.Orchestrator().ID())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopenedStore.Close() })
	reopened, err := NewRepository(reopenedStore, RepositoryOptions{})
	if err != nil {
		t.Fatalf("NewRepository reopened: %v", err)
	}
	got, err := reopened.Current(ctx)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}

	// Then
	view := got.View()
	if names := []string{view.Groups[0].Name, view.Groups[1].Name}; !slices.Equal(names, []string{"inline-east", "inline-west"}) {
		t.Fatalf("group order = %#v, want deterministic name order", names)
	}
	if gotChecksum, checksumErr := reopened.Checksum(ctx); checksumErr != nil || gotChecksum != wantChecksum {
		t.Fatalf("reopened checksum = %q, %v; want %q", gotChecksum, checksumErr, wantChecksum)
	}

	noGroups := validTopologyInput()
	noGroups.Groups = nil
	parsedNoGroups, err := ParseTopology(noGroups)
	if err != nil {
		t.Fatalf("ParseTopology no groups: %v", err)
	}
	if err := reopened.Replace(ctx, parsedNoGroups); err != nil {
		t.Fatalf("Replace no-group topology: %v", err)
	}
	withoutGroups, err := reopened.Current(ctx)
	if err != nil {
		t.Fatalf("Current no groups: %v", err)
	}
	if len(withoutGroups.View().Groups) != 0 {
		t.Fatalf("no-group topology readback = %#v, want no groups", withoutGroups.View().Groups)
	}
}

func newTestRepository(t *testing.T, ctx context.Context) *Repository {
	t.Helper()
	store, err := persistence.OpenForProduct(ctx, filepath.Join(t.TempDir(), "orchestrator.db"), product.Orchestrator().ID())
	if err != nil {
		t.Fatalf("OpenForProduct: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository, err := NewRepository(store, RepositoryOptions{Now: func() time.Time {
		return time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	return repository
}
