package persistence

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrationLegacyGatewayDatabasePreservesConfig(t *testing.T) {
	// Given
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-gateway.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		t.Fatalf("initialize legacy schema: %v", err)
	}
	updatedAt := time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC)
	if _, err := db.ExecContext(ctx, `
INSERT INTO config_documents (resource_type, resource_id, payload_json, payload_hash, updated_at)
VALUES ('wan_link', 'legacy-wan', '{"id":"legacy-wan","enabled":true}', 'legacy-hash', ?)`, encodeTime(updatedAt)); err != nil {
		db.Close()
		t.Fatalf("insert legacy config: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	// When
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open legacy Gateway database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Then
	document, err := store.Config(ctx, "wan_link", "legacy-wan")
	if err != nil {
		t.Fatalf("load migrated config: %v", err)
	}
	if string(document.Payload) != `{"id":"legacy-wan","enabled":true}` || document.PayloadHash != "legacy-hash" || !document.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("migrated document = %#v, want unchanged legacy resource", document)
	}
}
