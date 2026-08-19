package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

const batchAccountConcurrency = 4

type MessageReadResult struct {
	PublicID string
	Read     bool
	Err      error
}

type CleanupApprovalResult struct {
	PublicID string
	Item     *CleanupItem
	Err      error
}

type messageBatchRecord struct {
	publicID    string
	accountID   int64
	immutableID string
	isRead      bool
}

type movedCleanupRecord struct {
	record      cleanupRecord
	immutableID string
}

func (s *Service) MarkMessagesRead(ctx context.Context, publicIDs []string) []MessageReadResult {
	ordered := uniquePublicIDs(publicIDs)
	results := make(map[string]MessageReadResult, len(ordered))
	records, err := s.loadMessageBatchRecords(ctx, ordered, 0)
	if err != nil {
		return messageReadErrorResults(ordered, err)
	}
	groups := make(map[int64][]messageBatchRecord)
	found := make(map[string]struct{}, len(records))
	for _, record := range records {
		found[record.publicID] = struct{}{}
		groups[record.accountID] = append(groups[record.accountID], record)
	}
	for _, publicID := range ordered {
		if _, ok := found[publicID]; !ok {
			results[publicID] = MessageReadResult{PublicID: publicID, Err: ErrMessageNotFound}
		}
	}

	type accountGroup struct {
		accountID int64
		records   []messageBatchRecord
	}
	jobs := make(chan accountGroup)
	var resultMu sync.Mutex
	var workers sync.WaitGroup
	workerCount := min(batchAccountConcurrency, len(groups))
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for group := range jobs {
				items := s.markAccountMessagesRead(ctx, group.accountID, group.records)
				resultMu.Lock()
				for _, item := range items {
					results[item.PublicID] = item
				}
				resultMu.Unlock()
			}
		}()
	}
	for accountID, records := range groups {
		jobs <- accountGroup{accountID: accountID, records: records}
	}
	close(jobs)
	workers.Wait()

	orderedResults := make([]MessageReadResult, 0, len(ordered))
	for _, publicID := range ordered {
		orderedResults = append(orderedResults, results[publicID])
	}
	return orderedResults
}

func (s *Service) markAccountMessagesRead(ctx context.Context, accountID int64, requested []messageBatchRecord) []MessageReadResult {
	lock := s.accountLock(accountID)
	lock.Lock()
	defer lock.Unlock()

	publicIDs := make([]string, 0, len(requested))
	for _, record := range requested {
		publicIDs = append(publicIDs, record.publicID)
	}
	records, err := s.loadMessageBatchRecords(ctx, publicIDs, accountID)
	if err != nil {
		return messageReadErrorResults(publicIDs, err)
	}
	results := make(map[string]MessageReadResult, len(publicIDs))
	found := make(map[string]struct{}, len(records))
	pending := make([]messageBatchRecord, 0, len(records))
	for _, record := range records {
		found[record.publicID] = struct{}{}
		if record.isRead {
			results[record.publicID] = MessageReadResult{PublicID: record.publicID, Read: true}
		} else {
			pending = append(pending, record)
		}
	}
	for _, publicID := range publicIDs {
		if _, ok := found[publicID]; !ok {
			results[publicID] = MessageReadResult{PublicID: publicID, Err: ErrMessageNotFound}
		}
	}

	for start := 0; start < len(pending); start += maxGraphBatchRequests {
		end := min(start+maxGraphBatchRequests, len(pending))
		chunk := pending[start:end]
		requests := make([]graphBatchRequest, 0, len(chunk))
		for index, record := range chunk {
			requests = append(requests, graphBatchRequest{
				ID: strconv.Itoa(index), Method: http.MethodPatch,
				URL:     "/me/messages/" + url.PathEscape(record.immutableID),
				Headers: map[string]string{"Content-Type": "application/json", "Prefer": graphIDPreference},
				Body:    map[string]bool{"isRead": true},
			})
		}
		responses, batchErr := s.graph.batch(ctx, accountID, requests)
		if batchErr != nil {
			for _, record := range chunk {
				results[record.publicID] = MessageReadResult{PublicID: record.publicID, Err: batchErr}
			}
			continue
		}
		succeeded := make([]string, 0, len(chunk))
		for index, record := range chunk {
			response, ok := responses[strconv.Itoa(index)]
			if !ok {
				results[record.publicID] = MessageReadResult{PublicID: record.publicID, Err: errors.New("Microsoft Graph batch response is incomplete")}
				continue
			}
			if err := s.graph.batchResponseError(response); err != nil {
				results[record.publicID] = MessageReadResult{PublicID: record.publicID, Err: err}
				continue
			}
			succeeded = append(succeeded, record.publicID)
		}
		if len(succeeded) > 0 {
			if err := s.recordMessagesRead(ctx, accountID, succeeded); err != nil {
				for _, publicID := range succeeded {
					results[publicID] = MessageReadResult{PublicID: publicID, Err: err}
				}
				continue
			}
			for _, publicID := range succeeded {
				results[publicID] = MessageReadResult{PublicID: publicID, Read: true}
			}
		}
	}

	ordered := make([]MessageReadResult, 0, len(publicIDs))
	for _, publicID := range publicIDs {
		ordered = append(ordered, results[publicID])
	}
	return ordered
}

func (s *Service) ApproveCleanupBatch(ctx context.Context, publicIDs []string) []CleanupApprovalResult {
	ordered := uniquePublicIDs(publicIDs)
	results := make(map[string]CleanupApprovalResult, len(ordered))
	records, err := s.loadCleanupRecords(ctx, ordered, 0)
	if err != nil {
		return cleanupApprovalErrorResults(ordered, err)
	}
	groups := make(map[int64][]cleanupRecord)
	found := make(map[string]struct{}, len(records))
	for _, record := range records {
		found[record.actionPublicID] = struct{}{}
		groups[record.accountID] = append(groups[record.accountID], record)
	}
	for _, publicID := range ordered {
		if _, ok := found[publicID]; !ok {
			results[publicID] = CleanupApprovalResult{PublicID: publicID, Err: ErrCleanupNotFound}
		}
	}

	type accountGroup struct {
		accountID int64
		records   []cleanupRecord
	}
	jobs := make(chan accountGroup)
	var resultMu sync.Mutex
	var workers sync.WaitGroup
	workerCount := min(batchAccountConcurrency, len(groups))
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for group := range jobs {
				items := s.approveAccountCleanupBatch(ctx, group.accountID, group.records)
				resultMu.Lock()
				for _, item := range items {
					results[item.PublicID] = item
				}
				resultMu.Unlock()
			}
		}()
	}
	for accountID, records := range groups {
		jobs <- accountGroup{accountID: accountID, records: records}
	}
	close(jobs)
	workers.Wait()

	orderedResults := make([]CleanupApprovalResult, 0, len(ordered))
	for _, publicID := range ordered {
		orderedResults = append(orderedResults, results[publicID])
	}
	return orderedResults
}

func (s *Service) approveAccountCleanupBatch(ctx context.Context, accountID int64, requested []cleanupRecord) []CleanupApprovalResult {
	lock := s.accountLock(accountID)
	lock.Lock()
	defer lock.Unlock()

	publicIDs := make([]string, 0, len(requested))
	for _, record := range requested {
		publicIDs = append(publicIDs, record.actionPublicID)
	}
	records, err := s.loadCleanupRecords(ctx, publicIDs, accountID)
	if err != nil {
		return cleanupApprovalErrorResults(publicIDs, err)
	}
	results := make(map[string]CleanupApprovalResult, len(publicIDs))
	found := make(map[string]struct{}, len(records))
	eligible := make([]cleanupRecord, 0, len(records))
	for _, record := range records {
		found[record.actionPublicID] = struct{}{}
		if record.state != "candidate" {
			results[record.actionPublicID] = CleanupApprovalResult{PublicID: record.actionPublicID, Err: ErrCleanupStateConflict}
			continue
		}
		classification := ClassificationResult{Category: record.category, Protected: record.messageLocked, ProtectionReason: record.protection.String}
		if !cleanupEligibilityFor(ClassificationInput{Flagged: record.flagged, AccountLocked: record.accountLocked}, classification).Eligible {
			results[record.actionPublicID] = CleanupApprovalResult{PublicID: record.actionPublicID, Err: ErrCleanupProtected}
			continue
		}
		eligible = append(eligible, record)
	}
	for _, publicID := range publicIDs {
		if _, ok := found[publicID]; !ok {
			results[publicID] = CleanupApprovalResult{PublicID: publicID, Err: ErrCleanupNotFound}
		}
	}
	if len(eligible) == 0 {
		return orderedCleanupApprovalResults(publicIDs, results)
	}
	holdingFolder, err := s.ensureCleanupFolder(ctx, accountID)
	if err != nil {
		for _, record := range eligible {
			results[record.actionPublicID] = CleanupApprovalResult{PublicID: record.actionPublicID, Err: err}
		}
		return orderedCleanupApprovalResults(publicIDs, results)
	}

	for start := 0; start < len(eligible); start += maxGraphBatchRequests {
		end := min(start+maxGraphBatchRequests, len(eligible))
		chunk := eligible[start:end]
		requests := make([]graphBatchRequest, 0, len(chunk))
		for index, record := range chunk {
			requests = append(requests, graphBatchRequest{
				ID: strconv.Itoa(index), Method: http.MethodPost,
				URL:     "/me/messages/" + url.PathEscape(record.immutableID) + "/move",
				Headers: map[string]string{"Content-Type": "application/json", "Prefer": graphIDPreference},
				Body:    map[string]string{"destinationId": holdingFolder},
			})
		}
		responses, batchErr := s.graph.batch(ctx, accountID, requests)
		if batchErr != nil {
			for _, record := range chunk {
				results[record.actionPublicID] = CleanupApprovalResult{PublicID: record.actionPublicID, Err: batchErr}
			}
			continue
		}
		moved := make([]movedCleanupRecord, 0, len(chunk))
		for index, record := range chunk {
			response, ok := responses[strconv.Itoa(index)]
			if !ok {
				results[record.actionPublicID] = CleanupApprovalResult{PublicID: record.actionPublicID, Err: errors.New("Microsoft Graph batch response is incomplete")}
				continue
			}
			if err := s.graph.batchResponseError(response); err != nil {
				results[record.actionPublicID] = CleanupApprovalResult{PublicID: record.actionPublicID, Err: err}
				continue
			}
			var body struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(response.Body, &body); err != nil || body.ID == "" {
				results[record.actionPublicID] = CleanupApprovalResult{PublicID: record.actionPublicID, Err: errors.New("Microsoft Graph moved a message without returning its ID")}
				continue
			}
			moved = append(moved, movedCleanupRecord{record: record, immutableID: body.ID})
		}
		if len(moved) == 0 {
			continue
		}
		if err := s.recordCleanupApprovals(ctx, holdingFolder, moved); err != nil {
			for _, item := range moved {
				results[item.record.actionPublicID] = CleanupApprovalResult{PublicID: item.record.actionPublicID, Err: err}
			}
			continue
		}
		for _, movedItem := range moved {
			item, err := s.getCleanupItem(ctx, movedItem.record.actionPublicID)
			if err != nil {
				results[movedItem.record.actionPublicID] = CleanupApprovalResult{PublicID: movedItem.record.actionPublicID, Err: err}
				continue
			}
			results[movedItem.record.actionPublicID] = CleanupApprovalResult{PublicID: movedItem.record.actionPublicID, Item: &item}
		}
	}
	return orderedCleanupApprovalResults(publicIDs, results)
}

func (s *Service) loadMessageBatchRecords(ctx context.Context, publicIDs []string, accountID int64) ([]messageBatchRecord, error) {
	if len(publicIDs) == 0 {
		return nil, nil
	}
	clause, args := stringInClause(publicIDs)
	query := `SELECT public_id, account_id, immutable_id, is_read FROM messages WHERE remote_deleted = 0 AND public_id IN (` + clause + `)`
	if accountID != 0 {
		query += " AND account_id = ?"
		args = append(args, accountID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load messages for batch update: %w", err)
	}
	defer rows.Close()
	var records []messageBatchRecord
	for rows.Next() {
		var record messageBatchRecord
		if err := rows.Scan(&record.publicID, &record.accountID, &record.immutableID, &record.isRead); err != nil {
			return nil, fmt.Errorf("scan message batch update: %w", err)
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Service) loadCleanupRecords(ctx context.Context, publicIDs []string, accountID int64) ([]cleanupRecord, error) {
	if len(publicIDs) == 0 {
		return nil, nil
	}
	clause, args := stringInClause(publicIDs)
	query := `
		SELECT c.id, c.public_id, c.state, m.id, m.public_id, a.id, a.public_id, m.immutable_id,
			COALESCE(c.original_folder_graph_id, f.graph_id), COALESCE(c.holding_folder_graph_id, ''),
			m.category, m.is_flagged, a.cleanup_protected, m.cleanup_protected, m.cleanup_protection_reason
		FROM cleanup_actions c
		JOIN messages m ON m.id = c.message_id
		JOIN accounts a ON a.id = m.account_id
		JOIN folders f ON f.id = m.folder_id
		WHERE c.public_id IN (` + clause + `)`
	if accountID != 0 {
		query += " AND a.id = ?"
		args = append(args, accountID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load cleanup actions for batch approval: %w", err)
	}
	defer rows.Close()
	var records []cleanupRecord
	for rows.Next() {
		var record cleanupRecord
		if err := rows.Scan(&record.actionID, &record.actionPublicID, &record.state,
			&record.messageID, &record.messagePublic, &record.accountID, &record.accountPublic,
			&record.immutableID, &record.originalFolder, &record.holdingFolder, &record.category,
			&record.flagged, &record.accountLocked, &record.messageLocked, &record.protection); err != nil {
			return nil, fmt.Errorf("scan cleanup batch approval: %w", err)
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Service) recordMessagesRead(ctx context.Context, accountID int64, publicIDs []string) error {
	clause, args := stringInClause(publicIDs)
	queryArgs := []any{formatTime(s.now().UTC()), accountID}
	queryArgs = append(queryArgs, args...)
	result, err := s.db.ExecContext(ctx, `UPDATE messages SET is_read = 1, updated_at_utc = ? WHERE account_id = ? AND remote_deleted = 0 AND public_id IN (`+clause+`)`, queryArgs...)
	if err != nil {
		return fmt.Errorf("record batch message read state: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read batch message update result: %w", err)
	}
	if rows != int64(len(publicIDs)) {
		return ErrMessageNotFound
	}
	return nil
}

func (s *Service) recordCleanupApprovals(ctx context.Context, holdingFolder string, moved []movedCleanupRecord) error {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range moved {
		if _, err := tx.ExecContext(ctx, `
			UPDATE messages SET immutable_id = ?, hidden_from_inbox = 1, remote_deleted = 0, updated_at_utc = ?
			WHERE id = ?
		`, item.immutableID, formatTime(now), item.record.messageID); err != nil {
			return fmt.Errorf("record cleanup message move: %w", err)
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE cleanup_actions SET state = 'holding', original_folder_graph_id = ?, holding_folder_graph_id = ?,
				approved_at_utc = ?, execute_after_utc = ?, moved_at_utc = ?, restored_at_utc = NULL,
				completed_at_utc = NULL, last_error = NULL, attempt_count = 0, next_retry_at_utc = NULL,
				updated_at_utc = ? WHERE id = ? AND state = 'candidate'
		`, item.record.originalFolder, holdingFolder, formatTime(now), formatTime(now.Add(cleanupGracePeriod)),
			formatTime(now), formatTime(now), item.record.actionID)
		if err != nil {
			return fmt.Errorf("approve cleanup action: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil || rows != 1 {
			return ErrCleanupStateConflict
		}
		if err := s.recordAuditTx(ctx, tx, "cleanup.approved", "admin", "cleanup_action", item.record.actionPublicID,
			map[string]any{"message": item.record.messagePublic, "execute_after": formatTime(now.Add(cleanupGracePeriod))}); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit cleanup approvals: %w", err)
	}
	return nil
}

func uniquePublicIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func stringInClause(values []string) (string, []any) {
	placeholders := make([]string, len(values))
	args := make([]any, len(values))
	for index, value := range values {
		placeholders[index] = "?"
		args[index] = value
	}
	return strings.Join(placeholders, ","), args
}

func messageReadErrorResults(publicIDs []string, err error) []MessageReadResult {
	results := make([]MessageReadResult, 0, len(publicIDs))
	for _, publicID := range publicIDs {
		results = append(results, MessageReadResult{PublicID: publicID, Err: err})
	}
	return results
}

func cleanupApprovalErrorResults(publicIDs []string, err error) []CleanupApprovalResult {
	results := make([]CleanupApprovalResult, 0, len(publicIDs))
	for _, publicID := range publicIDs {
		results = append(results, CleanupApprovalResult{PublicID: publicID, Err: err})
	}
	return results
}

func orderedCleanupApprovalResults(publicIDs []string, results map[string]CleanupApprovalResult) []CleanupApprovalResult {
	ordered := make([]CleanupApprovalResult, 0, len(publicIDs))
	for _, publicID := range publicIDs {
		ordered = append(ordered, results[publicID])
	}
	return ordered
}
