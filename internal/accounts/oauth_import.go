package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const oauthImportConcurrency = 4

type OAuthImportInput struct {
	Email        string `json:"email"`
	ClientID     string `json:"client_id"`
	RefreshToken string `json:"refresh_token"`
}

type OAuthImportJob struct {
	ID          string            `json:"id"`
	State       string            `json:"state"`
	Total       int               `json:"total"`
	Processed   int               `json:"processed"`
	Created     int               `json:"created"`
	Updated     int               `json:"updated"`
	Skipped     int               `json:"skipped"`
	Failed      int               `json:"failed"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	CompletedAt *time.Time        `json:"completed_at,omitempty"`
	Items       []OAuthImportItem `json:"items"`
}

type OAuthImportItem struct {
	Row       int    `json:"row"`
	Email     string `json:"email"`
	State     string `json:"state"`
	ErrorCode string `json:"error_code,omitempty"`
	Message   string `json:"message,omitempty"`
}

type AccountUpdate struct {
	ImportedEmail string   `json:"imported_email"`
	Notes         string   `json:"notes"`
	Groups        []string `json:"groups"`
	Tags          []string `json:"tags"`
}

func (s *Service) StartOAuthImport(ctx context.Context, inputs []OAuthImportInput, overwrite bool) (OAuthImportJob, error) {
	if len(inputs) == 0 || len(inputs) > maxImportRows {
		return OAuthImportJob{}, invalidImport(fmt.Sprintf("每次需要导入 1 到 %d 个账号", maxImportRows))
	}
	jobID, err := randomPrefixedID("oauth_import_", s.random)
	if err != nil {
		return OAuthImportJob{}, err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OAuthImportJob{}, fmt.Errorf("begin OAuth import: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO oauth_import_jobs (
			public_id, overwrite_existing, state, total_count, created_at_utc, updated_at_utc
		) VALUES (?, ?, 'queued', ?, ?, ?)
	`, jobID, overwrite, len(inputs), formatTime(now), formatTime(now)); err != nil {
		return OAuthImportJob{}, fmt.Errorf("create OAuth import job: %w", err)
	}
	seen := make(map[string]struct{}, len(inputs))
	for index, input := range inputs {
		email, err := normalizeEmail(input.Email)
		if err != nil {
			return OAuthImportJob{}, invalidImport(fmt.Sprintf("第 %d 行的邮箱地址无效", index+1))
		}
		if _, exists := seen[email]; exists {
			return OAuthImportJob{}, invalidImport(fmt.Sprintf("第 %d 行重复了邮箱 %s", index+1, email))
		}
		seen[email] = struct{}{}
		clientID, err := normalizeMicrosoftClientID(input.ClientID)
		if err != nil {
			return OAuthImportJob{}, invalidImport(fmt.Sprintf("第 %d 行的 Client ID 无效", index+1))
		}
		refreshToken := strings.TrimSpace(input.RefreshToken)
		if refreshToken == "" || len(refreshToken) > 8192 {
			return OAuthImportJob{}, invalidImport(fmt.Sprintf("第 %d 行的 refresh token 无效", index+1))
		}
		ciphertext, err := s.keyring.SealString(refreshToken, oauthImportAssociatedData(jobID, index+1))
		if err != nil {
			return OAuthImportJob{}, fmt.Errorf("encrypt OAuth import credential: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO oauth_import_items (
				job_public_id, row_number, email, client_id, refresh_token_ciphertext,
				state, created_at_utc, updated_at_utc
			) VALUES (?, ?, ?, ?, ?, 'queued', ?, ?)
		`, jobID, index+1, email, clientID, ciphertext, formatTime(now), formatTime(now)); err != nil {
			return OAuthImportJob{}, fmt.Errorf("create OAuth import item: %w", err)
		}
	}
	if err := insertAudit(ctx, tx, "account_oauth_import_started", "admin", map[string]any{
		"job": jobID, "total": len(inputs), "overwrite_existing": overwrite,
	}, now); err != nil {
		return OAuthImportJob{}, err
	}
	if err := tx.Commit(); err != nil {
		return OAuthImportJob{}, fmt.Errorf("commit OAuth import job: %w", err)
	}
	go s.processOAuthImport(jobID)
	return s.GetOAuthImport(ctx, jobID)
}

func (s *Service) GetOAuthImport(ctx context.Context, jobID string) (OAuthImportJob, error) {
	var result OAuthImportJob
	var created, updated, completed sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT public_id, state, total_count, processed_count, created_count, updated_count,
			skipped_count, failed_count, created_at_utc, updated_at_utc, completed_at_utc
		FROM oauth_import_jobs WHERE public_id = ?
	`, strings.TrimSpace(jobID)).Scan(&result.ID, &result.State, &result.Total, &result.Processed,
		&result.Created, &result.Updated, &result.Skipped, &result.Failed, &created, &updated, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthImportJob{}, ErrAuthorizationNotFound
	}
	if err != nil {
		return OAuthImportJob{}, fmt.Errorf("load OAuth import job: %w", err)
	}
	result.CreatedAt, _ = time.Parse(time.RFC3339Nano, created.String)
	result.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated.String)
	if completed.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, completed.String)
		if parseErr == nil {
			result.CompletedAt = &value
		}
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT row_number, email, state, COALESCE(error_code, ''), COALESCE(message, '')
		FROM oauth_import_items WHERE job_public_id = ? ORDER BY row_number
	`, result.ID)
	if err != nil {
		return OAuthImportJob{}, fmt.Errorf("list OAuth import results: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item OAuthImportItem
		if err := rows.Scan(&item.Row, &item.Email, &item.State, &item.ErrorCode, &item.Message); err != nil {
			return OAuthImportJob{}, err
		}
		result.Items = append(result.Items, item)
	}
	return result, rows.Err()
}

func (s *Service) processOAuthImport(jobID string) {
	ctx := s.closeCtx
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE oauth_import_jobs SET state = 'running', updated_at_utc = ? WHERE public_id = ? AND state IN ('queued', 'running')`, formatTime(now), jobID)
	if err != nil {
		return
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM oauth_import_items WHERE job_public_id = ? AND state IN ('queued', 'running') ORDER BY row_number`, jobID)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	jobs := make(chan int64)
	var workers sync.WaitGroup
	for range min(oauthImportConcurrency, len(ids)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for id := range jobs {
				s.processOAuthImportItem(ctx, jobID, id)
			}
		}()
	}
	for _, id := range ids {
		select {
		case jobs <- id:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return
		}
	}
	close(jobs)
	workers.Wait()
	s.finishOAuthImport(ctx, jobID)
}

func (s *Service) processOAuthImportItem(ctx context.Context, jobID string, itemID int64) {
	var row int
	var email, clientID, ciphertext string
	var overwrite bool
	err := s.db.QueryRowContext(ctx, `
		SELECT i.row_number, i.email, i.client_id, i.refresh_token_ciphertext, j.overwrite_existing
		FROM oauth_import_items i JOIN oauth_import_jobs j ON j.public_id = i.job_public_id
		WHERE i.id = ? AND i.job_public_id = ?
	`, itemID, jobID).Scan(&row, &email, &clientID, &ciphertext, &overwrite)
	if err != nil {
		return
	}
	now := s.now().UTC()
	_, _ = s.db.ExecContext(ctx, `UPDATE oauth_import_items SET state = 'running', updated_at_utc = ? WHERE id = ?`, formatTime(now), itemID)
	var existingID int64
	var existingHasToken bool
	var existingPublic, existingMicrosoftID, existingStatus string
	err = s.db.QueryRowContext(ctx, `
		SELECT a.id, a.public_id, COALESCE(a.microsoft_user_id, ''), a.status,
			EXISTS(SELECT 1 FROM account_tokens t WHERE t.account_id = a.id)
		FROM accounts a WHERE a.imported_email = ?
	`, email).Scan(&existingID, &existingPublic, &existingMicrosoftID, &existingStatus, &existingHasToken)
	if err == nil && existingStatus == "disabled" {
		s.completeOAuthImportItem(ctx, jobID, itemID, "failed", "account_disabled", "账号已停用，请先启用")
		return
	}
	if err == nil && existingHasToken && !overwrite {
		s.completeOAuthImportItem(ctx, jobID, itemID, "skipped", "", "账号已存在，未勾选覆盖已有授权")
		return
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.completeOAuthImportItem(ctx, jobID, itemID, "failed", "database_error", "无法检查已有账号")
		return
	}
	refreshToken, err := s.keyring.OpenString(ciphertext, oauthImportAssociatedData(jobID, row))
	if err != nil {
		s.completeOAuthImportItem(ctx, jobID, itemID, "failed", "credential_unavailable", "无法读取加密凭据")
		return
	}
	select {
	case s.oauthImportSlots <- struct{}{}:
		defer func() { <-s.oauthImportSlots }()
	case <-ctx.Done():
		return
	}
	token, profile, scopes, err := s.validateImportedCredential(ctx, email, clientID, refreshToken)
	refreshToken = ""
	if err != nil {
		s.completeOAuthImportItem(ctx, jobID, itemID, "failed", credentialErrorCode(err), credentialErrorMessage(err))
		return
	}
	if existingID != 0 {
		if existingMicrosoftID != "" && syntheticMicrosoftProfile(profile) {
			profile.ID = existingMicrosoftID
		}
		if existingMicrosoftID != "" && existingMicrosoftID != profile.ID {
			s.completeOAuthImportItem(ctx, jobID, itemID, "failed", "account_mismatch", "凭据属于另一个 Microsoft 账号")
			return
		}
		if existingMicrosoftID == "" && !strings.EqualFold(email, profileEmail(profile)) {
			s.completeOAuthImportItem(ctx, jobID, itemID, "failed", "email_mismatch", "Microsoft 主邮箱与导入邮箱不一致，请改用设备码授权确认别名")
			return
		}
		if err := s.persistAuthorizationWithClientID(ctx, existingID, existingPublic, token, profile, scopes, clientID, AuthMethodOAuth, false); err != nil {
			s.completeOAuthImportItem(ctx, jobID, itemID, "failed", credentialErrorCode(err), credentialErrorMessage(err))
			return
		}
		s.completeOAuthImportItem(ctx, jobID, itemID, "updated", "", "授权凭据已更新")
		return
	}
	if !strings.EqualFold(email, profileEmail(profile)) {
		s.completeOAuthImportItem(ctx, jobID, itemID, "failed", "email_mismatch", "Microsoft 主邮箱与导入邮箱不一致，请改用设备码授权确认别名")
		return
	}
	publicID, err := randomPublicID(s.random)
	if err != nil {
		s.completeOAuthImportItem(ctx, jobID, itemID, "failed", "internal_error", "无法创建账号")
		return
	}
	insert, err := s.db.ExecContext(ctx, `INSERT INTO accounts (public_id, imported_email, auth_method, status, created_at_utc, updated_at_utc) VALUES (?, ?, ?, 'pending', ?, ?)`, publicID, email, AuthMethodOAuth, formatTime(now), formatTime(now))
	if err != nil {
		s.completeOAuthImportItem(ctx, jobID, itemID, "failed", "account_conflict", "账号已存在或无法创建")
		return
	}
	accountID, _ := insert.LastInsertId()
	if err := s.persistAuthorizationWithClientID(ctx, accountID, publicID, token, profile, scopes, clientID, AuthMethodOAuth, false); err != nil {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM accounts WHERE id = ? AND status = 'pending'`, accountID)
		s.completeOAuthImportItem(ctx, jobID, itemID, "failed", credentialErrorCode(err), credentialErrorMessage(err))
		return
	}
	s.completeOAuthImportItem(ctx, jobID, itemID, "created", "", "账号已创建并完成授权")
}

func (s *Service) validateImportedCredential(ctx context.Context, importedEmail, clientID, refreshToken string) (*oauth2.Token, microsoftProfile, []string, error) {
	token, err := refreshMicrosoftToken(ctx, s.httpClient, s.graphOAuthEndpoint, clientID, refreshToken, s.now)
	if err != nil {
		var retrieveError *oauth2.RetrieveError
		if errors.As(err, &retrieveError) {
			switch retrieveError.ErrorCode {
			case "consent_required", "interaction_required", "invalid_scope":
				return nil, microsoftProfile{}, nil, ErrReauthorizationRequired
			}
		}
		return nil, microsoftProfile{}, nil, err
	}
	if token.AccessToken == "" || token.RefreshToken == "" || token.Expiry.IsZero() {
		return nil, microsoftProfile{}, nil, errors.New("incomplete token")
	}
	scopes := tokenScopes(token)
	if !hasImportedMailScopes(scopes) {
		if hasPOPIMAPScopes(scopes) {
			return nil, microsoftProfile{}, nil, ErrPOPIMAPCredential
		}
		return nil, microsoftProfile{}, nil, ErrReauthorizationRequired
	}
	profile, err := s.fetchProfile(ctx, token.AccessToken)
	if err == nil {
		if hasGraphDefaultScope(scopes) {
			if err := s.verifyMailboxAccess(ctx, token.AccessToken); err != nil {
				return nil, microsoftProfile{}, nil, mailboxPermissionError(err)
			}
		}
		return token, profile, scopes, nil
	}
	if err := s.verifyMailboxAccess(ctx, token.AccessToken); err != nil {
		return nil, microsoftProfile{}, nil, mailboxPermissionError(err)
	}
	return token, importedEmailProfile(importedEmail), scopes, nil
}

func (s *Service) ReplaceOAuthCredentials(ctx context.Context, publicID, clientID, refreshToken string) error {
	clientID, err := normalizeMicrosoftClientID(clientID)
	if err != nil {
		return err
	}
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" || len(refreshToken) > 8192 {
		return invalidImport("refresh token 无效")
	}
	accountID, importedEmail, status, err := s.accountIdentity(ctx, publicID)
	if err != nil {
		return err
	}
	if status == "disabled" {
		return ErrAccountDisabled
	}
	var microsoftID string
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(microsoft_user_id, '') FROM accounts WHERE id = ?`, accountID).Scan(&microsoftID)
	token, profile, scopes, err := s.validateImportedCredential(ctx, importedEmail, clientID, refreshToken)
	refreshToken = ""
	if err != nil {
		return err
	}
	if microsoftID != "" && syntheticMicrosoftProfile(profile) {
		profile.ID = microsoftID
	}
	if microsoftID != "" && microsoftID != profile.ID {
		return errors.New("credential belongs to another Microsoft account")
	}
	if microsoftID == "" && !strings.EqualFold(importedEmail, profileEmail(profile)) {
		return errors.New("Microsoft primary email does not match imported email")
	}
	return s.persistAuthorizationWithClientID(ctx, accountID, publicID, token, profile, scopes, clientID, AuthMethodOAuth, false)
}

func (s *Service) verifyMailboxAccess(ctx context.Context, accessToken string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.graphBaseURL+"/me/mailFolders/inbox/messages?$top=1&$select=id", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return &graphHTTPError{status: response.StatusCode}
	}
	return nil
}

func mailboxPermissionError(err error) error {
	var graphErr *graphHTTPError
	if errors.As(err, &graphErr) && (graphErr.status == http.StatusUnauthorized || graphErr.status == http.StatusForbidden) {
		return ErrReauthorizationRequired
	}
	return err
}

func importedEmailProfile(email string) microsoftProfile {
	normalized := strings.ToLower(strings.TrimSpace(email))
	digest := sha256.Sum256([]byte(normalized))
	return microsoftProfile{
		ID:   "imported-email:" + fmt.Sprintf("%x", digest[:]),
		Mail: normalized, UserPrincipalName: normalized,
	}
}

func syntheticMicrosoftProfile(profile microsoftProfile) bool {
	return strings.HasPrefix(profile.ID, "imported-email:")
}

func (s *Service) UpdateAccount(ctx context.Context, publicID string, input AccountUpdate) (Account, error) {
	email, err := normalizeEmail(input.ImportedEmail)
	if err != nil {
		return Account{}, invalidImport("邮箱地址无效")
	}
	input.Notes = strings.TrimSpace(input.Notes)
	if len(input.Notes) > maxNotesBytes {
		return Account{}, invalidImport("备注过长")
	}
	groups, err := normalizeNames(input.Groups, 80, 20)
	if err != nil {
		return Account{}, invalidImport("分组无效")
	}
	tags, err := normalizeNames(input.Tags, 64, 20)
	if err != nil {
		return Account{}, invalidImport("标签无效")
	}
	accountID, existingEmail, _, err := s.accountIdentity(ctx, strings.TrimSpace(publicID))
	if err != nil {
		return Account{}, err
	}
	var duplicateID int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM accounts WHERE imported_email = ? AND id <> ?`, email, accountID).Scan(&duplicateID)
	if err == nil {
		return Account{}, invalidImport("该导入邮箱已存在")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Account{}, fmt.Errorf("check account email uniqueness: %w", err)
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET imported_email = ?, notes = ?, updated_at_utc = ? WHERE id = ?`, email, input.Notes, formatTime(now), accountID); err != nil {
		return Account{}, fmt.Errorf("update account: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_group_members WHERE account_id = ?`, accountID); err != nil {
		return Account{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_tag_members WHERE account_id = ?`, accountID); err != nil {
		return Account{}, err
	}
	for _, name := range groups {
		if err := attachName(ctx, tx, accountID, name, "account_groups", "account_group_members", "group_id", now); err != nil {
			return Account{}, err
		}
	}
	for _, name := range tags {
		if err := attachName(ctx, tx, accountID, name, "account_tags", "account_tag_members", "tag_id", now); err != nil {
			return Account{}, err
		}
	}
	if err := insertAudit(ctx, tx, "account_updated", "admin", map[string]any{"account": publicID}, now); err != nil {
		return Account{}, err
	}
	if err := tx.Commit(); err != nil {
		return Account{}, err
	}
	if !strings.EqualFold(existingEmail, email) {
		s.invalidateAuthorizationForAccountChange(accountID, existingEmail)
	}
	items, err := s.List(ctx, "")
	if err != nil {
		return Account{}, err
	}
	for _, item := range items {
		if item.PublicID == publicID {
			return item, nil
		}
	}
	return Account{}, ErrAccountNotFound
}

func (s *Service) completeOAuthImportItem(ctx context.Context, jobID string, itemID int64, state, code, message string) {
	now := s.now().UTC()
	_, _ = s.db.ExecContext(ctx, `
		UPDATE oauth_import_items SET state = ?, error_code = NULLIF(?, ''), message = ?,
			refresh_token_ciphertext = NULL, updated_at_utc = ? WHERE id = ?
	`, state, code, message, formatTime(now), itemID)
}

func (s *Service) finishOAuthImport(ctx context.Context, jobID string) {
	now := s.now().UTC()
	_, _ = s.db.ExecContext(ctx, `
		UPDATE oauth_import_jobs SET state = 'completed',
			processed_count = (SELECT COUNT(*) FROM oauth_import_items WHERE job_public_id = ? AND state NOT IN ('queued', 'running')),
			created_count = (SELECT COUNT(*) FROM oauth_import_items WHERE job_public_id = ? AND state = 'created'),
			updated_count = (SELECT COUNT(*) FROM oauth_import_items WHERE job_public_id = ? AND state = 'updated'),
			skipped_count = (SELECT COUNT(*) FROM oauth_import_items WHERE job_public_id = ? AND state = 'skipped'),
			failed_count = (SELECT COUNT(*) FROM oauth_import_items WHERE job_public_id = ? AND state = 'failed'),
			updated_at_utc = ?, completed_at_utc = ? WHERE public_id = ?
	`, jobID, jobID, jobID, jobID, jobID, formatTime(now), formatTime(now), jobID)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM oauth_import_jobs WHERE completed_at_utc < ?`, formatTime(now.Add(-24*time.Hour)))
}

func (s *Service) ResumeOAuthImports() {
	if s.keyring.Locked() {
		return
	}
	_, _ = s.db.ExecContext(s.closeCtx, `DELETE FROM oauth_import_jobs WHERE completed_at_utc < ?`, formatTime(s.now().UTC().Add(-24*time.Hour)))
	rows, err := s.db.QueryContext(s.closeCtx, `SELECT public_id FROM oauth_import_jobs WHERE state IN ('queued', 'running') ORDER BY created_at_utc`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			go s.processOAuthImport(id)
		}
	}
}

func normalizeNames(values []string, maximumLength, maximumCount int) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > maximumLength {
			return nil, errors.New("invalid name")
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	if len(result) > maximumCount {
		return nil, errors.New("too many names")
	}
	return result, nil
}

func oauthImportAssociatedData(jobID string, row int) string {
	return fmt.Sprintf("oauth-import:%s:%d", jobID, row)
}

func randomPrefixedID(prefix string, source io.Reader) (string, error) {
	if source == nil {
		source = rand.Reader
	}
	value := make([]byte, 16)
	if _, err := io.ReadFull(source, value); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}

func credentialErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrPOPIMAPCredential):
		return "pop_imap_only"
	case errors.Is(err, ErrReauthorizationRequired):
		return "insufficient_scope"
	case errors.Is(err, ErrDuplicateMicrosoftAccount):
		return "duplicate_microsoft_account"
	case errors.Is(err, ErrAccountDisabled):
		return "account_disabled"
	default:
		switch oauthFailureCode(err) {
		case "invalid_client", "unauthorized_client":
			return "client_id_rejected"
		case "invalid_grant":
			return "refresh_token_rejected"
		default:
			return "credential_invalid"
		}
	}
}

func credentialErrorMessage(err error) string {
	switch credentialErrorCode(err) {
	case "pop_imap_only":
		return "该凭据只有 POP/IMAP 邮件权限，不包含本项目所需的 Microsoft Graph Mail.ReadWrite 权限"
	case "insufficient_scope":
		return "授权缺少 Microsoft Graph Mail.ReadWrite 权限，需要使用该 Client ID 重新登录并同意权限"
	case "duplicate_microsoft_account":
		return "该 Microsoft 账号已经绑定到其他记录"
	case "account_disabled":
		return "账号已停用，请先启用"
	case "client_id_rejected":
		return "Microsoft 拒绝了 Client ID，请确认它与生成该 refresh token 时使用的 Client ID 完全一致"
	case "refresh_token_rejected":
		return "Microsoft 拒绝了 refresh token：它可能已失效、被撤销，或不属于填写的 Client ID，请重新生成授权凭据"
	default:
		return "Microsoft 无法验证该 OAuth 凭据，请检查 Client ID 和 refresh token 后重试"
	}
}
