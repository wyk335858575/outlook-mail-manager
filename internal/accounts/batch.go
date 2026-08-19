package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidAccountBatch = errors.New("invalid account batch")

type BatchAccountItemResult struct {
	PublicID string `json:"public_id"`
	State    string `json:"state"`
	Status   string `json:"status,omitempty"`
	Error    string `json:"error,omitempty"`
}

type BatchAccountResult struct {
	Requested int                      `json:"requested"`
	Succeeded int                      `json:"succeeded"`
	Skipped   int                      `json:"skipped"`
	Failed    int                      `json:"failed"`
	Results   []BatchAccountItemResult `json:"results"`
}

type batchAccountRecord struct {
	id           int64
	publicID     string
	status       string
	hasToken     bool
	syncFailures int
}

func (s *Service) SetDisabledBatch(ctx context.Context, publicIDs []string, disabled bool) (BatchAccountResult, error) {
	ids, err := normalizeBatchAccountIDs(publicIDs)
	if err != nil {
		return BatchAccountResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BatchAccountResult{}, fmt.Errorf("begin account status batch: %w", err)
	}
	defer tx.Rollback()
	records, err := loadBatchAccounts(ctx, tx, ids)
	if err != nil {
		return BatchAccountResult{}, err
	}

	result := BatchAccountResult{Requested: len(ids), Results: make([]BatchAccountItemResult, 0, len(ids))}
	changed := make([]batchAccountRecord, 0, len(records))
	for _, publicID := range ids {
		record, exists := records[publicID]
		if !exists {
			result.Failed++
			result.Results = append(result.Results, BatchAccountItemResult{PublicID: publicID, State: "failed", Error: "not_found"})
			continue
		}
		if disabled == (record.status == "disabled") {
			result.Skipped++
			result.Results = append(result.Results, BatchAccountItemResult{PublicID: publicID, State: "skipped", Status: record.status})
			continue
		}
		changed = append(changed, record)
	}

	now := s.now().UTC()
	for start := 0; start < len(changed); start += 200 {
		end := min(start+200, len(changed))
		chunk := changed[start:end]
		values := make([]any, 0, len(chunk)+2)
		values = append(values, formatTime(now), formatTime(now))
		for _, record := range chunk {
			values = append(values, record.id)
		}
		var statement string
		if disabled {
			statement = `UPDATE accounts SET status = 'disabled', disabled_at_utc = ?, updated_at_utc = ? WHERE id IN (` + placeholders(len(chunk)) + `)`
		} else {
			statement = `UPDATE accounts SET status = CASE
				WHEN EXISTS (SELECT 1 FROM account_tokens t WHERE t.account_id = accounts.id)
					THEN CASE WHEN sync_failures > 0 THEN 'degraded' ELSE 'active' END
				ELSE 'pending' END,
				reauth_reason = NULL, disabled_at_utc = NULL, updated_at_utc = ?
				WHERE id IN (` + placeholders(len(chunk)) + `)`
			values = append(values[:0], formatTime(now))
			for _, record := range chunk {
				values = append(values, record.id)
			}
		}
		if _, err := tx.ExecContext(ctx, statement, values...); err != nil {
			return BatchAccountResult{}, fmt.Errorf("update account status batch: %w", err)
		}
	}
	for _, record := range changed {
		if err := insertAudit(ctx, tx, "account_status_changed", "admin", map[string]any{
			"account": record.publicID, "disabled": disabled, "batch": true,
		}, now); err != nil {
			return BatchAccountResult{}, err
		}
		status := "disabled"
		if !disabled {
			status = "pending"
			if record.hasToken && record.syncFailures == 0 {
				status = "active"
			} else if record.hasToken {
				status = "degraded"
			}
		}
		result.Succeeded++
		result.Results = append(result.Results, BatchAccountItemResult{PublicID: record.publicID, State: "updated", Status: status})
	}
	if err := tx.Commit(); err != nil {
		return BatchAccountResult{}, fmt.Errorf("commit account status batch: %w", err)
	}
	for _, record := range changed {
		if disabled {
			s.cancelAccountAuthorization(record.id)
		} else if record.hasToken {
			s.queueHealthCheck(record)
		}
	}
	return orderBatchResults(result, ids), nil
}

func (s *Service) DeleteBatch(ctx context.Context, publicIDs []string) (BatchAccountResult, error) {
	ids, err := normalizeBatchAccountIDs(publicIDs)
	if err != nil {
		return BatchAccountResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BatchAccountResult{}, fmt.Errorf("begin account deletion batch: %w", err)
	}
	defer tx.Rollback()
	records, err := loadBatchAccounts(ctx, tx, ids)
	if err != nil {
		return BatchAccountResult{}, err
	}
	result := BatchAccountResult{Requested: len(ids), Results: make([]BatchAccountItemResult, 0, len(ids))}
	deleted := make([]batchAccountRecord, 0, len(records))
	for _, publicID := range ids {
		record, exists := records[publicID]
		if !exists {
			result.Failed++
			result.Results = append(result.Results, BatchAccountItemResult{PublicID: publicID, State: "failed", Error: "not_found"})
			continue
		}
		deleted = append(deleted, record)
	}

	now := s.now().UTC()
	for start := 0; start < len(deleted); start += 200 {
		end := min(start+200, len(deleted))
		chunk := deleted[start:end]
		accountIDs := make([]any, 0, len(chunk))
		for _, record := range chunk {
			accountIDs = append(accountIDs, record.id)
		}
		in := placeholders(len(chunk))
		for _, statement := range []string{
			`DELETE FROM notification_deliveries WHERE message_id IN (SELECT id FROM messages WHERE account_id IN (` + in + `))`,
			`DELETE FROM cleanup_actions WHERE message_id IN (SELECT id FROM messages WHERE account_id IN (` + in + `))`,
			`DELETE FROM messages WHERE account_id IN (` + in + `)`,
			`DELETE FROM folders WHERE account_id IN (` + in + `)`,
			`DELETE FROM account_tokens WHERE account_id IN (` + in + `)`,
			`DELETE FROM accounts WHERE id IN (` + in + `)`,
		} {
			if _, err := tx.ExecContext(ctx, statement, accountIDs...); err != nil {
				return BatchAccountResult{}, fmt.Errorf("delete account batch data: %w", err)
			}
		}
	}
	for _, record := range deleted {
		if err := insertAudit(ctx, tx, "account_deleted", "admin", map[string]any{
			"account": record.publicID, "local_data_removed": true, "batch": true,
		}, now); err != nil {
			return BatchAccountResult{}, err
		}
		result.Succeeded++
		result.Results = append(result.Results, BatchAccountItemResult{PublicID: record.publicID, State: "deleted"})
	}
	if err := tx.Commit(); err != nil {
		return BatchAccountResult{}, fmt.Errorf("commit account deletion batch: %w", err)
	}
	for _, record := range deleted {
		s.cancelAccountAuthorization(record.id)
	}
	return orderBatchResults(result, ids), nil
}

func normalizeBatchAccountIDs(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > maxImportRows {
		return nil, ErrInvalidAccountBatch
	}
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, ErrInvalidAccountBatch
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result, nil
}

func loadBatchAccounts(ctx context.Context, tx *sql.Tx, publicIDs []string) (map[string]batchAccountRecord, error) {
	result := make(map[string]batchAccountRecord, len(publicIDs))
	for start := 0; start < len(publicIDs); start += 200 {
		end := min(start+200, len(publicIDs))
		chunk := publicIDs[start:end]
		args := make([]any, len(chunk))
		for index, value := range chunk {
			args[index] = value
		}
		rows, err := tx.QueryContext(ctx, `SELECT a.id, a.public_id, a.status, a.sync_failures,
			EXISTS (SELECT 1 FROM account_tokens t WHERE t.account_id = a.id)
			FROM accounts a WHERE a.public_id IN (`+placeholders(len(chunk))+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("load account batch: %w", err)
		}
		for rows.Next() {
			var record batchAccountRecord
			if err := rows.Scan(&record.id, &record.publicID, &record.status, &record.syncFailures, &record.hasToken); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan account batch: %w", err)
			}
			result[record.publicID] = record
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close account batch: %w", err)
		}
	}
	return result, nil
}

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func orderBatchResults(result BatchAccountResult, order []string) BatchAccountResult {
	byID := make(map[string]BatchAccountItemResult, len(result.Results))
	for _, item := range result.Results {
		byID[item.PublicID] = item
	}
	result.Results = result.Results[:0]
	for _, publicID := range order {
		result.Results = append(result.Results, byID[publicID])
	}
	return result
}
