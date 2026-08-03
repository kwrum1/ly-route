package persistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS config_documents (
  resource_type TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
  payload_hash TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (resource_type, resource_id)
);

CREATE TABLE IF NOT EXISTS policy_documents (
  namespace TEXT NOT NULL,
  policy_id TEXT NOT NULL,
  priority INTEGER NOT NULL CHECK (priority >= 0),
  enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
  payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
  payload_hash TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (namespace, policy_id)
);

CREATE INDEX IF NOT EXISTS policy_documents_namespace_priority_idx ON policy_documents(namespace, priority);

CREATE TABLE IF NOT EXISTS runtime_snapshots (
  id TEXT PRIMARY KEY,
  source_transaction_id TEXT NOT NULL,
  payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
  payload_hash TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS rollback_metadata (
  id TEXT PRIMARY KEY,
  target_snapshot_id TEXT NOT NULL REFERENCES runtime_snapshots(id),
  reason TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'failed')),
  requested_at TEXT NOT NULL,
  completed_at TEXT,
  error TEXT
);

CREATE TABLE IF NOT EXISTS audit_events (
  id TEXT PRIMARY KEY,
  timestamp TEXT NOT NULL,
  actor TEXT NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('admin', 'readonly', 'system')),
  resource TEXT NOT NULL,
  action TEXT NOT NULL,
  before_hash TEXT,
  after_hash TEXT,
  status TEXT NOT NULL CHECK (status IN ('success', 'failure', 'rollback', 'denied')),
  error TEXT,
  transaction_id TEXT
);

CREATE INDEX IF NOT EXISTS audit_events_timestamp_idx ON audit_events(timestamp);
CREATE INDEX IF NOT EXISTS audit_events_transaction_idx ON audit_events(transaction_id);

CREATE TABLE IF NOT EXISTS auth_users (
  username TEXT PRIMARY KEY,
  role TEXT NOT NULL CHECK (role IN ('admin', 'readonly')),
  password_hash TEXT NOT NULL,
  password_change_required INTEGER NOT NULL CHECK (password_change_required IN (0, 1)),
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS encrypted_secrets (
  resource_type TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  field_name TEXT NOT NULL,
  nonce BLOB NOT NULL,
  ciphertext BLOB NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (resource_type, resource_id, field_name)
);
`

var ErrNotFound = errors.New("persistence record not found")

type ConfigDocument struct {
	ResourceType string
	ResourceID   string
	Payload      json.RawMessage
	PayloadHash  string
	UpdatedAt    time.Time
}

type ConfigWithSecrets struct {
	Document ConfigDocument
	Secrets  map[string]string
}

type ConfigKey struct {
	ResourceType string
	ResourceID   string
}

type PolicyDocument struct {
	Namespace   string
	PolicyID    string
	Priority    int
	Enabled     bool
	Payload     json.RawMessage
	PayloadHash string
	UpdatedAt   time.Time
}

type RuntimeSnapshot struct {
	ID                  string
	SourceTransactionID string
	Payload             json.RawMessage
	PayloadHash         string
	CreatedAt           time.Time
}

type RollbackMetadata struct {
	ID               string
	TargetSnapshotID string
	Reason           string
	Status           string
	RequestedAt      time.Time
	CompletedAt      *time.Time
	Error            string
}

type AuditEvent struct {
	ID            string
	Timestamp     time.Time
	Actor         string
	Role          string
	Resource      string
	Action        string
	BeforeHash    string
	AfterHash     string
	Status        string
	Error         string
	TransactionID string
}

type AuthUser struct {
	Username               string
	Role                   string
	PasswordHash           string
	PasswordChangeRequired bool
	UpdatedAt              time.Time
	Stored                 bool
}

type ApplyRecord struct {
	ConfigDocuments []ConfigDocument
	Policies        []PolicyDocument
	Snapshot        RuntimeSnapshot
	Rollback        RollbackMetadata
	AuditEvents     []AuditEvent
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Init(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

func (s *Store) SaveApply(ctx context.Context, record ApplyRecord) error {
	for _, document := range record.ConfigDocuments {
		if err := s.validateConfigResource(document.ResourceType); err != nil {
			return err
		}
	}
	for _, document := range record.Policies {
		if err := s.validatePolicyNamespace(document.Namespace); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, document := range record.ConfigDocuments {
		if err := upsertConfig(ctx, tx, document); err != nil {
			return err
		}
	}
	for _, document := range record.Policies {
		if err := upsertPolicy(ctx, tx, document); err != nil {
			return err
		}
	}
	if len(record.Snapshot.Payload) > 0 || record.Snapshot.ID != "" {
		if err := insertSnapshot(ctx, tx, record.Snapshot); err != nil {
			return err
		}
	}
	if record.Rollback.ID != "" {
		if err := insertRollback(ctx, tx, record.Rollback); err != nil {
			return err
		}
	}
	for _, event := range record.AuditEvents {
		if err := insertAuditEvent(ctx, tx, event); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) SaveAuditEvents(ctx context.Context, events []AuditEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, event := range events {
		if err := insertAuditEvent(ctx, tx, event); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) SavePolicy(ctx context.Context, document PolicyDocument) error {
	if err := s.validatePolicyNamespace(document.Namespace); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := upsertPolicy(ctx, tx, document); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveConfig(ctx context.Context, document ConfigDocument) error {
	if err := s.validateConfigResource(document.ResourceType); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := upsertConfig(ctx, tx, document); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveConfigWithSecrets(ctx context.Context, document ConfigDocument, secrets map[string]string) error {
	return s.SaveConfigsWithSecrets(ctx, []ConfigWithSecrets{{Document: document, Secrets: secrets}})
}

func (s *Store) SaveConfigsWithSecrets(ctx context.Context, writes []ConfigWithSecrets) error {
	return s.SaveConfigsWithSecretsAndDelete(ctx, writes, nil)
}

func (s *Store) SaveConfigsWithSecretsAndDelete(ctx context.Context, writes []ConfigWithSecrets, deletes []ConfigKey) error {
	for _, write := range writes {
		if err := s.validateConfigResource(write.Document.ResourceType); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, key := range deletes {
		if err := s.validateConfigResource(key.ResourceType); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM encrypted_secrets WHERE resource_type = ? AND resource_id = ?`, key.ResourceType, key.ResourceID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM config_documents WHERE resource_type = ? AND resource_id = ?`, key.ResourceType, key.ResourceID); err != nil {
			return err
		}
	}
	for _, write := range writes {
		document := write.Document
		if err := upsertConfig(ctx, tx, document); err != nil {
			return err
		}
		for field, value := range write.Secrets {
			nonce, ciphertext, err := s.encryptSecret(document.ResourceType, document.ResourceID, field, value)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO encrypted_secrets (resource_type, resource_id, field_name, nonce, ciphertext, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(resource_type, resource_id, field_name) DO UPDATE SET
  nonce = excluded.nonce,
  ciphertext = excluded.ciphertext,
  updated_at = excluded.updated_at`, document.ResourceType, document.ResourceID, field, nonce, ciphertext, encodeTime(document.UpdatedAt)); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) SaveConfigs(ctx context.Context, documents []ConfigDocument) error {
	for _, document := range documents {
		if err := s.validateConfigResource(document.ResourceType); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, document := range documents {
		if err := upsertConfig(ctx, tx, document); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ReplaceConfigsForTypes(ctx context.Context, resourceTypes []string, documents []ConfigDocument) error {
	for _, resourceType := range resourceTypes {
		if err := s.validateConfigResource(resourceType); err != nil {
			return err
		}
	}
	for _, document := range documents {
		if err := s.validateConfigResource(document.ResourceType); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seen := map[string]struct{}{}
	for _, resourceType := range resourceTypes {
		if resourceType == "" {
			continue
		}
		if _, ok := seen[resourceType]; ok {
			continue
		}
		seen[resourceType] = struct{}{}
		if _, err := tx.ExecContext(ctx, `DELETE FROM config_documents WHERE resource_type = ?`, resourceType); err != nil {
			return err
		}
	}
	for _, document := range documents {
		if err := upsertConfig(ctx, tx, document); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SaveRuntimeSnapshot(ctx context.Context, snapshot RuntimeSnapshot) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertSnapshot(ctx, tx, snapshot); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteConfig(ctx context.Context, resourceType, resourceID string) error {
	if err := s.validateConfigResource(resourceType); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM encrypted_secrets WHERE resource_type = ? AND resource_id = ?`, resourceType, resourceID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM config_documents WHERE resource_type = ? AND resource_id = ?`, resourceType, resourceID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) DeletePolicy(ctx context.Context, namespace, policyID string) error {
	if err := s.validatePolicyNamespace(namespace); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM policy_documents WHERE namespace = ? AND policy_id = ?`, namespace, policyID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) Config(ctx context.Context, resourceType, resourceID string) (ConfigDocument, error) {
	if err := s.validateConfigResource(resourceType); err != nil {
		return ConfigDocument{}, err
	}
	row := s.db.QueryRowContext(ctx, `
SELECT resource_type, resource_id, payload_json, payload_hash, updated_at
FROM config_documents WHERE resource_type = ? AND resource_id = ?`, resourceType, resourceID)
	return scanConfig(row)
}

func (s *Store) Configs(ctx context.Context, resourceType string) ([]ConfigDocument, error) {
	if err := s.validateConfigResource(resourceType); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT resource_type, resource_id, payload_json, payload_hash, updated_at
FROM config_documents WHERE resource_type = ? ORDER BY resource_id`, resourceType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var documents []ConfigDocument
	for rows.Next() {
		document, err := scanConfig(rows)
		if err != nil {
			return nil, err
		}
		documents = append(documents, document)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return documents, nil
}

func (s *Store) Policy(ctx context.Context, namespace, policyID string) (PolicyDocument, error) {
	if err := s.validatePolicyNamespace(namespace); err != nil {
		return PolicyDocument{}, err
	}
	row := s.db.QueryRowContext(ctx, `
SELECT namespace, policy_id, priority, enabled, payload_json, payload_hash, updated_at
FROM policy_documents WHERE namespace = ? AND policy_id = ?`, namespace, policyID)
	return scanPolicy(row)
}

func (s *Store) Policies(ctx context.Context, namespace string) ([]PolicyDocument, error) {
	if err := s.validatePolicyNamespace(namespace); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT namespace, policy_id, priority, enabled, payload_json, payload_hash, updated_at
FROM policy_documents WHERE namespace = ? ORDER BY priority, policy_id`, namespace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var documents []PolicyDocument
	for rows.Next() {
		document, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		documents = append(documents, document)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return documents, nil
}

func (s *Store) RuntimeSnapshot(ctx context.Context, id string) (RuntimeSnapshot, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, source_transaction_id, payload_json, payload_hash, created_at
FROM runtime_snapshots WHERE id = ?`, id)
	return scanSnapshot(row)
}

func (s *Store) AuthUser(ctx context.Context, username string) (AuthUser, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT username, role, password_hash, password_change_required, updated_at
FROM auth_users WHERE username = ?`, username)
	return scanAuthUser(row)
}

func (s *Store) AuthUsers(ctx context.Context) ([]AuthUser, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT username, role, password_hash, password_change_required, updated_at
FROM auth_users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []AuthUser
	for rows.Next() {
		user, err := scanAuthUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return users, nil
}

func (s *Store) SaveAuthUser(ctx context.Context, user AuthUser) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO auth_users (username, role, password_hash, password_change_required, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(username) DO UPDATE SET
  role = excluded.role,
  password_hash = excluded.password_hash,
  password_change_required = excluded.password_change_required,
  updated_at = excluded.updated_at`, user.Username, user.Role, user.PasswordHash, boolInt(user.PasswordChangeRequired), encodeTime(user.UpdatedAt))
	return err
}

func (s *Store) DeleteAuthUser(ctx context.Context, username string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM auth_users WHERE username = ?`, username)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RuntimeSnapshots(ctx context.Context) ([]RuntimeSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, source_transaction_id, payload_json, payload_hash, created_at
FROM runtime_snapshots ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snapshots []RuntimeSnapshot
	for rows.Next() {
		snapshot, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return snapshots, nil
}

func (s *Store) Rollback(ctx context.Context, id string) (RollbackMetadata, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, target_snapshot_id, reason, status, requested_at, completed_at, error
FROM rollback_metadata WHERE id = ?`, id)
	return scanRollback(row)
}

func (s *Store) AuditEvents(ctx context.Context, transactionID string) ([]AuditEvent, error) {
	query := `
SELECT id, timestamp, actor, role, resource, action, before_hash, after_hash, status, error, transaction_id
	FROM audit_events ORDER BY timestamp, id`
	args := []any{}
	if transactionID != "" {
		query = `
SELECT id, timestamp, actor, role, resource, action, before_hash, after_hash, status, error, transaction_id
FROM audit_events WHERE transaction_id = ? ORDER BY timestamp, id`
		args = append(args, transactionID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []AuditEvent
	for rows.Next() {
		event, err := scanAuditEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func MarshalPayload(value any) (json.RawMessage, string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(payload)
	return json.RawMessage(payload), hex.EncodeToString(digest[:]), nil
}

func upsertConfig(ctx context.Context, tx *sql.Tx, document ConfigDocument) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO config_documents (resource_type, resource_id, payload_json, payload_hash, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(resource_type, resource_id) DO UPDATE SET
  payload_json = excluded.payload_json,
  payload_hash = excluded.payload_hash,
  updated_at = excluded.updated_at`, document.ResourceType, document.ResourceID, string(document.Payload), hash(document.Payload, document.PayloadHash), encodeTime(document.UpdatedAt))
	return err
}

func upsertPolicy(ctx context.Context, tx *sql.Tx, document PolicyDocument) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO policy_documents (namespace, policy_id, priority, enabled, payload_json, payload_hash, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(namespace, policy_id) DO UPDATE SET
  priority = excluded.priority,
  enabled = excluded.enabled,
  payload_json = excluded.payload_json,
  payload_hash = excluded.payload_hash,
  updated_at = excluded.updated_at`, document.Namespace, document.PolicyID, document.Priority, boolInt(document.Enabled), string(document.Payload), hash(document.Payload, document.PayloadHash), encodeTime(document.UpdatedAt))
	return err
}

func insertSnapshot(ctx context.Context, tx *sql.Tx, snapshot RuntimeSnapshot) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO runtime_snapshots (id, source_transaction_id, payload_json, payload_hash, created_at)
VALUES (?, ?, ?, ?, ?)`, snapshot.ID, snapshot.SourceTransactionID, string(snapshot.Payload), hash(snapshot.Payload, snapshot.PayloadHash), encodeTime(snapshot.CreatedAt))
	return err
}

func insertRollback(ctx context.Context, tx *sql.Tx, rollback RollbackMetadata) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO rollback_metadata (id, target_snapshot_id, reason, status, requested_at, completed_at, error)
VALUES (?, ?, ?, ?, ?, ?, ?)`, rollback.ID, rollback.TargetSnapshotID, rollback.Reason, rollback.Status, encodeTime(rollback.RequestedAt), nullableTime(rollback.CompletedAt), nullableString(rollback.Error))
	return err
}

func insertAuditEvent(ctx context.Context, tx *sql.Tx, event AuditEvent) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO audit_events (id, timestamp, actor, role, resource, action, before_hash, after_hash, status, error, transaction_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, event.ID, encodeTime(event.Timestamp), event.Actor, event.Role, event.Resource, event.Action, nullableString(event.BeforeHash), nullableString(event.AfterHash), event.Status, nullableString(event.Error), nullableString(event.TransactionID))
	return err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanConfig(row scanner) (ConfigDocument, error) {
	var document ConfigDocument
	var payload string
	var updatedAt string
	if err := row.Scan(&document.ResourceType, &document.ResourceID, &payload, &document.PayloadHash, &updatedAt); err != nil {
		return document, convertScanError(err)
	}
	document.Payload = json.RawMessage(payload)
	document.UpdatedAt = decodeTime(updatedAt)
	return document, nil
}

func scanPolicy(row scanner) (PolicyDocument, error) {
	var document PolicyDocument
	var payload string
	var enabled int
	var updatedAt string
	if err := row.Scan(&document.Namespace, &document.PolicyID, &document.Priority, &enabled, &payload, &document.PayloadHash, &updatedAt); err != nil {
		return document, convertScanError(err)
	}
	document.Enabled = enabled == 1
	document.Payload = json.RawMessage(payload)
	document.UpdatedAt = decodeTime(updatedAt)
	return document, nil
}

func scanAuthUser(row scanner) (AuthUser, error) {
	var user AuthUser
	var updatedAt string
	var changeRequired int
	if err := row.Scan(&user.Username, &user.Role, &user.PasswordHash, &changeRequired, &updatedAt); err != nil {
		return user, convertScanError(err)
	}
	user.PasswordChangeRequired = changeRequired == 1
	user.UpdatedAt = decodeTime(updatedAt)
	user.Stored = true
	return user, nil
}

func scanSnapshot(row scanner) (RuntimeSnapshot, error) {
	var snapshot RuntimeSnapshot
	var payload string
	var createdAt string
	if err := row.Scan(&snapshot.ID, &snapshot.SourceTransactionID, &payload, &snapshot.PayloadHash, &createdAt); err != nil {
		return snapshot, convertScanError(err)
	}
	snapshot.Payload = json.RawMessage(payload)
	snapshot.CreatedAt = decodeTime(createdAt)
	return snapshot, nil
}

func scanRollback(row scanner) (RollbackMetadata, error) {
	var rollback RollbackMetadata
	var completedAt sql.NullString
	var errText sql.NullString
	var requestedAt string
	if err := row.Scan(&rollback.ID, &rollback.TargetSnapshotID, &rollback.Reason, &rollback.Status, &requestedAt, &completedAt, &errText); err != nil {
		return rollback, convertScanError(err)
	}
	rollback.RequestedAt = decodeTime(requestedAt)
	if completedAt.Valid {
		decoded := decodeTime(completedAt.String)
		rollback.CompletedAt = &decoded
	}
	if errText.Valid {
		rollback.Error = errText.String
	}
	return rollback, nil
}

func scanAuditEvent(row scanner) (AuditEvent, error) {
	var event AuditEvent
	var timestamp string
	var beforeHash sql.NullString
	var afterHash sql.NullString
	var errText sql.NullString
	var transactionID sql.NullString
	if err := row.Scan(&event.ID, &timestamp, &event.Actor, &event.Role, &event.Resource, &event.Action, &beforeHash, &afterHash, &event.Status, &errText, &transactionID); err != nil {
		return event, convertScanError(err)
	}
	event.Timestamp = decodeTime(timestamp)
	if beforeHash.Valid {
		event.BeforeHash = beforeHash.String
	}
	if afterHash.Valid {
		event.AfterHash = afterHash.String
	}
	if errText.Valid {
		event.Error = errText.String
	}
	if transactionID.Valid {
		event.TransactionID = transactionID.String
	}
	return event, nil
}

func hash(payload json.RawMessage, current string) string {
	if current != "" {
		return current
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func encodeTime(value time.Time) string {
	if value.IsZero() {
		value = time.Now().UTC()
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func decodeTime(value string) time.Time {
	decoded, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		panic(fmt.Sprintf("invalid persisted timestamp %q: %v", value, err))
	}
	return decoded
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	encoded := encodeTime(*value)
	return encoded
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func convertScanError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
