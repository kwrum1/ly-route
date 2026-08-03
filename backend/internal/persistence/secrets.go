package persistence

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func (s *Store) initializeSecretKey(dsn string) error {
	if encoded := strings.TrimSpace(os.Getenv("LY_ROUTE_SECRET_KEY")); encoded != "" {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(decoded) != len(s.secretKey) {
			return fmt.Errorf("LY_ROUTE_SECRET_KEY must be base64-encoded 32 bytes")
		}
		copy(s.secretKey[:], decoded)
		return nil
	}
	if dsn == ":memory:" || strings.Contains(dsn, "mode=memory") {
		_, err := io.ReadFull(rand.Reader, s.secretKey[:])
		return err
	}
	databasePath := strings.TrimPrefix(dsn, "file:")
	if index := strings.IndexByte(databasePath, '?'); index >= 0 {
		databasePath = databasePath[:index]
	}
	if strings.TrimSpace(databasePath) == "" {
		return fmt.Errorf("file database path is required for persistent secret key")
	}
	keyPath := strings.TrimSpace(os.Getenv("LY_ROUTE_SECRET_KEY_FILE"))
	if keyPath == "" {
		keyPath = databasePath + ".key"
	}
	if content, err := readSecretKeyFile(keyPath, len(s.secretKey)); err == nil {
		copy(s.secretKey[:], content)
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil && filepath.Dir(keyPath) != "." {
		return err
	}
	generated := make([]byte, len(s.secretKey))
	if _, err := io.ReadFull(rand.Reader, generated); err != nil {
		return err
	}
	directory := filepath.Dir(keyPath)
	file, err := os.CreateTemp(directory, ".ly-route-secret-*")
	if err != nil {
		return err
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(generated); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, keyPath); err != nil {
		if !os.IsExist(err) {
			return fmt.Errorf("atomically install secret key: %w", err)
		}
		content, readErr := readSecretKeyFile(keyPath, len(s.secretKey))
		if readErr != nil {
			return fmt.Errorf("read concurrently-created secret key: %w", readErr)
		}
		copy(s.secretKey[:], content)
		return nil
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open secret key directory: %w", err)
	}
	if syncErr := directoryHandle.Sync(); syncErr != nil {
		_ = directoryHandle.Close()
		return fmt.Errorf("sync secret key directory: %w", syncErr)
	}
	if closeErr := directoryHandle.Close(); closeErr != nil {
		return closeErr
	}
	copy(s.secretKey[:], generated)
	return nil
}

func readSecretKeyFile(path string, expectedLength int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("secret key file %q must be a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("secret key file %q permissions %04o allow group or other access", path, info.Mode().Perm())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(content) != expectedLength {
		return nil, fmt.Errorf("secret key file %q must contain exactly %d bytes", path, expectedLength)
	}
	return content, nil
}

func (s *Store) Secret(ctx context.Context, resourceType, resourceID, field string) (string, error) {
	var nonce, ciphertext []byte
	err := s.db.QueryRowContext(ctx, `SELECT nonce, ciphertext FROM encrypted_secrets WHERE resource_type = ? AND resource_id = ? AND field_name = ?`, resourceType, resourceID, field).Scan(&nonce, &ciphertext)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	block, err := aes.NewCipher(s.secretKey[:])
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, s.secretAAD(resourceType, resourceID, field))
	if err != nil {
		return "", fmt.Errorf("decrypt secret %s/%s/%s: %w", resourceType, resourceID, field, err)
	}
	return string(plaintext), nil
}

func (s *Store) encryptSecret(resourceType, resourceID, field, value string) ([]byte, []byte, error) {
	block, err := aes.NewCipher(s.secretKey[:])
	if err != nil {
		return nil, nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	return nonce, aead.Seal(nil, nonce, []byte(value), s.secretAAD(resourceType, resourceID, field)), nil
}

func (s *Store) secretAAD(resourceType, resourceID, field string) []byte {
	return []byte(s.productID.String() + "\x00" + resourceType + "\x00" + resourceID + "\x00" + field)
}
