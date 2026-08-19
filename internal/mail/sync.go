package mail

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"

	"outlook-mail-manager/internal/accounts"
)

const (
	maxDeltaPages                 = 1000
	maxVerificationRescuesPerSync = 50
	syncMessageSelect             = "id,internetMessageId,subject,from,receivedDateTime,isRead,flag,body"
)

type folderRecord struct {
	id          int64
	graphID     string
	wellKnown   string
	displayName string
	deltaLink   string
	windowStart time.Time
}

type deltaResponse struct {
	Value     []graphMessage `json:"value"`
	NextLink  string         `json:"@odata.nextLink"`
	DeltaLink string         `json:"@odata.deltaLink"`
}

type graphMessage struct {
	ID                string `json:"id"`
	InternetMessageID string `json:"internetMessageId"`
	Subject           string `json:"subject"`
	ReceivedDateTime  string `json:"receivedDateTime"`
	IsRead            bool   `json:"isRead"`
	From              struct {
		EmailAddress struct {
			Name    string `json:"name"`
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"from"`
	Flag struct {
		Status string `json:"flagStatus"`
	} `json:"flag"`
	Body struct {
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	} `json:"body"`
	Removed *struct {
		Reason string `json:"reason"`
	} `json:"@removed"`
}

func (s *Service) syncAccount(ctx context.Context, accountID int64) error {
	lock := s.accountLock(accountID)
	lock.Lock()
	defer lock.Unlock()

	var status string
	err := s.db.QueryRowContext(ctx, "SELECT status FROM accounts WHERE id = ?", accountID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return accounts.ErrAccountNotFound
	}
	if err != nil {
		return fmt.Errorf("load account sync status: %w", err)
	}
	switch status {
	case "disabled":
		return accounts.ErrAccountDisabled
	case "pending", "reauth_required":
		return accounts.ErrReauthorizationRequired
	}

	folders, err := s.ensureFolders(ctx, accountID)
	if err != nil {
		return err
	}
	for _, folder := range folders {
		if err := s.syncFolder(ctx, accountID, folder); err != nil {
			s.recordSyncFailure(ctx, accountID, folder.id, err)
			return err
		}
	}
	if err := s.rescueVerificationMessages(ctx, accountID); err != nil {
		for _, folder := range folders {
			if folder.wellKnown == "junkemail" {
				s.recordSyncFailure(ctx, accountID, folder.id, err)
				break
			}
		}
		return err
	}
	now := s.now().UTC()
	if _, err := s.db.ExecContext(ctx, `
		UPDATE accounts SET last_sync_success_at_utc = ?, last_sync_error = NULL,
			sync_failures = 0, sync_next_retry_at_utc = NULL, sync_backlog = 0,
			status = CASE WHEN status = 'degraded' THEN 'active' ELSE status END,
			updated_at_utc = ? WHERE id = ? AND status NOT IN ('disabled', 'reauth_required')
	`, formatTime(now), formatTime(now), accountID); err != nil {
		return fmt.Errorf("record account sync success: %w", err)
	}
	return nil
}

func (s *Service) rescueVerificationMessages(ctx context.Context, accountID int64) error {
	var inboxFolderID int64
	var inboxGraphID string
	if err := s.db.QueryRowContext(ctx, `
		SELECT id, graph_id FROM folders WHERE account_id = ? AND well_known_name = 'inbox'
	`, accountID).Scan(&inboxFolderID, &inboxGraphID); err != nil {
		return fmt.Errorf("load verification rescue destination: %w", err)
	}

	type candidate struct {
		id          int64
		publicID    string
		immutableID string
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.public_id, m.immutable_id
		FROM messages m
		JOIN folders f ON f.id = m.folder_id
		WHERE m.account_id = ? AND f.well_known_name = 'junkemail'
			AND m.category = 'verification' AND m.remote_deleted = 0 AND m.hidden_from_inbox = 0
		ORDER BY m.received_at_utc DESC, m.id DESC
		LIMIT ?
	`, accountID, maxVerificationRescuesPerSync)
	if err != nil {
		return fmt.Errorf("list verification messages in junk folder: %w", err)
	}
	candidates := make([]candidate, 0)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.publicID, &item.immutableID); err != nil {
			rows.Close()
			return fmt.Errorf("scan verification rescue candidate: %w", err)
		}
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, item := range candidates {
		movedID, err := s.graph.moveMessage(ctx, accountID, item.immutableID, inboxGraphID)
		if err != nil {
			return fmt.Errorf("move verification message to inbox: %w", err)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin verification rescue update: %w", err)
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE messages SET immutable_id = ?, folder_id = ?, updated_at_utc = ?
			WHERE id = ? AND account_id = ? AND remote_deleted = 0
		`, movedID, inboxFolderID, formatTime(s.now().UTC()), item.id, accountID)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("record verification message move: %w", err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("read verification move result: %w", err)
		}
		if updated == 1 {
			if err := s.recordAuditTx(ctx, tx, "verification.moved_to_inbox", "system", "message", item.publicID, map[string]any{
				"from_folder": "junkemail", "to_folder": "inbox",
			}); err != nil {
				tx.Rollback()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit verification message move: %w", err)
		}
	}
	return nil
}

func (s *Service) ensureFolders(ctx context.Context, accountID int64) ([]folderRecord, error) {
	now := s.now().UTC()
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	windowStart := now.Add(-time.Duration(settings.InitialSyncDays) * 24 * time.Hour)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin folder setup: %w", err)
	}
	defer tx.Rollback()
	for _, folder := range []struct{ name, display string }{
		{name: "inbox", display: "收件箱"},
		{name: "junkemail", display: "垃圾邮件"},
	} {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO folders (
				account_id, graph_id, well_known_name, display_name, created_at_utc, updated_at_utc
			) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(account_id, well_known_name) DO UPDATE SET
				display_name = excluded.display_name, updated_at_utc = excluded.updated_at_utc
		`, accountID, folder.name, folder.name, folder.display, formatTime(now), formatTime(now)); err != nil {
			return nil, fmt.Errorf("ensure mail folder: %w", err)
		}
		var folderID int64
		if err := tx.QueryRowContext(ctx,
			"SELECT id FROM folders WHERE account_id = ? AND well_known_name = ?",
			accountID, folder.name,
		).Scan(&folderID); err != nil {
			return nil, fmt.Errorf("load mail folder: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sync_cursors (folder_id, initial_window_start_utc, updated_at_utc)
			VALUES (?, ?, ?) ON CONFLICT(folder_id) DO NOTHING
		`, folderID, formatTime(windowStart), formatTime(now)); err != nil {
			return nil, fmt.Errorf("ensure folder cursor: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit folder setup: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT f.id, f.graph_id, f.well_known_name, f.display_name,
			COALESCE(c.delta_link, ''), c.initial_window_start_utc
		FROM folders f JOIN sync_cursors c ON c.folder_id = f.id
		WHERE f.account_id = ? ORDER BY CASE f.well_known_name WHEN 'inbox' THEN 0 ELSE 1 END
	`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list mail folders: %w", err)
	}
	defer rows.Close()
	result := make([]folderRecord, 0, 2)
	for rows.Next() {
		var folder folderRecord
		var windowValue string
		if err := rows.Scan(&folder.id, &folder.graphID, &folder.wellKnown, &folder.displayName, &folder.deltaLink, &windowValue); err != nil {
			return nil, fmt.Errorf("scan mail folder: %w", err)
		}
		folder.windowStart, err = time.Parse(time.RFC3339Nano, windowValue)
		if err != nil {
			return nil, fmt.Errorf("parse initial sync window: %w", err)
		}
		result = append(result, folder)
	}
	return result, rows.Err()
}

func (s *Service) syncFolder(ctx context.Context, accountID int64, folder folderRecord) error {
	target := folder.deltaLink
	usingCursor := target != ""
	if target == "" {
		target = initialDeltaTarget(folder)
	}
	resetAttempted := false
	for pageNumber := 0; pageNumber < maxDeltaPages; pageNumber++ {
		var page deltaResponse
		err := s.graph.getJSON(ctx, accountID, target, &page)
		var graphErr *GraphError
		if errors.As(err, &graphErr) && graphErr.Status == http.StatusGone && usingCursor && !resetAttempted {
			if err := s.resetCursor(ctx, folder.id); err != nil {
				return err
			}
			target = initialDeltaTarget(folder)
			usingCursor = false
			resetAttempted = true
			continue
		}
		if err != nil {
			return err
		}
		page.Value, err = s.hydrateDeltaMessages(ctx, accountID, page.Value)
		if err != nil {
			return err
		}
		disk, err := s.DiskState()
		if err != nil {
			return err
		}
		finalDelta := ""
		if page.NextLink == "" {
			finalDelta = page.DeltaLink
			if finalDelta == "" {
				return errors.New("Graph delta response did not include a final cursor")
			}
		}
		if err := s.applyPage(ctx, accountID, folder, page.Value, disk.MetadataOnly, finalDelta); err != nil {
			return err
		}
		if page.NextLink == "" {
			return nil
		}
		target = page.NextLink
	}
	return errors.New("Graph delta pagination exceeded the safety limit")
}

func (s *Service) hydrateDeltaMessages(ctx context.Context, accountID int64, messages []graphMessage) ([]graphMessage, error) {
	for index := range messages {
		message := messages[index]
		if message.ID == "" {
			return nil, errors.New("Graph message is missing an immutable ID")
		}
		if message.Removed != nil || message.ReceivedDateTime != "" {
			continue
		}
		full, err := s.graph.getMessageForSync(ctx, accountID, message.ID)
		if err != nil {
			return nil, fmt.Errorf("hydrate partial Graph message: %w", err)
		}
		messages[index] = full
	}
	return messages, nil
}

func initialDeltaTarget(folder folderRecord) string {
	query := url.Values{}
	query.Set("$filter", "receivedDateTime ge "+folder.windowStart.UTC().Format(time.RFC3339))
	query.Set("$orderby", "receivedDateTime desc")
	query.Set("$select", syncMessageSelect)
	query.Set("$top", "50")
	return fmt.Sprintf("me/mailFolders/%s/messages/delta?%s", url.PathEscape(folder.graphID), query.Encode())
}

func (s *Service) applyPage(
	ctx context.Context,
	accountID int64,
	folder folderRecord,
	messages []graphMessage,
	metadataOnly bool,
	finalDelta string,
) error {
	now := s.now().UTC()
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return err
	}
	rules, err := s.ListClassificationRules(ctx)
	if err != nil {
		return err
	}
	bodyLimit := settings.BodyCacheKiB << 10
	newMessageIDs := make([]string, 0)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin mail page save: %w", err)
	}
	defer tx.Rollback()
	for _, message := range messages {
		if message.ID == "" {
			return errors.New("Graph message is missing an immutable ID")
		}
		if message.Removed != nil {
			if _, err := tx.ExecContext(ctx, `
				UPDATE messages SET remote_deleted = 1, body_text = NULL, body_cached_at_utc = NULL,
					updated_at_utc = ? WHERE account_id = ? AND immutable_id = ? AND folder_id = ?
					AND hidden_from_inbox = 0
			`, formatTime(now), accountID, message.ID, folder.id); err != nil {
				return fmt.Errorf("save message tombstone: %w", err)
			}
			continue
		}
		receivedAt, err := time.Parse(time.RFC3339Nano, message.ReceivedDateTime)
		if err != nil {
			return fmt.Errorf("parse message received time: %w", err)
		}
		publicID, err := randomMessageID(s.random)
		if err != nil {
			return err
		}
		var existed bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(SELECT 1 FROM messages WHERE account_id = ? AND immutable_id = ?)
		`, accountID, message.ID).Scan(&existed); err != nil {
			return fmt.Errorf("check existing message: %w", err)
		}
		var body any
		var bodyCachedAt any
		truncated := false
		cacheBody := !metadataOnly
		if cacheBody {
			plain, err := plainTextBody(message.Body.ContentType, message.Body.Content)
			if err != nil {
				return err
			}
			plain, truncated = truncateUTF8(plain, bodyLimit)
			body = plain
			bodyCachedAt = formatTime(now)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO messages (
				public_id, account_id, folder_id, immutable_id, internet_message_id,
				subject, sender_name, sender_address, received_at_utc, is_read, is_flagged,
				body_text, body_cached_at_utc, body_truncated, created_at_utc, updated_at_utc
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(account_id, immutable_id) DO UPDATE SET
				folder_id = excluded.folder_id,
				internet_message_id = excluded.internet_message_id,
				subject = excluded.subject,
				sender_name = excluded.sender_name,
				sender_address = excluded.sender_address,
				received_at_utc = excluded.received_at_utc,
				is_read = excluded.is_read,
				is_flagged = excluded.is_flagged,
				body_text = CASE WHEN ? THEN excluded.body_text ELSE messages.body_text END,
				body_cached_at_utc = CASE WHEN ? THEN excluded.body_cached_at_utc ELSE messages.body_cached_at_utc END,
				body_truncated = CASE WHEN ? THEN excluded.body_truncated ELSE messages.body_truncated END,
				remote_deleted = 0,
				updated_at_utc = excluded.updated_at_utc
		`, publicID, accountID, folder.id, message.ID, nullableString(message.InternetMessageID),
			message.Subject, message.From.EmailAddress.Name, strings.ToLower(message.From.EmailAddress.Address),
			formatTime(receivedAt), message.IsRead, strings.EqualFold(message.Flag.Status, "flagged"),
			body, bodyCachedAt, truncated, formatTime(now), formatTime(now),
			cacheBody, cacheBody, cacheBody); err != nil {
			return fmt.Errorf("save message: %w", err)
		}
		var messageID int64
		var storedPublicID string
		var input ClassificationInput
		if err := tx.QueryRowContext(ctx, `
			SELECT m.id, m.public_id, m.subject, m.sender_address, COALESCE(m.body_text, ''), f.well_known_name,
				m.is_flagged, a.cleanup_protected
			FROM messages m JOIN folders f ON f.id = m.folder_id JOIN accounts a ON a.id = m.account_id
			WHERE m.account_id = ? AND m.immutable_id = ?
		`, accountID, message.ID).Scan(&messageID, &storedPublicID, &input.Subject, &input.SenderAddress, &input.Body,
			&input.Folder, &input.Flagged, &input.AccountLocked); err != nil {
			return fmt.Errorf("load saved message classification input: %w", err)
		}
		if err := s.applyClassificationTx(ctx, tx, messageID, input, rules); err != nil {
			return err
		}
		if !existed {
			newMessageIDs = append(newMessageIDs, storedPublicID)
		}
	}
	if finalDelta != "" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE sync_cursors SET delta_link = ?, last_success_at_utc = ?, updated_at_utc = ?
			WHERE folder_id = ?
		`, finalDelta, formatTime(now), formatTime(now), folder.id); err != nil {
			return fmt.Errorf("save folder delta cursor: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE folders SET sync_status = 'active', last_synced_at_utc = ?, last_error = NULL,
				updated_at_utc = ? WHERE id = ?
		`, formatTime(now), formatTime(now), folder.id); err != nil {
			return fmt.Errorf("record folder sync success: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit mail page: %w", err)
	}
	if s.notifier != nil {
		for _, publicID := range newMessageIDs {
			_ = s.notifier.EnqueueMessage(ctx, publicID)
		}
	}
	return nil
}

func (s *Service) resetCursor(ctx context.Context, folderID int64) error {
	now := s.now().UTC()
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE sync_cursors SET delta_link = NULL, initial_window_start_utc = ?, updated_at_utc = ?
		WHERE folder_id = ?
	`, formatTime(now.Add(-time.Duration(settings.InitialSyncDays)*24*time.Hour)), formatTime(now), folderID); err != nil {
		return fmt.Errorf("reset expired delta cursor: %w", err)
	}
	return nil
}

func (s *Service) recordSyncFailure(ctx context.Context, accountID, folderID int64, syncErr error) {
	if errors.Is(syncErr, context.Canceled) {
		return
	}
	now := s.now().UTC()
	code := syncErrorCode(syncErr)
	retryAt := now.Add(syncFailureBackoff(accountID, s.syncFailureCount(ctx, accountID)+1))
	var graphErr *GraphError
	if errors.As(syncErr, &graphErr) && graphErr.RetryAt != nil {
		retryAt = graphErr.RetryAt.UTC()
	}
	_, _ = s.db.ExecContext(ctx, `
		UPDATE folders SET sync_status = ?, last_error = ?, updated_at_utc = ? WHERE id = ?
	`, folderFailureStatus(syncErr), code, formatTime(now), folderID)
	_, _ = s.db.ExecContext(ctx, `
		UPDATE accounts SET last_sync_error = ?, sync_failures = sync_failures + 1,
			sync_next_retry_at_utc = ?, sync_backlog = 1,
			status = CASE WHEN sync_failures + 1 >= 3 AND status = 'active' THEN 'degraded' ELSE status END,
			updated_at_utc = ? WHERE id = ? AND status != 'disabled'
	`, code, formatTime(retryAt), formatTime(now), accountID)
	if s.notifier != nil && (code == "reauth_required" || s.syncFailureCount(ctx, accountID) >= 3) {
		_ = s.notifier.EnqueueSystem(ctx, "mail_sync."+code, "邮箱同步持续失败，请在账号管理中检查授权状态。")
	}
}

func (s *Service) syncFailureCount(ctx context.Context, accountID int64) int {
	var count int
	_ = s.db.QueryRowContext(ctx, "SELECT sync_failures FROM accounts WHERE id = ?", accountID).Scan(&count)
	return count
}

func syncErrorCode(err error) string {
	var graphErr *GraphError
	if errors.As(err, &graphErr) {
		if graphErr.Code != "" {
			return graphErr.Code
		}
		return fmt.Sprintf("graph_http_%d", graphErr.Status)
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "sync_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "sync_timeout"
	case errors.Is(err, accounts.ErrReauthorizationRequired):
		return "reauth_required"
	case errors.Is(err, accounts.ErrOAuthConfiguration):
		return "oauth_configuration"
	case strings.HasPrefix(err.Error(), "request Microsoft Graph"):
		return "graph_network"
	case strings.HasPrefix(err.Error(), "decode Microsoft Graph response"):
		return "graph_response_invalid"
	case strings.HasPrefix(err.Error(), "parse message received time"), strings.Contains(err.Error(), "missing an immutable ID"):
		return "graph_message_invalid"
	case strings.Contains(err.Error(), "database is locked"), strings.Contains(err.Error(), "SQLITE_BUSY"):
		return "database_busy"
	default:
		return "sync_failed"
	}
}

func folderFailureStatus(err error) string {
	var graphErr *GraphError
	if errors.As(err, &graphErr) && graphErr.Status == http.StatusGone {
		return "resync_required"
	}
	return "error"
}

func syncFailureBackoff(accountID int64, failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	shift := failures - 1
	if shift > 5 {
		shift = 5
	}
	base := 30 * time.Second * time.Duration(1<<shift)
	if base > 15*time.Minute {
		base = 15 * time.Minute
	}
	value := sha256.Sum256([]byte(fmt.Sprintf("sync-retry:%d:%d", accountID, failures)))
	jitter := time.Duration(uint16(value[0])<<8|uint16(value[1])) % (15 * time.Second)
	return base + jitter
}

func plainTextBody(contentType, content string) (string, error) {
	if !strings.EqualFold(contentType, "html") {
		return strings.TrimSpace(content), nil
	}
	document, err := xhtml.Parse(strings.NewReader(content))
	if err != nil {
		return "", fmt.Errorf("parse message HTML: %w", err)
	}
	var builder strings.Builder
	var visit func(*xhtml.Node, bool)
	visit = func(node *xhtml.Node, ignored bool) {
		if node.Type == xhtml.ElementNode {
			switch strings.ToLower(node.Data) {
			case "script", "style", "head", "noscript":
				ignored = true
			case "br", "p", "div", "li", "tr":
				if builder.Len() > 0 {
					builder.WriteByte('\n')
				}
			}
		}
		if node.Type == xhtml.TextNode && !ignored {
			builder.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child, ignored)
		}
	}
	visit(document, false)
	return strings.TrimSpace(html.UnescapeString(builder.String())), nil
}

func truncateUTF8(value string, maximum int) (string, bool) {
	if len(value) <= maximum {
		return value, false
	}
	value = value[:maximum]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value, true
}

func randomMessageID(random io.Reader) (string, error) {
	value := make([]byte, 18)
	if _, err := io.ReadFull(random, value); err != nil {
		return "", fmt.Errorf("generate message public id: %w", err)
	}
	return "msg_" + base64.RawURLEncoding.EncodeToString(value), nil
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
