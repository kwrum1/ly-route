package orchestrator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/product"
)

func TestServiceChainIntentPersistsAcrossRepositoryRestart(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "orchestrator.db")
	store, err := persistence.OpenForProduct(ctx, databasePath, product.Orchestrator().ID())
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(store, RepositoryOptions{Now: func() time.Time { return time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"flow":{"source_ip":"192.0.2.10"}}`)
	if err := repository.SaveServiceChainIntent(ctx, "chain-a", payload); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedStore, err := persistence.OpenForProduct(ctx, databasePath, product.Orchestrator().ID())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopenedStore.Close() })
	reopened, err := NewRepository(reopenedStore, RepositoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	records, err := reopened.ServiceChainIntents(ctx)
	if err != nil || len(records) != 1 || records[0].ID != "chain-a" || string(records[0].Payload) != string(payload) {
		t.Fatalf("reopened intents = %#v, err=%v", records, err)
	}
}
