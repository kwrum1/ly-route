package persistence

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEncryptedSecretPersistsAcrossReopenWithoutPlaintext(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	document := ConfigDocument{ResourceType: "proxy_node", ResourceID: "node-a", Payload: []byte(`{"id":"node-a","secret_redacted":"redacted"}`), UpdatedAt: time.Now().UTC()}
	if err := store.SaveConfigWithSecrets(ctx, document, map[string]string{"secret": "private-credential"}); err != nil {
		t.Fatal(err)
	}
	var ciphertext []byte
	if err := store.db.QueryRowContext(ctx, `SELECT ciphertext FROM encrypted_secrets WHERE resource_type = 'proxy_node' AND resource_id = 'node-a' AND field_name = 'secret'`).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("private-credential")) {
		t.Fatal("ciphertext contains plaintext credential")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	keyInfo, err := os.Stat(databasePath + ".key")
	if err != nil {
		t.Fatal(err)
	}
	if keyInfo.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %o, want 600", keyInfo.Mode().Perm())
	}
	reopened, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	value, err := reopened.Secret(ctx, "proxy_node", "node-a", "secret")
	if err != nil || value != "private-credential" {
		t.Fatalf("reopened secret = %q, err=%v", value, err)
	}
}

func TestOpenRejectsOverpermissiveSecretKeyFile(t *testing.T) {
	t.Setenv("LY_ROUTE_SECRET_KEY", "")
	t.Setenv("LY_ROUTE_SECRET_KEY_FILE", "")
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	if err := os.WriteFile(databasePath+".key", make([]byte, 32), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(context.Background(), databasePath)
	if err == nil || !strings.Contains(err.Error(), "allow group or other access") {
		t.Fatalf("Open error = %v, want insecure key permissions rejection", err)
	}
}

func TestOpenRejectsSymlinkSecretKeyFile(t *testing.T) {
	t.Setenv("LY_ROUTE_SECRET_KEY", "")
	t.Setenv("LY_ROUTE_SECRET_KEY_FILE", "")
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "gateway.db")
	targetPath := filepath.Join(directory, "target.key")
	if err := os.WriteFile(targetPath, make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetPath, databasePath+".key"); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := Open(context.Background(), databasePath)
	if err == nil || !strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("Open error = %v, want symlink rejection", err)
	}
}

func TestDeleteConfigDeletesAssociatedSecrets(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, "file:delete-secret?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	document := ConfigDocument{ResourceType: "proxy_subscription", ResourceID: "sub-a", Payload: []byte(`{"id":"sub-a"}`), UpdatedAt: time.Now().UTC()}
	if err := store.SaveConfigWithSecrets(ctx, document, map[string]string{"url": "https://secret.example/sub"}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteConfig(ctx, "proxy_subscription", "sub-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Secret(ctx, "proxy_subscription", "sub-a", "url"); err != ErrNotFound {
		t.Fatalf("Secret error = %v, want ErrNotFound", err)
	}
}
