package persistence

import (
	"context"
	"errors"
	"fmt"
)

var ErrWriteConflict = errors.New("persistence write conflict")

func (s *Store) ReplaceConfigIfHash(ctx context.Context, document ConfigDocument, expectedHash string) error {
	if err := s.validateConfigResource(document.ResourceType); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
UPDATE config_documents
SET payload_json = ?, payload_hash = ?, updated_at = ?
WHERE resource_type = ? AND resource_id = ? AND payload_hash = ?`,
		string(document.Payload), hash(document.Payload, document.PayloadHash), encodeTime(document.UpdatedAt),
		document.ResourceType, document.ResourceID, expectedHash)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("%w: config %s/%s changed", ErrWriteConflict, document.ResourceType, document.ResourceID)
	}
	return tx.Commit()
}
