package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"ly-route/backend/internal/product"
)

const SchemaVersion = 1

const productMetadataSchema = `
CREATE TABLE product_metadata (
  singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
  product_id TEXT NOT NULL CHECK (product_id IN ('gateway', 'orchestrator')),
  schema_version INTEGER NOT NULL CHECK (schema_version > 0)
);`

const productMetadataProtection = `
CREATE TRIGGER IF NOT EXISTS product_metadata_immutable_update
BEFORE UPDATE ON product_metadata
BEGIN
  SELECT RAISE(ABORT, 'product metadata is immutable');
END;
CREATE TRIGGER IF NOT EXISTS product_metadata_immutable_delete
BEFORE DELETE ON product_metadata
BEGIN
  SELECT RAISE(ABORT, 'product metadata is immutable');
END;`

var (
	ErrProductMismatch        = errors.New("persistence product mismatch")
	ErrProductResource        = errors.New("persistence resource is not available for product")
	ErrUntaggedLegacyDatabase = errors.New("untagged legacy database")
	ErrInvalidProductMetadata = errors.New("invalid product metadata")
)

type ProductMismatchError struct {
	Expected product.ID
	Actual   product.ID
}

func (err *ProductMismatchError) Error() string {
	return fmt.Sprintf("%s: database is %s, process is %s", ErrProductMismatch, err.Actual, err.Expected)
}

func (err *ProductMismatchError) Is(target error) bool {
	return target == ErrProductMismatch
}

type ProductResourceError struct {
	Product  product.ID
	Resource string
}

func (err *ProductResourceError) Error() string {
	return fmt.Sprintf("%s: %s cannot access %q", ErrProductResource, err.Product, err.Resource)
}

func (err *ProductResourceError) Is(target error) bool {
	return target == ErrProductResource
}

type Store struct {
	db        *sql.DB
	productID product.ID
	secretKey [32]byte
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	return OpenForProduct(ctx, dsn, product.Gateway().ID())
}

func OpenForProduct(ctx context.Context, dsn string, productID product.ID) (*Store, error) {
	profile, err := product.ParseProfile(productID.String())
	if err != nil {
		return nil, fmt.Errorf("open persistence: %w", err)
	}
	productID = profile.ID()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, productID: productID}
	if err := store.initializeSecretKey(dsn); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize secret key: %w", err)
	}
	if err := store.initialize(ctx); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			return nil, errors.Join(err, fmt.Errorf("close sqlite: %w", closeErr))
		}
		return nil, err
	}
	return store, nil
}

func (s *Store) ProductID() product.ID {
	return s.productID
}

func (s *Store) validateConfigResource(resourceType string) error {
	profile, err := product.ParseProfile(s.productID.String())
	if err != nil {
		return fmt.Errorf("validate persistence product: %w", err)
	}
	if !profile.AllowsConfigResource(resourceType) {
		return &ProductResourceError{Product: s.productID, Resource: resourceType}
	}
	return nil
}

func (s *Store) validatePolicyNamespace(namespace string) error {
	if s.productID == product.Orchestrator().ID() && (namespace == "dns-policy" || namespace == "dns-render") {
		return &ProductResourceError{Product: s.productID, Resource: namespace}
	}
	return nil
}

func (s *Store) initialize(ctx context.Context) (err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer func() {
		err = errors.Join(err, conn.Close())
	}()
	if _, err := conn.ExecContext(ctx, `PRAGMA busy_timeout = 5000; PRAGMA foreign_keys = ON;`); err != nil {
		return fmt.Errorf("configure sqlite migration: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("begin persistence migration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, rollbackErr := conn.ExecContext(context.WithoutCancel(ctx), `ROLLBACK`)
			err = errors.Join(err, rollbackErr)
		}
	}()

	metadataExists, err := sqliteTableExists(ctx, conn, "product_metadata")
	if err != nil {
		return err
	}
	if metadataExists {
		if err := s.verifyProductMetadata(ctx, conn); err != nil {
			return err
		}
	} else {
		hasLegacyState, err := hasUserTables(ctx, conn)
		if err != nil {
			return err
		}
		if hasLegacyState && s.productID == product.Orchestrator().ID() {
			return fmt.Errorf("%w: orchestrator cannot claim existing state", ErrUntaggedLegacyDatabase)
		}
		if _, err := conn.ExecContext(ctx, productMetadataSchema); err != nil {
			return fmt.Errorf("create product metadata: %w", err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO product_metadata (singleton, product_id, schema_version) VALUES (1, ?, ?)`, s.productID.String(), SchemaVersion); err != nil {
			return fmt.Errorf("initialize product metadata: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, productMetadataProtection); err != nil {
		return fmt.Errorf("protect product metadata: %w", err)
	}
	if _, err := conn.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize persistence schema: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit persistence migration: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) verifyProductMetadata(ctx context.Context, conn *sql.Conn) error {
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM product_metadata`).Scan(&count); err != nil {
		return fmt.Errorf("count product metadata: %w", err)
	}
	if count != 1 {
		return fmt.Errorf("%w: expected exactly one row, found %d", ErrInvalidProductMetadata, count)
	}
	var singleton, version int
	var rawProduct string
	if err := conn.QueryRowContext(ctx, `SELECT singleton, product_id, schema_version FROM product_metadata`).Scan(&singleton, &rawProduct, &version); err != nil {
		return fmt.Errorf("read product metadata: %w", err)
	}
	profile, err := product.ParseProfile(rawProduct)
	if err != nil || singleton != 1 || version != SchemaVersion {
		return fmt.Errorf("%w: singleton=%d product=%q schema_version=%d", ErrInvalidProductMetadata, singleton, rawProduct, version)
	}
	actual := profile.ID()
	if actual != s.productID {
		return &ProductMismatchError{Expected: s.productID, Actual: actual}
	}
	return nil
}

func sqliteTableExists(ctx context.Context, conn *sql.Conn, name string) (bool, error) {
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect sqlite table %s: %w", name, err)
	}
	return count == 1, nil
}

func hasUserTables(ctx context.Context, conn *sql.Conn) (bool, error) {
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect legacy sqlite state: %w", err)
	}
	return count > 0, nil
}
