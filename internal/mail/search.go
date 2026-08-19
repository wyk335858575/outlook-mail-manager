package mail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	defaultMessageLimit = 75
	maxMessageLimit     = 200
)

type MessageFilter struct {
	Query            string
	Account          string
	Group            string
	Tag              string
	Category         Category
	Folder           string
	Sender           string
	PersonalOnly     bool
	Since            *time.Time
	Until            *time.Time
	AccountScope     []string
	CursorReceivedAt *time.Time
	CursorPublicID   string
	Unread           *bool
	Limit            int
}

type MessageSummary struct {
	PublicID             string     `json:"public_id"`
	AccountPublicID      string     `json:"account_public_id"`
	AccountName          string     `json:"account_name"`
	AccountAddress       string     `json:"account_address"`
	Folder               string     `json:"folder"`
	FolderName           string     `json:"folder_name"`
	Subject              string     `json:"subject"`
	SenderName           string     `json:"sender_name"`
	SenderAddress        string     `json:"sender_address"`
	ReceivedAt           time.Time  `json:"received_at"`
	Unread               bool       `json:"unread"`
	Flagged              bool       `json:"flagged"`
	BodyPreview          string     `json:"body_preview"`
	BodyTruncated        bool       `json:"body_truncated"`
	SyncedAt             *time.Time `json:"synced_at,omitempty"`
	Category             Category   `json:"category"`
	ClassificationReason string     `json:"classification_reason"`
	CleanupProtected     bool       `json:"cleanup_protected"`
}

type MessageDetail struct {
	MessageSummary
	BodyText     string     `json:"body_text"`
	BodyCachedAt *time.Time `json:"body_cached_at,omitempty"`
}

type Attachment struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	Inline      bool   `json:"inline"`
}

type AttachmentDownload struct {
	Attachment
	Body io.ReadCloser
}

func (s *Service) SearchMessages(ctx context.Context, filter MessageFilter) ([]MessageSummary, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultMessageLimit
	}
	if limit > maxMessageLimit {
		limit = maxMessageLimit
	}

	query := `
		SELECT m.public_id, a.public_id,
			COALESCE(NULLIF(a.display_name, ''), NULLIF(a.primary_email, ''), a.imported_email),
			COALESCE(NULLIF(a.primary_email, ''), a.imported_email),
			f.well_known_name, f.display_name, m.subject, m.sender_name, m.sender_address,
			m.received_at_utc, m.is_read, m.is_flagged,
			COALESCE(substr(m.body_text, 1, 180), ''), m.body_truncated, f.last_synced_at_utc,
			m.category, m.classification_reason, m.cleanup_protected
		FROM messages m
		JOIN accounts a ON a.id = m.account_id
		JOIN folders f ON f.id = m.folder_id
	`
	conditions := []string{"m.remote_deleted = 0", "m.hidden_from_inbox = 0"}
	arguments := make([]any, 0, 10)
	if search := buildFTSQuery(filter.Query); search != "" {
		conditions = append(conditions, "m.id IN (SELECT rowid FROM message_fts WHERE message_fts MATCH ?)")
		arguments = append(arguments, search)
	}
	if account := strings.TrimSpace(filter.Account); account != "" {
		conditions = append(conditions, "a.public_id = ?")
		arguments = append(arguments, account)
	}
	if len(filter.AccountScope) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(filter.AccountScope)), ",")
		conditions = append(conditions, "a.public_id IN ("+placeholders+")")
		for _, publicID := range filter.AccountScope {
			arguments = append(arguments, publicID)
		}
	}
	if group := strings.TrimSpace(filter.Group); group != "" {
		conditions = append(conditions, `EXISTS (
			SELECT 1 FROM account_group_members gm JOIN account_groups g ON g.id = gm.group_id
			WHERE gm.account_id = a.id AND g.name = ? COLLATE NOCASE
		)`)
		arguments = append(arguments, group)
	}
	if tag := strings.TrimSpace(filter.Tag); tag != "" {
		conditions = append(conditions, `EXISTS (
			SELECT 1 FROM account_tag_members tm JOIN account_tags t ON t.id = tm.tag_id
			WHERE tm.account_id = a.id AND t.name = ? COLLATE NOCASE
		)`)
		arguments = append(arguments, tag)
	}
	if filter.PersonalOnly {
		conditions = append(conditions, `EXISTS (
			SELECT 1 FROM personal_inbox_rules r
			WHERE r.enabled = 1
				AND (json_array_length(r.account_public_ids_json) = 0 OR EXISTS (
					SELECT 1 FROM json_each(r.account_public_ids_json) rule_account
					WHERE LOWER(TRIM(CAST(rule_account.value AS TEXT))) = LOWER(a.public_id)
				))
				AND (json_array_length(r.group_names_json) = 0 OR EXISTS (
					SELECT 1 FROM json_each(r.group_names_json) rule_group
					JOIN account_groups personal_group
						ON personal_group.name = CAST(rule_group.value AS TEXT) COLLATE NOCASE
					JOIN account_group_members personal_group_member
						ON personal_group_member.group_id = personal_group.id
					WHERE personal_group_member.account_id = a.id
				))
				AND (json_array_length(r.tag_names_json) = 0 OR EXISTS (
					SELECT 1 FROM json_each(r.tag_names_json) rule_tag
					JOIN account_tags personal_tag
						ON personal_tag.name = CAST(rule_tag.value AS TEXT) COLLATE NOCASE
					JOIN account_tag_members personal_tag_member
						ON personal_tag_member.tag_id = personal_tag.id
					WHERE personal_tag_member.account_id = a.id
				))
				AND (json_array_length(r.categories_json) = 0 OR EXISTS (
					SELECT 1 FROM json_each(r.categories_json) rule_category
					WHERE LOWER(TRIM(CAST(rule_category.value AS TEXT))) = LOWER(m.category)
				))
				AND (TRIM(r.sender_address) = '' OR LOWER(TRIM(r.sender_address)) = LOWER(TRIM(m.sender_address)))
				AND (TRIM(r.sender_domain) = '' OR (
					INSTR(m.sender_address, '@') > 0 AND
					LOWER(TRIM(SUBSTR(m.sender_address, INSTR(m.sender_address, '@') + 1))) = LOWER(TRIM(r.sender_domain))
				))
				AND (json_array_length(r.subject_keywords_json) = 0 OR EXISTS (
					SELECT 1 FROM json_each(r.subject_keywords_json) rule_keyword
					WHERE INSTR(LOWER(m.subject), LOWER(TRIM(CAST(rule_keyword.value AS TEXT)))) > 0
				))
				AND (r.require_otp = 0 OR COALESCE(m.verification_code, '') <> '')
		)`)
	}
	if filter.Category != "" {
		if !validCategory(filter.Category) {
			return nil, ErrInvalidCategory
		}
		conditions = append(conditions, "m.category = ?")
		arguments = append(arguments, filter.Category)
	}
	if folder := strings.TrimSpace(filter.Folder); folder != "" {
		conditions = append(conditions, "f.well_known_name = ?")
		arguments = append(arguments, folder)
	}
	if filter.Unread != nil {
		conditions = append(conditions, "m.is_read = ?")
		arguments = append(arguments, !*filter.Unread)
	}
	if sender := strings.ToLower(strings.TrimSpace(filter.Sender)); sender != "" {
		conditions = append(conditions, "(LOWER(m.sender_address) = ? OR LOWER(m.sender_name) LIKE ? ESCAPE '\\')")
		arguments = append(arguments, sender, "%"+escapeLike(sender)+"%")
	}
	if filter.Since != nil {
		conditions = append(conditions, "m.received_at_utc >= ?")
		arguments = append(arguments, formatTime(filter.Since.UTC()))
	}
	if filter.Until != nil {
		conditions = append(conditions, "m.received_at_utc <= ?")
		arguments = append(arguments, formatTime(filter.Until.UTC()))
	}
	if filter.CursorReceivedAt != nil && strings.TrimSpace(filter.CursorPublicID) != "" {
		conditions = append(conditions, "(m.received_at_utc < ? OR (m.received_at_utc = ? AND m.public_id < ?))")
		cursorTime := formatTime(filter.CursorReceivedAt.UTC())
		arguments = append(arguments, cursorTime, cursorTime, strings.TrimSpace(filter.CursorPublicID))
	}
	query += " WHERE " + strings.Join(conditions, " AND ") + " ORDER BY m.received_at_utc DESC, m.public_id DESC LIMIT ?"
	arguments = append(arguments, limit)

	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	defer rows.Close()
	items := make([]MessageSummary, 0)
	for rows.Next() {
		item, err := scanMessageSummary(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}
	return items, nil
}

func (s *Service) GetMessage(ctx context.Context, publicID string) (MessageDetail, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT m.public_id, a.public_id,
			COALESCE(NULLIF(a.display_name, ''), NULLIF(a.primary_email, ''), a.imported_email),
			COALESCE(NULLIF(a.primary_email, ''), a.imported_email),
			f.well_known_name, f.display_name, m.subject, m.sender_name, m.sender_address,
			m.received_at_utc, m.is_read, m.is_flagged,
			COALESCE(substr(m.body_text, 1, 180), ''), m.body_truncated, f.last_synced_at_utc,
			m.category, m.classification_reason, m.cleanup_protected,
			COALESCE(m.body_text, ''), m.body_cached_at_utc
		FROM messages m
		JOIN accounts a ON a.id = m.account_id
		JOIN folders f ON f.id = m.folder_id
		WHERE m.public_id = ? AND m.remote_deleted = 0 AND m.hidden_from_inbox = 0
	`, strings.TrimSpace(publicID))
	var detail MessageDetail
	var receivedValue string
	var syncedValue, cachedValue sql.NullString
	var isRead bool
	err := row.Scan(
		&detail.PublicID, &detail.AccountPublicID, &detail.AccountName, &detail.AccountAddress,
		&detail.Folder, &detail.FolderName, &detail.Subject, &detail.SenderName, &detail.SenderAddress,
		&receivedValue, &isRead, &detail.Flagged, &detail.BodyPreview, &detail.BodyTruncated, &syncedValue,
		&detail.Category, &detail.ClassificationReason, &detail.CleanupProtected,
		&detail.BodyText, &cachedValue,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return MessageDetail{}, ErrMessageNotFound
	}
	if err != nil {
		return MessageDetail{}, fmt.Errorf("load message: %w", err)
	}
	detail.Unread = !isRead
	detail.ReceivedAt, err = parseStoredTime(receivedValue)
	if err != nil {
		return MessageDetail{}, err
	}
	detail.SyncedAt, err = parseNullableStoredTime(syncedValue)
	if err != nil {
		return MessageDetail{}, err
	}
	detail.BodyCachedAt, err = parseNullableStoredTime(cachedValue)
	if err != nil {
		return MessageDetail{}, err
	}
	return detail, nil
}

func (s *Service) MarkMessageRead(ctx context.Context, publicID string) error {
	publicID = strings.TrimSpace(publicID)
	var accountID int64
	err := s.db.QueryRowContext(ctx, `
		SELECT account_id FROM messages WHERE public_id = ? AND remote_deleted = 0
	`, publicID).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrMessageNotFound
	}
	if err != nil {
		return fmt.Errorf("load message account: %w", err)
	}

	lock := s.accountLock(accountID)
	lock.Lock()
	defer lock.Unlock()

	var immutableID string
	var isRead bool
	err = s.db.QueryRowContext(ctx, `
		SELECT immutable_id, is_read FROM messages
		WHERE public_id = ? AND account_id = ? AND remote_deleted = 0
	`, publicID, accountID).Scan(&immutableID, &isRead)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrMessageNotFound
	}
	if err != nil {
		return fmt.Errorf("load message read state: %w", err)
	}
	if isRead {
		return nil
	}
	if err := s.graph.markMessageRead(ctx, accountID, immutableID); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE messages SET is_read = 1, updated_at_utc = ?
		WHERE public_id = ? AND account_id = ? AND remote_deleted = 0
	`, formatTime(s.now().UTC()), publicID, accountID)
	if err != nil {
		return fmt.Errorf("record message read state: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read message update result: %w", err)
	}
	if rows != 1 {
		return ErrMessageNotFound
	}
	return nil
}

func (s *Service) GetMessageHTML(ctx context.Context, publicID string, loadRemoteImages bool) (string, error) {
	var accountID int64
	var immutableID string
	err := s.db.QueryRowContext(ctx, `
		SELECT account_id, immutable_id FROM messages
		WHERE public_id = ? AND remote_deleted = 0 AND hidden_from_inbox = 0
	`, strings.TrimSpace(publicID)).Scan(&accountID, &immutableID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrMessageNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load message HTML source: %w", err)
	}
	raw, err := s.graph.getMessageHTML(ctx, accountID, immutableID)
	if err != nil {
		return "", err
	}
	return sanitizeMessageHTML(raw, loadRemoteImages)
}

func (s *Service) ListAttachments(ctx context.Context, publicID string) ([]Attachment, error) {
	accountID, immutableID, err := s.messageGraphIdentity(ctx, publicID)
	if err != nil {
		return nil, err
	}
	items, err := s.graph.listMessageAttachments(ctx, accountID, immutableID)
	if err != nil {
		return nil, err
	}
	result := make([]Attachment, 0, len(items))
	for _, item := range items {
		if item.ID == "" || strings.EqualFold(item.ODataType, "#microsoft.graph.referenceAttachment") {
			continue
		}
		result = append(result, Attachment{
			ID: item.ID, Name: item.Name, ContentType: item.ContentType, Size: item.Size, Inline: item.Inline,
		})
	}
	return result, nil
}

func (s *Service) OpenAttachment(ctx context.Context, publicID, attachmentID string) (AttachmentDownload, error) {
	attachmentID = strings.TrimSpace(attachmentID)
	if attachmentID == "" {
		return AttachmentDownload{}, ErrMessageNotFound
	}
	accountID, immutableID, err := s.messageGraphIdentity(ctx, publicID)
	if err != nil {
		return AttachmentDownload{}, err
	}
	items, err := s.graph.listMessageAttachments(ctx, accountID, immutableID)
	if err != nil {
		return AttachmentDownload{}, err
	}
	var attachment Attachment
	found := false
	for _, item := range items {
		if item.ID == attachmentID && !strings.EqualFold(item.ODataType, "#microsoft.graph.referenceAttachment") {
			attachment = Attachment{ID: item.ID, Name: item.Name, ContentType: item.ContentType, Size: item.Size, Inline: item.Inline}
			found = true
			break
		}
	}
	if !found {
		return AttachmentDownload{}, ErrMessageNotFound
	}
	response, err := s.graph.openMessageAttachment(ctx, accountID, immutableID, attachmentID)
	if err != nil {
		return AttachmentDownload{}, err
	}
	return AttachmentDownload{Attachment: attachment, Body: response.Body}, nil
}

func (s *Service) messageGraphIdentity(ctx context.Context, publicID string) (int64, string, error) {
	var accountID int64
	var immutableID string
	err := s.db.QueryRowContext(ctx, `
		SELECT account_id, immutable_id FROM messages
		WHERE public_id = ? AND remote_deleted = 0 AND hidden_from_inbox = 0
	`, strings.TrimSpace(publicID)).Scan(&accountID, &immutableID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", ErrMessageNotFound
	}
	if err != nil {
		return 0, "", fmt.Errorf("load message Graph identity: %w", err)
	}
	return accountID, immutableID, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanMessageSummary(scanner rowScanner) (MessageSummary, error) {
	var item MessageSummary
	var receivedValue string
	var syncedValue sql.NullString
	var isRead bool
	if err := scanner.Scan(
		&item.PublicID, &item.AccountPublicID, &item.AccountName, &item.AccountAddress,
		&item.Folder, &item.FolderName, &item.Subject, &item.SenderName, &item.SenderAddress,
		&receivedValue, &isRead, &item.Flagged, &item.BodyPreview, &item.BodyTruncated, &syncedValue,
		&item.Category, &item.ClassificationReason, &item.CleanupProtected,
	); err != nil {
		return MessageSummary{}, fmt.Errorf("scan message: %w", err)
	}
	item.Unread = !isRead
	var err error
	item.ReceivedAt, err = parseStoredTime(receivedValue)
	if err != nil {
		return MessageSummary{}, err
	}
	item.SyncedAt, err = parseNullableStoredTime(syncedValue)
	if err != nil {
		return MessageSummary{}, err
	}
	return item, nil
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "%", "\\%")
	return strings.ReplaceAll(value, "_", "\\_")
}

func buildFTSQuery(value string) string {
	fields := strings.Fields(strings.TrimSpace(value))
	quoted := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.ReplaceAll(field, `"`, `""`)
		quoted = append(quoted, `"`+field+`"`)
	}
	return strings.Join(quoted, " AND ")
}

func parseStoredTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse stored message time: %w", err)
	}
	return parsed.UTC(), nil
}

func parseNullableStoredTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil, nil
	}
	parsed, err := parseStoredTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
