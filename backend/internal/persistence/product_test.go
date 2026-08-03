package persistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"ly-route/backend/internal/product"
)

func TestProductMetadataInitializesExactlyOneProductAndSchemaVersion(t *testing.T) {
	// Given
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gateway.db")

	// When
	store, err := OpenForProduct(ctx, path, product.Gateway().ID())
	if err != nil {
		t.Fatalf("open Gateway store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Then
	var count, version int
	var productID string
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*), product_id, schema_version FROM product_metadata`).Scan(&count, &productID, &version); err != nil {
		t.Fatalf("read product metadata: %v", err)
	}
	if count != 1 || productID != "gateway" || version != SchemaVersion {
		t.Fatalf("metadata count=%d product=%q version=%d, want 1 gateway %d", count, productID, version, SchemaVersion)
	}
}

func TestProductMetadataNewOrchestratorDatabaseUsesOrchestratorID(t *testing.T) {
	// Given
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "orchestrator.db")

	// When
	store, err := OpenForProduct(ctx, path, product.Orchestrator().ID())
	if err != nil {
		t.Fatalf("open Orchestrator store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Then
	if store.ProductID() != product.Orchestrator().ID() {
		t.Fatalf("store product = %q, want orchestrator", store.ProductID())
	}
}

func TestProductOrchestratorRejectsGatewayOnlyConfigWrite(t *testing.T) {
	// Given
	ctx := context.Background()
	store, err := OpenForProduct(ctx, filepath.Join(t.TempDir(), "orchestrator.db"), product.Orchestrator().ID())
	if err != nil {
		t.Fatalf("open Orchestrator store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// When
	err = store.SaveConfig(ctx, ConfigDocument{ResourceType: "wan_link", ResourceID: "gateway-only", Payload: []byte(`{"id":"gateway-only"}`)})

	// Then
	if !errors.Is(err, ErrProductResource) {
		t.Fatalf("save Gateway-only config error = %v, want ErrProductResource", err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM config_documents`).Scan(&count); err != nil {
		t.Fatalf("count config rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("config row count = %d, want zero after rejection", count)
	}
}

func TestProductStoresRejectUnknownAndCrossProductConfigWritesWithoutRows(t *testing.T) {
	tests := []struct {
		name      string
		productID product.ID
		resource  string
	}{
		{name: "Gateway unknown", productID: product.Gateway().ID(), resource: "unknown_resource"},
		{name: "Gateway Orchestrator policy", productID: product.Gateway().ID(), resource: "orchestrator_policy"},
		{name: "Gateway Orchestrator topology", productID: product.Gateway().ID(), resource: "orchestrator_topology"},
		{name: "Gateway Orchestrator service-chain intent", productID: product.Gateway().ID(), resource: "orchestrator_service_chain_intent"},
		{name: "Orchestrator Gateway system mode", productID: product.Orchestrator().ID(), resource: "system_mode"},
		{name: "Orchestrator Gateway rollback", productID: product.Orchestrator().ID(), resource: "dns_rule_update_rollback"},
		{name: "Orchestrator unknown", productID: product.Orchestrator().ID(), resource: "unknown_resource"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := OpenForProduct(ctx, filepath.Join(t.TempDir(), "product.db"), test.productID)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })

			err = store.SaveConfig(ctx, ConfigDocument{ResourceType: test.resource, ResourceID: "forbidden", Payload: []byte(`{"id":"forbidden"}`)})
			if !errors.Is(err, ErrProductResource) {
				t.Fatalf("SaveConfig error = %v, want ErrProductResource", err)
			}
			var count int
			if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM config_documents`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("config row count = %d, want zero after rejection", count)
			}
		})
	}
}

func TestProductOrchestratorDoesNotExposeInjectedGatewayOnlyConfig(t *testing.T) {
	// Given
	ctx := context.Background()
	store, err := OpenForProduct(ctx, filepath.Join(t.TempDir(), "orchestrator.db"), product.Orchestrator().ID())
	if err != nil {
		t.Fatalf("open Orchestrator store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.db.ExecContext(ctx, `INSERT INTO config_documents (resource_type, resource_id, payload_json, payload_hash, updated_at) VALUES ('wan_link', 'injected', '{"id":"injected"}', 'hash', '2026-07-19T00:00:00Z')`); err != nil {
		t.Fatalf("inject Gateway-only config: %v", err)
	}

	// When
	_, err = store.Config(ctx, "wan_link", "injected")

	// Then
	if !errors.Is(err, ErrProductResource) {
		t.Fatalf("read Gateway-only config error = %v, want ErrProductResource", err)
	}
}

func TestProductMetadataRejectsUpdateAndDelete(t *testing.T) {
	// Given
	ctx := context.Background()
	store, err := OpenForProduct(ctx, filepath.Join(t.TempDir(), "immutable-metadata.db"), product.Gateway().ID())
	if err != nil {
		t.Fatalf("open Gateway store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// When
	_, updateErr := store.db.ExecContext(ctx, `UPDATE product_metadata SET product_id = 'orchestrator' WHERE singleton = 1`)
	_, deleteErr := store.db.ExecContext(ctx, `DELETE FROM product_metadata WHERE singleton = 1`)

	// Then
	if updateErr == nil || deleteErr == nil {
		t.Fatalf("metadata update error=%v delete error=%v, want both rejected", updateErr, deleteErr)
	}
	if store.ProductID() != product.Gateway().ID() {
		t.Fatalf("store product = %q, want gateway", store.ProductID())
	}
}

func TestProductMetadataRejectsWrongProductReopenBeforeMigration(t *testing.T) {
	// Given
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "immutable.db")
	gateway, err := OpenForProduct(ctx, path, product.Gateway().ID())
	if err != nil {
		t.Fatalf("open Gateway store: %v", err)
	}
	if err := gateway.SaveConfig(ctx, ConfigDocument{ResourceType: "wan_link", ResourceID: "wan-immutable", Payload: []byte(`{"id":"wan-immutable"}`)}); err != nil {
		gateway.Close()
		t.Fatalf("save Gateway config: %v", err)
	}
	if err := gateway.Close(); err != nil {
		t.Fatalf("close Gateway store: %v", err)
	}
	beforeBody, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read database before wrong-product reopen: %v", err)
	}
	beforeHash := sha256.Sum256(beforeBody)

	// When
	_, err = OpenForProduct(ctx, path, product.Orchestrator().ID())

	// Then
	if !errors.Is(err, ErrProductMismatch) {
		t.Fatalf("wrong-product open error = %v, want ErrProductMismatch", err)
	}
	afterBody, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read database after wrong-product reopen: %v", readErr)
	}
	if afterHash := sha256.Sum256(afterBody); afterHash != beforeHash {
		t.Fatalf("database checksum changed after wrong-product rejection: before=%x after=%x", beforeHash, afterHash)
	}
	reopened, err := OpenForProduct(ctx, path, product.Gateway().ID())
	if err != nil {
		t.Fatalf("reopen Gateway store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if _, err := reopened.Config(ctx, "wan_link", "wan-immutable"); err != nil {
		t.Fatalf("load Gateway resource after rejection: %v", err)
	}
}

func TestMigrationOrchestratorRejectsUntaggedLegacyDatabaseWithoutWrites(t *testing.T) {
	// Given
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db := openLegacyDatabase(t, ctx, path)
	if _, err := db.ExecContext(ctx, `INSERT INTO config_documents (resource_type, resource_id, payload_json, payload_hash, updated_at) VALUES ('wan_link', 'legacy-wan', '{"id":"legacy-wan"}', 'hash', '2026-07-19T00:00:00Z')`); err != nil {
		db.Close()
		t.Fatalf("insert legacy resource: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	// When
	_, err := OpenForProduct(ctx, path, product.Orchestrator().ID())

	// Then
	if !errors.Is(err, ErrUntaggedLegacyDatabase) {
		t.Fatalf("legacy Orchestrator open error = %v, want ErrUntaggedLegacyDatabase", err)
	}
	check, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen raw database: %v", err)
	}
	t.Cleanup(func() { _ = check.Close() })
	var metadataTables, resources int
	if err := check.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='product_metadata'`).Scan(&metadataTables); err != nil {
		t.Fatalf("query metadata table: %v", err)
	}
	if err := check.QueryRowContext(ctx, `SELECT COUNT(*) FROM config_documents WHERE resource_id='legacy-wan'`).Scan(&resources); err != nil {
		t.Fatalf("query legacy resource: %v", err)
	}
	if metadataTables != 0 || resources != 1 {
		t.Fatalf("after rejected claim metadata_tables=%d resources=%d, want 0 and 1", metadataTables, resources)
	}
}

func TestMigrationGatewayClaimsUntaggedLegacyDatabase(t *testing.T) {
	// Given
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-gateway.db")
	db := openLegacyDatabase(t, ctx, path)
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	// When
	store, err := OpenForProduct(ctx, path, product.Gateway().ID())
	if err != nil {
		t.Fatalf("claim legacy Gateway database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Then
	if store.ProductID() != product.Gateway().ID() {
		t.Fatalf("claimed product = %q, want gateway", store.ProductID())
	}
}

func TestProductMetadataRejectsInterruptedMigrationState(t *testing.T) {
	// Given
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "interrupted.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE product_metadata (singleton INTEGER PRIMARY KEY, product_id TEXT NOT NULL, schema_version INTEGER NOT NULL)`); err != nil {
		db.Close()
		t.Fatalf("create interrupted metadata: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close interrupted database: %v", err)
	}

	// When
	_, err = OpenForProduct(ctx, path, product.Gateway().ID())

	// Then
	if !errors.Is(err, ErrInvalidProductMetadata) {
		t.Fatalf("interrupted migration error = %v, want ErrInvalidProductMetadata", err)
	}
	check, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen interrupted database: %v", err)
	}
	t.Cleanup(func() { _ = check.Close() })
	var configTables int
	if err := check.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='config_documents'`).Scan(&configTables); err != nil {
		t.Fatalf("query config table: %v", err)
	}
	if configTables != 0 {
		t.Fatalf("config table count = %d, want zero writes after interrupted metadata", configTables)
	}
}

func TestProductMetadataConcurrentSameProductOpenIsIdempotent(t *testing.T) {
	// Given
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concurrent.db")
	const opens = 8
	stores := make(chan *Store, opens)
	errs := make(chan error, opens)
	var group sync.WaitGroup

	// When
	for range opens {
		group.Add(1)
		go func() {
			defer group.Done()
			store, err := OpenForProduct(ctx, path, product.Gateway().ID())
			if err != nil {
				errs <- err
				return
			}
			stores <- store
		}()
	}
	group.Wait()
	close(stores)
	close(errs)

	// Then
	for err := range errs {
		t.Errorf("concurrent open: %v", err)
	}
	for store := range stores {
		if store.ProductID() != product.Gateway().ID() {
			t.Errorf("concurrent product = %q, want gateway", store.ProductID())
		}
		if err := store.Close(); err != nil {
			t.Errorf("close concurrent store: %v", err)
		}
	}
	if t.Failed() {
		return
	}
	store, err := OpenForProduct(ctx, path, product.Gateway().ID())
	if err != nil {
		t.Fatalf("reopen concurrent database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM product_metadata`).Scan(&count); err != nil {
		t.Fatalf("count metadata rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("metadata row count = %d, want 1", count)
	}
}

func openLegacyDatabase(t *testing.T, ctx context.Context, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		t.Fatalf("initialize legacy database: %v", err)
	}
	return db
}
