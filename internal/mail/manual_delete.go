package mail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type manualDeleteRecord struct {
	messageID   int64
	accountID   int64
	publicID    string
	immutableID string
}

func (s *Service) MoveMessageToDeletedItems(ctx context.Context, publicID string) error {
	record, err := s.loadManualDeleteRecord(ctx, publicID)
	if err != nil {
		return err
	}
	lock := s.accountLock(record.accountID)
	lock.Lock()
	defer lock.Unlock()
	record, err = s.loadManualDeleteRecord(ctx, publicID)
	if err != nil {
		return err
	}

	deletedItems, err := s.graph.getMailFolder(ctx, record.accountID, "deleteditems")
	if err != nil {
		return err
	}
	movedID, err := s.graph.moveMessage(ctx, record.accountID, record.immutableID, deletedItems.ID)
	if err != nil {
		return err
	}

	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin manual message deletion: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE messages SET immutable_id = ?, body_text = NULL, body_cached_at_utc = NULL,
			hidden_from_inbox = 1, updated_at_utc = ?
		WHERE id = ? AND hidden_from_inbox = 0 AND remote_deleted = 0
	`, movedID, formatTime(now), record.messageID)
	if err != nil {
		return fmt.Errorf("record manual message deletion: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read manual message deletion result: %w", err)
	}
	if updated != 1 {
		return ErrMessageNotFound
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE cleanup_actions SET state = 'deleted', completed_at_utc = ?, last_error = NULL,
			next_retry_at_utc = NULL, updated_at_utc = ?
		WHERE message_id = ? AND state IN ('candidate', 'dismissed', 'restored', 'failed')
	`, formatTime(now), formatTime(now), record.messageID); err != nil {
		return fmt.Errorf("finish cleanup candidate after manual deletion: %w", err)
	}
	if err := s.recordAuditTx(ctx, tx, "message.moved_to_deleted_items", "admin", "message", record.publicID,
		map[string]any{"destination": "deleteditems", "manual": true}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit manual message deletion: %w", err)
	}
	return nil
}

func (s *Service) loadManualDeleteRecord(ctx context.Context, publicID string) (manualDeleteRecord, error) {
	var record manualDeleteRecord
	err := s.db.QueryRowContext(ctx, `
		SELECT id, account_id, public_id, immutable_id FROM messages
		WHERE public_id = ? AND remote_deleted = 0 AND hidden_from_inbox = 0
	`, strings.TrimSpace(publicID)).Scan(&record.messageID, &record.accountID, &record.publicID, &record.immutableID)
	if errors.Is(err, sql.ErrNoRows) {
		return manualDeleteRecord{}, ErrMessageNotFound
	}
	if err != nil {
		return manualDeleteRecord{}, fmt.Errorf("load message for manual deletion: %w", err)
	}
	return record, nil
}
