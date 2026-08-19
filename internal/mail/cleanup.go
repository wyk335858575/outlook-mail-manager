package mail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"outlook-mail-manager/internal/accounts"
)

const (
	cleanupFolderName  = "Outlook Manager 待清理"
	cleanupGracePeriod = 14 * 24 * time.Hour
)

type CleanupItem struct {
	PublicID        string     `json:"public_id"`
	State           string     `json:"state"`
	CandidateReason string     `json:"candidate_reason"`
	MessagePublicID string     `json:"message_public_id"`
	AccountPublicID string     `json:"account_public_id"`
	AccountName     string     `json:"account_name"`
	Subject         string     `json:"subject"`
	SenderName      string     `json:"sender_name"`
	SenderAddress   string     `json:"sender_address"`
	Category        Category   `json:"category"`
	ReceivedAt      time.Time  `json:"received_at"`
	ApprovedAt      *time.Time `json:"approved_at,omitempty"`
	ExecuteAfter    *time.Time `json:"execute_after,omitempty"`
	MovedAt         *time.Time `json:"moved_at,omitempty"`
	RestoredAt      *time.Time `json:"restored_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	AttemptCount    int        `json:"attempt_count"`
}

type cleanupRecord struct {
	actionID       int64
	actionPublicID string
	state          string
	messageID      int64
	messagePublic  string
	accountID      int64
	accountPublic  string
	immutableID    string
	originalFolder string
	holdingFolder  string
	category       Category
	flagged        bool
	accountLocked  bool
	messageLocked  bool
	protection     sql.NullString
}

func (s *Service) ListCleanupActions(ctx context.Context, state string, limit int) ([]CleanupItem, error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	conditions := "1 = 1"
	args := make([]any, 0, 2)
	if state = strings.TrimSpace(state); state != "" && state != "all" {
		if !validCleanupState(state) {
			return nil, ErrInvalidCleanupState
		}
		conditions = "c.state = ?"
		args = append(args, state)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.public_id, c.state, c.candidate_reason, m.public_id, a.public_id,
			COALESCE(NULLIF(a.display_name, ''), NULLIF(a.primary_email, ''), a.imported_email),
			m.subject, m.sender_name, m.sender_address, m.category, m.received_at_utc,
			c.approved_at_utc, c.execute_after_utc, c.moved_at_utc, c.restored_at_utc,
			c.completed_at_utc, COALESCE(c.last_error, ''), c.attempt_count
		FROM cleanup_actions c
		JOIN messages m ON m.id = c.message_id
		JOIN accounts a ON a.id = m.account_id
		WHERE `+conditions+`
		ORDER BY CASE c.state WHEN 'candidate' THEN 0 WHEN 'holding' THEN 1 WHEN 'failed' THEN 2 ELSE 3 END,
			COALESCE(c.execute_after_utc, m.received_at_utc), c.id DESC LIMIT ?
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("list cleanup actions: %w", err)
	}
	defer rows.Close()
	items := make([]CleanupItem, 0)
	for rows.Next() {
		var item CleanupItem
		var received string
		var approved, executeAfter, moved, restored, completed sql.NullString
		if err := rows.Scan(&item.PublicID, &item.State, &item.CandidateReason, &item.MessagePublicID,
			&item.AccountPublicID, &item.AccountName, &item.Subject, &item.SenderName, &item.SenderAddress,
			&item.Category, &received, &approved, &executeAfter, &moved, &restored, &completed,
			&item.LastError, &item.AttemptCount); err != nil {
			return nil, fmt.Errorf("scan cleanup action: %w", err)
		}
		var parseErr error
		item.ReceivedAt, parseErr = parseStoredTime(received)
		if parseErr != nil {
			return nil, parseErr
		}
		if item.ApprovedAt, parseErr = parseNullableStoredTime(approved); parseErr != nil {
			return nil, parseErr
		}
		if item.ExecuteAfter, parseErr = parseNullableStoredTime(executeAfter); parseErr != nil {
			return nil, parseErr
		}
		if item.MovedAt, parseErr = parseNullableStoredTime(moved); parseErr != nil {
			return nil, parseErr
		}
		if item.RestoredAt, parseErr = parseNullableStoredTime(restored); parseErr != nil {
			return nil, parseErr
		}
		if item.CompletedAt, parseErr = parseNullableStoredTime(completed); parseErr != nil {
			return nil, parseErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) ApproveCleanup(ctx context.Context, publicID string) (CleanupItem, error) {
	record, err := s.loadCleanupRecord(ctx, publicID)
	if err != nil {
		return CleanupItem{}, err
	}
	lock := s.accountLock(record.accountID)
	lock.Lock()
	defer lock.Unlock()
	record, err = s.loadCleanupRecord(ctx, publicID)
	if err != nil {
		return CleanupItem{}, err
	}
	if record.state != "candidate" {
		return CleanupItem{}, ErrCleanupStateConflict
	}
	classification := ClassificationResult{Category: record.category, Protected: record.messageLocked, ProtectionReason: record.protection.String}
	eligibility := cleanupEligibilityFor(ClassificationInput{Flagged: record.flagged, AccountLocked: record.accountLocked}, classification)
	if !eligibility.Eligible {
		return CleanupItem{}, ErrCleanupProtected
	}
	holdingFolder, err := s.ensureCleanupFolder(ctx, record.accountID)
	if err != nil {
		return CleanupItem{}, err
	}
	movedID, err := s.graph.moveMessage(ctx, record.accountID, record.immutableID, holdingFolder)
	if err != nil {
		return CleanupItem{}, err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CleanupItem{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE messages SET immutable_id = ?, hidden_from_inbox = 1, remote_deleted = 0, updated_at_utc = ?
		WHERE id = ?
	`, movedID, formatTime(now), record.messageID); err != nil {
		return CleanupItem{}, fmt.Errorf("record cleanup message move: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE cleanup_actions SET state = 'holding', original_folder_graph_id = ?, holding_folder_graph_id = ?,
			approved_at_utc = ?, execute_after_utc = ?, moved_at_utc = ?, restored_at_utc = NULL,
			completed_at_utc = NULL, last_error = NULL, attempt_count = 0, next_retry_at_utc = NULL,
			updated_at_utc = ? WHERE id = ? AND state = 'candidate'
	`, record.originalFolder, holdingFolder, formatTime(now), formatTime(now.Add(cleanupGracePeriod)),
		formatTime(now), formatTime(now), record.actionID); err != nil {
		return CleanupItem{}, fmt.Errorf("approve cleanup action: %w", err)
	}
	if err := s.recordAuditTx(ctx, tx, "cleanup.approved", "admin", "cleanup_action", record.actionPublicID,
		map[string]any{"message": record.messagePublic, "execute_after": formatTime(now.Add(cleanupGracePeriod))}); err != nil {
		return CleanupItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return CleanupItem{}, fmt.Errorf("commit cleanup approval: %w", err)
	}
	return s.getCleanupItem(ctx, record.actionPublicID)
}

func (s *Service) RestoreCleanup(ctx context.Context, publicID string) (CleanupItem, error) {
	record, err := s.loadCleanupRecord(ctx, publicID)
	if err != nil {
		return CleanupItem{}, err
	}
	if record.state != "holding" && record.state != "failed" {
		return CleanupItem{}, ErrCleanupStateConflict
	}
	lock := s.accountLock(record.accountID)
	lock.Lock()
	defer lock.Unlock()
	record, err = s.loadCleanupRecord(ctx, publicID)
	if err != nil {
		return CleanupItem{}, err
	}
	if record.state != "holding" && record.state != "failed" {
		return CleanupItem{}, ErrCleanupStateConflict
	}
	if record.originalFolder == "" {
		return CleanupItem{}, errors.New("cleanup action is missing its original folder")
	}
	movedID, err := s.graph.moveMessage(ctx, record.accountID, record.immutableID, record.originalFolder)
	if err != nil {
		return CleanupItem{}, err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CleanupItem{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE messages SET immutable_id = ?, hidden_from_inbox = 0, remote_deleted = 0, updated_at_utc = ? WHERE id = ?
	`, movedID, formatTime(now), record.messageID); err != nil {
		return CleanupItem{}, fmt.Errorf("record restored message: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE cleanup_actions SET state = 'restored', restored_at_utc = ?, last_error = NULL,
			next_retry_at_utc = NULL, updated_at_utc = ? WHERE id = ?
	`, formatTime(now), formatTime(now), record.actionID); err != nil {
		return CleanupItem{}, fmt.Errorf("record cleanup restore: %w", err)
	}
	if err := s.recordAuditTx(ctx, tx, "cleanup.restored", "admin", "cleanup_action", record.actionPublicID,
		map[string]any{"message": record.messagePublic}); err != nil {
		return CleanupItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return CleanupItem{}, fmt.Errorf("commit cleanup restore: %w", err)
	}
	return s.getCleanupItem(ctx, record.actionPublicID)
}

func (s *Service) RetryCleanup(ctx context.Context, publicID string) error {
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE cleanup_actions SET state = 'holding', attempt_count = 0, last_error = NULL,
			next_retry_at_utc = ?, updated_at_utc = ? WHERE public_id = ? AND state = 'failed'
	`, formatTime(now), formatTime(now), strings.TrimSpace(publicID))
	if err != nil {
		return fmt.Errorf("retry cleanup action: %w", err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrCleanupStateConflict
	}
	return nil
}

func (s *Service) ProcessDueCleanup(ctx context.Context) error {
	now := s.now().UTC()
	rows, err := s.db.QueryContext(ctx, `
		SELECT public_id FROM cleanup_actions
		WHERE state = 'holding' AND execute_after_utc <= ?
			AND (next_retry_at_utc IS NULL OR next_retry_at_utc <= ?)
		ORDER BY execute_after_utc, id LIMIT 20
	`, formatTime(now), formatTime(now))
	if err != nil {
		return fmt.Errorf("list due cleanup actions: %w", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := s.completeCleanup(ctx, id); err != nil && !errors.Is(err, ErrCleanupStateConflict) {
			s.recordCleanupFailure(ctx, id, err)
		}
	}
	return nil
}

func (s *Service) SetMessageFlagged(ctx context.Context, publicID string, flagged bool) error {
	var accountID int64
	var immutableID string
	var messageID int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT id, account_id, immutable_id FROM messages WHERE public_id = ? AND remote_deleted = 0
	`, strings.TrimSpace(publicID)).Scan(&messageID, &accountID, &immutableID); errors.Is(err, sql.ErrNoRows) {
		return ErrMessageNotFound
	} else if err != nil {
		return fmt.Errorf("load flagged message: %w", err)
	}
	lock := s.accountLock(accountID)
	lock.Lock()
	defer lock.Unlock()
	if err := s.graph.setMessageFlagged(ctx, accountID, immutableID, flagged); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET is_flagged = ?, updated_at_utc = ? WHERE id = ?`,
		flagged, formatTime(s.now().UTC()), messageID); err != nil {
		return fmt.Errorf("save message flag: %w", err)
	}
	if err := s.refreshCleanupCandidateTx(ctx, tx, messageID); err != nil {
		return err
	}
	if err := s.recordAuditTx(ctx, tx, "message.flagged", "admin", "message", publicID,
		map[string]any{"flagged": flagged}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) SetAccountCleanupProtected(ctx context.Context, publicID string, protected bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var accountID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE public_id = ?`, strings.TrimSpace(publicID)).Scan(&accountID); errors.Is(err, sql.ErrNoRows) {
		return accounts.ErrAccountNotFound
	} else if err != nil {
		return fmt.Errorf("load protected account: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET cleanup_protected = ?, updated_at_utc = ? WHERE id = ?`,
		protected, formatTime(s.now().UTC()), accountID); err != nil {
		return fmt.Errorf("save account cleanup protection: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM messages WHERE account_id = ? AND remote_deleted = 0`, accountID)
	if err != nil {
		return err
	}
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := s.refreshCleanupCandidateTx(ctx, tx, id); err != nil {
			return err
		}
	}
	if err := s.recordAuditTx(ctx, tx, "account.cleanup_protection", "admin", "account", publicID,
		map[string]any{"protected": protected}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) completeCleanup(ctx context.Context, publicID string) error {
	record, err := s.loadCleanupRecord(ctx, publicID)
	if err != nil {
		return err
	}
	if record.state != "holding" {
		return ErrCleanupStateConflict
	}
	lock := s.accountLock(record.accountID)
	lock.Lock()
	defer lock.Unlock()
	record, err = s.loadCleanupRecord(ctx, publicID)
	if err != nil {
		return err
	}
	if record.state != "holding" {
		return ErrCleanupStateConflict
	}
	classification := ClassificationResult{Category: record.category, Protected: record.messageLocked, ProtectionReason: record.protection.String}
	eligibility := cleanupEligibilityFor(ClassificationInput{Flagged: record.flagged, AccountLocked: record.accountLocked}, classification)
	if !eligibility.Eligible {
		now := s.now().UTC()
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		result, err := tx.ExecContext(ctx, `
			UPDATE cleanup_actions SET state = 'failed', last_error = 'cleanup_protected',
				next_retry_at_utc = NULL, updated_at_utc = ? WHERE id = ? AND state = 'holding'
		`, formatTime(now), record.actionID)
		if err != nil {
			return fmt.Errorf("block protected cleanup action: %w", err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read protected cleanup result: %w", err)
		}
		if updated != 1 {
			return ErrCleanupStateConflict
		}
		if err := s.recordAuditTx(ctx, tx, "cleanup.blocked_by_protection", "system", "cleanup_action", record.actionPublicID,
			map[string]any{"message": record.messagePublic, "reason": eligibility.Reason}); err != nil {
			return err
		}
		return tx.Commit()
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
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE messages SET immutable_id = ?, body_text = NULL, body_cached_at_utc = NULL,
			hidden_from_inbox = 1, updated_at_utc = ? WHERE id = ?
	`, movedID, formatTime(now), record.messageID); err != nil {
		return fmt.Errorf("clear completed cleanup body: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE cleanup_actions SET state = 'deleted', completed_at_utc = ?, last_error = NULL,
			next_retry_at_utc = NULL, updated_at_utc = ? WHERE id = ? AND state = 'holding'
	`, formatTime(now), formatTime(now), record.actionID); err != nil {
		return fmt.Errorf("complete cleanup action: %w", err)
	}
	if err := s.recordAuditTx(ctx, tx, "cleanup.moved_to_deleted_items", "system", "cleanup_action", record.actionPublicID,
		map[string]any{"message": record.messagePublic}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) recordCleanupFailure(ctx context.Context, publicID string, cleanupErr error) {
	now := s.now().UTC()
	var attempts int
	_ = s.db.QueryRowContext(ctx, `SELECT attempt_count FROM cleanup_actions WHERE public_id = ?`, publicID).Scan(&attempts)
	attempts++
	state := "holding"
	if attempts >= 8 {
		state = "failed"
	}
	next := now.Add(cleanupRetryDelay(attempts))
	_, _ = s.db.ExecContext(ctx, `
		UPDATE cleanup_actions SET state = ?, last_error = ?, attempt_count = ?, next_retry_at_utc = ?,
			updated_at_utc = ? WHERE public_id = ? AND state = 'holding'
	`, state, cleanupErrorCode(cleanupErr), attempts, formatTime(next), formatTime(now), publicID)
}

func (s *Service) ensureCleanupFolder(ctx context.Context, accountID int64) (string, error) {
	var folderID string
	err := s.db.QueryRowContext(ctx, `SELECT graph_folder_id FROM cleanup_folders WHERE account_id = ?`, accountID).Scan(&folderID)
	if err == nil && folderID != "" {
		return folderID, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("load cleanup folder: %w", err)
	}
	folders, err := s.graph.listMailFolders(ctx, accountID)
	if err != nil {
		return "", err
	}
	for _, folder := range folders {
		if strings.EqualFold(folder.DisplayName, cleanupFolderName) {
			folderID = folder.ID
			break
		}
	}
	if folderID == "" {
		folder, err := s.graph.createMailFolder(ctx, accountID, cleanupFolderName)
		if err != nil {
			return "", err
		}
		folderID = folder.ID
	}
	now := s.now().UTC()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO cleanup_folders (account_id, graph_folder_id, display_name, created_at_utc, updated_at_utc)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET graph_folder_id = excluded.graph_folder_id,
			display_name = excluded.display_name, updated_at_utc = excluded.updated_at_utc
	`, accountID, folderID, cleanupFolderName, formatTime(now), formatTime(now)); err != nil {
		return "", fmt.Errorf("save cleanup folder: %w", err)
	}
	return folderID, nil
}

func (s *Service) loadCleanupRecord(ctx context.Context, publicID string) (cleanupRecord, error) {
	var record cleanupRecord
	err := s.db.QueryRowContext(ctx, `
		SELECT c.id, c.public_id, c.state, m.id, m.public_id, a.id, a.public_id, m.immutable_id,
			COALESCE(c.original_folder_graph_id, f.graph_id), COALESCE(c.holding_folder_graph_id, ''),
			m.category, m.is_flagged, a.cleanup_protected, m.cleanup_protected, m.cleanup_protection_reason
		FROM cleanup_actions c
		JOIN messages m ON m.id = c.message_id
		JOIN accounts a ON a.id = m.account_id
		JOIN folders f ON f.id = m.folder_id
		WHERE c.public_id = ?
	`, strings.TrimSpace(publicID)).Scan(&record.actionID, &record.actionPublicID, &record.state,
		&record.messageID, &record.messagePublic, &record.accountID, &record.accountPublic,
		&record.immutableID, &record.originalFolder, &record.holdingFolder, &record.category,
		&record.flagged, &record.accountLocked, &record.messageLocked, &record.protection)
	if errors.Is(err, sql.ErrNoRows) {
		return cleanupRecord{}, ErrCleanupNotFound
	}
	if err != nil {
		return cleanupRecord{}, fmt.Errorf("load cleanup action: %w", err)
	}
	return record, nil
}

func (s *Service) getCleanupItem(ctx context.Context, publicID string) (CleanupItem, error) {
	var item CleanupItem
	var received string
	var approved, executeAfter, moved, restored, completed sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT c.public_id, c.state, c.candidate_reason, m.public_id, a.public_id,
			COALESCE(NULLIF(a.display_name, ''), NULLIF(a.primary_email, ''), a.imported_email),
			m.subject, m.sender_name, m.sender_address, m.category, m.received_at_utc,
			c.approved_at_utc, c.execute_after_utc, c.moved_at_utc, c.restored_at_utc,
			c.completed_at_utc, COALESCE(c.last_error, ''), c.attempt_count
		FROM cleanup_actions c
		JOIN messages m ON m.id = c.message_id
		JOIN accounts a ON a.id = m.account_id
		WHERE c.public_id = ?
	`, strings.TrimSpace(publicID)).Scan(&item.PublicID, &item.State, &item.CandidateReason, &item.MessagePublicID,
		&item.AccountPublicID, &item.AccountName, &item.Subject, &item.SenderName, &item.SenderAddress,
		&item.Category, &received, &approved, &executeAfter, &moved, &restored, &completed,
		&item.LastError, &item.AttemptCount)
	if errors.Is(err, sql.ErrNoRows) {
		return CleanupItem{}, ErrCleanupNotFound
	}
	if err != nil {
		return CleanupItem{}, fmt.Errorf("load cleanup item: %w", err)
	}
	item.ReceivedAt, err = parseStoredTime(received)
	if err != nil {
		return CleanupItem{}, err
	}
	if item.ApprovedAt, err = parseNullableStoredTime(approved); err != nil {
		return CleanupItem{}, err
	}
	if item.ExecuteAfter, err = parseNullableStoredTime(executeAfter); err != nil {
		return CleanupItem{}, err
	}
	if item.MovedAt, err = parseNullableStoredTime(moved); err != nil {
		return CleanupItem{}, err
	}
	if item.RestoredAt, err = parseNullableStoredTime(restored); err != nil {
		return CleanupItem{}, err
	}
	if item.CompletedAt, err = parseNullableStoredTime(completed); err != nil {
		return CleanupItem{}, err
	}
	return item, nil
}

func validCleanupState(value string) bool {
	switch value {
	case "candidate", "dismissed", "holding", "restored", "deleted", "failed":
		return true
	default:
		return false
	}
}

func cleanupRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 6 {
		attempts = 6
	}
	return time.Duration(1<<(attempts-1)) * 5 * time.Minute
}

func cleanupErrorCode(err error) string {
	var graphErr *GraphError
	if errors.As(err, &graphErr) {
		if graphErr.Code != "" {
			return graphErr.Code
		}
		return fmt.Sprintf("graph_http_%d", graphErr.Status)
	}
	return "cleanup_move_failed"
}
