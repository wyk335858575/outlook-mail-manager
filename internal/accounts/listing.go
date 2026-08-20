package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidAccountList = errors.New("invalid account list filter")
	ErrAccountBatchLimit  = errors.New("account batch limit exceeded")
)

type AccountListOptions struct {
	Query    string
	Status   string
	Page     int
	PageSize int
}

type AccountList struct {
	Accounts         []Account      `json:"accounts"`
	Total            int            `json:"total"`
	Page             int            `json:"page"`
	PageSize         int            `json:"page_size"`
	StatusCounts     map[string]int `json:"status_counts"`
	AuthMethodCounts map[string]int `json:"auth_method_counts"`
}

func (s *Service) List(ctx context.Context, status string) ([]Account, error) {
	result, err := s.ListAccounts(ctx, AccountListOptions{Status: status})
	return result.Accounts, err
}

func (s *Service) ListAccounts(ctx context.Context, options AccountListOptions) (AccountList, error) {
	options.Query = strings.TrimSpace(options.Query)
	options.Status = strings.TrimSpace(options.Status)
	if options.Status != "" && !validStatus(options.Status) {
		return AccountList{}, ErrInvalidAccountList
	}
	if options.PageSize < 0 || options.PageSize > 100 || options.PageSize > 0 && options.Page < 1 {
		return AccountList{}, ErrInvalidAccountList
	}

	searchCondition, searchArgs := accountSearchCondition(options.Query)
	counts := map[string]int{"pending": 0, "active": 0, "degraded": 0, "reauth_required": 0, "disabled": 0}
	countRows, err := s.db.QueryContext(ctx, `SELECT a.status, COUNT(*) FROM accounts a`+searchCondition+` GROUP BY a.status`, searchArgs...)
	if err != nil {
		return AccountList{}, fmt.Errorf("count account statuses: %w", err)
	}
	for countRows.Next() {
		var status string
		var count int
		if err := countRows.Scan(&status, &count); err != nil {
			countRows.Close()
			return AccountList{}, fmt.Errorf("scan account status count: %w", err)
		}
		counts[status] = count
	}
	if err := countRows.Close(); err != nil {
		return AccountList{}, fmt.Errorf("close account status counts: %w", err)
	}
	authMethodCounts := map[string]int{string(AuthMethodWeb): 0, string(AuthMethodOAuth): 0}
	authCondition, authArgs := accountListCondition(options.Query, options.Status)
	authRows, err := s.db.QueryContext(ctx, `SELECT a.auth_method, COUNT(*) FROM accounts a`+authCondition+` GROUP BY a.auth_method`, authArgs...)
	if err != nil {
		return AccountList{}, fmt.Errorf("count account authorization methods: %w", err)
	}
	for authRows.Next() {
		var method string
		var count int
		if err := authRows.Scan(&method, &count); err != nil {
			authRows.Close()
			return AccountList{}, fmt.Errorf("scan account authorization method count: %w", err)
		}
		authMethodCounts[method] = count
	}
	if err := authRows.Close(); err != nil {
		return AccountList{}, fmt.Errorf("close account authorization method counts: %w", err)
	}

	condition, args := accountListCondition(options.Query, options.Status)
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts a`+condition, args...).Scan(&total); err != nil {
		return AccountList{}, fmt.Errorf("count accounts: %w", err)
	}

	query := accountSelect + condition + accountOrder
	if options.PageSize > 0 {
		query += " LIMIT ? OFFSET ?"
		args = append(args, options.PageSize, (options.Page-1)*options.PageSize)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return AccountList{}, fmt.Errorf("list accounts: %w", err)
	}
	items, err := s.scanAccounts(rows)
	if err != nil {
		return AccountList{}, err
	}
	page := options.Page
	if page == 0 {
		page = 1
	}
	return AccountList{Accounts: items, Total: total, Page: page, PageSize: options.PageSize, StatusCounts: counts, AuthMethodCounts: authMethodCounts}, nil
}

func (s *Service) SelectAccountIDs(ctx context.Context, query, status string) ([]string, error) {
	condition, args := accountListCondition(strings.TrimSpace(query), strings.TrimSpace(status))
	if status != "" && !validStatus(strings.TrimSpace(status)) {
		return nil, ErrInvalidAccountList
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.public_id FROM accounts a`+condition+accountOrder+` LIMIT ?`, append(args, maxImportRows+1)...)
	if err != nil {
		return nil, fmt.Errorf("select account ids: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var publicID string
		if err := rows.Scan(&publicID); err != nil {
			return nil, fmt.Errorf("scan account id: %w", err)
		}
		ids = append(ids, publicID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate account ids: %w", err)
	}
	if len(ids) > maxImportRows {
		return nil, ErrAccountBatchLimit
	}
	return ids, nil
}

const accountSelect = `
	SELECT a.id, a.public_id, a.imported_email, a.auth_method, COALESCE(a.primary_email, ''),
		COALESCE(a.display_name, ''), a.notes, a.status, COALESCE(a.reauth_reason, ''),
		COALESCE(a.last_oauth_error, ''), a.consecutive_failures,
		a.next_retry_at_utc, t.last_refresh_success_at_utc, a.last_graph_success_at_utc,
		a.last_sync_success_at_utc, COALESCE(a.last_sync_error, ''),
		a.sync_failures, a.sync_next_retry_at_utc, a.sync_backlog,
		a.cleanup_protected,
		COALESCE((SELECT group_concat(g.name, char(31)) FROM account_groups g
			JOIN account_group_members gm ON gm.group_id = g.id WHERE gm.account_id = a.id), ''),
		COALESCE((SELECT group_concat(tg.name, char(31)) FROM account_tags tg
			JOIN account_tag_members tm ON tm.tag_id = tg.id WHERE tm.account_id = a.id), '')
	FROM accounts a
	LEFT JOIN account_tokens t ON t.account_id = a.id`

const accountOrder = ` ORDER BY CASE a.status
	WHEN 'reauth_required' THEN 0 WHEN 'pending' THEN 1 WHEN 'degraded' THEN 2
	WHEN 'active' THEN 3 ELSE 4 END, a.created_at_utc, a.id`

func accountListCondition(query, status string) (string, []any) {
	condition, args := accountSearchCondition(query)
	if status == "" {
		return condition, args
	}
	if condition == "" {
		condition = " WHERE a.status = ?"
	} else {
		condition += " AND a.status = ?"
	}
	return condition, append(args, status)
}

func accountSearchCondition(query string) (string, []any) {
	if query == "" {
		return "", nil
	}
	value := "%" + escapeLike(query) + "%"
	return ` WHERE (
		a.imported_email LIKE ? ESCAPE '\' COLLATE NOCASE OR
		COALESCE(a.primary_email, '') LIKE ? ESCAPE '\' COLLATE NOCASE OR
		COALESCE(a.display_name, '') LIKE ? ESCAPE '\' COLLATE NOCASE OR
		a.notes LIKE ? ESCAPE '\' COLLATE NOCASE OR
		EXISTS (SELECT 1 FROM account_group_members gm JOIN account_groups g ON g.id = gm.group_id
			WHERE gm.account_id = a.id AND g.name LIKE ? ESCAPE '\' COLLATE NOCASE) OR
		EXISTS (SELECT 1 FROM account_tag_members tm JOIN account_tags tg ON tg.id = tm.tag_id
			WHERE tm.account_id = a.id AND tg.name LIKE ? ESCAPE '\' COLLATE NOCASE)
	)`, []any{value, value, value, value, value, value}
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}

func (s *Service) scanAccounts(rows *sql.Rows) ([]Account, error) {
	defer rows.Close()
	items := make([]Account, 0)
	for rows.Next() {
		var account Account
		var accountID int64
		var nextRetry, refreshSuccess, graphSuccess, syncSuccess, syncNextRetry sql.NullString
		var groups, tags string
		if err := rows.Scan(
			&accountID, &account.PublicID, &account.ImportedEmail, &account.AuthMethod, &account.PrimaryEmail,
			&account.DisplayName, &account.Notes, &account.Status, &account.ReauthReason,
			&account.LastOAuthError, &account.ConsecutiveFailures,
			&nextRetry, &refreshSuccess, &graphSuccess, &syncSuccess, &account.LastSyncError,
			&account.SyncFailures, &syncNextRetry, &account.SyncBacklog, &account.CleanupProtected,
			&groups, &tags,
		); err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		account.NextRetryAt = parseOptionalTime(nextRetry)
		account.LastRefreshSuccessAt = parseOptionalTime(refreshSuccess)
		account.LastGraphSuccessAt = parseOptionalTime(graphSuccess)
		account.LastSyncSuccessAt = parseOptionalTime(syncSuccess)
		account.SyncNextRetryAt = parseOptionalTime(syncNextRetry)
		account.Groups = splitNames(groups)
		account.Tags = splitNames(tags)
		account.AuthorizationInProgress = s.authorizationInProgress(accountID)
		items = append(items, account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate accounts: %w", err)
	}
	return items, nil
}
