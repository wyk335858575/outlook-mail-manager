package accounts

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

type Authorization struct {
	ID                      string    `json:"id"`
	AccountPublicID         string    `json:"account_public_id"`
	ImportedEmail           string    `json:"imported_email"`
	State                   string    `json:"state"`
	UserCode                string    `json:"user_code,omitempty"`
	VerificationURI         string    `json:"verification_uri,omitempty"`
	VerificationURIComplete string    `json:"verification_uri_complete,omitempty"`
	ExpiresAt               time.Time `json:"expires_at"`
	MicrosoftEmail          string    `json:"microsoft_email,omitempty"`
	DisplayName             string    `json:"display_name,omitempty"`
	ErrorCode               string    `json:"error_code,omitempty"`
	Message                 string    `json:"message,omitempty"`
}

type authorizationJob struct {
	id              string
	accountID       int64
	accountPublicID string
	importedEmail   string
	state           string
	clientID        string
	device          *oauth2.DeviceAuthResponse
	expiresAt       time.Time
	profile         microsoftProfile
	token           *oauth2.Token
	scopes          []string
	errorCode       string
	message         string
	cancel          context.CancelFunc
}

type microsoftProfile struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	Mail              string `json:"mail"`
	UserPrincipalName string `json:"userPrincipalName"`
}

type graphHTTPError struct {
	status int
}

func (e *graphHTTPError) Error() string {
	return fmt.Sprintf("Microsoft Graph returned HTTP %d", e.status)
}

func (s *Service) StartAuthorization(ctx context.Context, publicID string) (Authorization, error) {
	config, err := s.GetMicrosoftConfig(ctx)
	if err != nil {
		return Authorization{}, err
	}
	if !config.MicrosoftConfigured {
		return Authorization{}, ErrMicrosoftNotConfigured
	}
	accountID, importedEmail, status, err := s.accountIdentity(ctx, publicID)
	if err != nil {
		return Authorization{}, err
	}
	if status == "disabled" {
		return Authorization{}, ErrAccountDisabled
	}

	s.jobsMu.Lock()
	if jobID := s.accountJobs[accountID]; jobID != "" {
		if job := s.jobs[jobID]; job != nil && authorizationActive(job.state) {
			result := snapshotAuthorization(job)
			s.jobsMu.Unlock()
			return result, nil
		}
	}
	s.jobsMu.Unlock()

	requestCtx := context.WithValue(ctx, oauth2.HTTPClient, s.httpClient)
	device, err := newOAuthConfig(config.ClientID, s.oauthEndpoint).DeviceAuth(requestCtx)
	if err != nil {
		return Authorization{}, fmt.Errorf("start Microsoft device authorization: %w", err)
	}
	if device.UserCode == "" || device.VerificationURI == "" || device.DeviceCode == "" {
		return Authorization{}, errors.New("Microsoft device authorization response is incomplete")
	}
	expiresAt := device.Expiry.UTC()
	if expiresAt.IsZero() {
		expiresAt = s.now().UTC().Add(15 * time.Minute)
		device.Expiry = expiresAt
	}
	jobID, err := randomAuthorizationID(s.random)
	if err != nil {
		return Authorization{}, err
	}
	jobCtx, cancel := context.WithCancel(s.closeCtx)
	job := &authorizationJob{
		id:              jobID,
		accountID:       accountID,
		accountPublicID: publicID,
		importedEmail:   importedEmail,
		state:           "waiting",
		clientID:        config.ClientID,
		device:          device,
		expiresAt:       expiresAt,
		message:         "等待在 Microsoft 页面完成登录与授权",
		cancel:          cancel,
	}

	s.jobsMu.Lock()
	if oldID := s.accountJobs[accountID]; oldID != "" {
		if old := s.jobs[oldID]; old != nil {
			old.cancel()
		}
		delete(s.jobs, oldID)
	}
	s.jobs[jobID] = job
	s.accountJobs[accountID] = jobID
	s.jobsMu.Unlock()

	now := s.now().UTC()
	if _, err := s.db.ExecContext(ctx, `
		UPDATE accounts SET last_oauth_error = NULL, updated_at_utc = ? WHERE id = ?
	`, formatTime(now), accountID); err != nil {
		s.discardAuthorizationJob(jobID)
		return Authorization{}, fmt.Errorf("record authorization start: %w", err)
	}
	if err := insertAudit(ctx, s.db, "account_authorization_started", "admin", map[string]any{
		"account": publicID,
	}, now); err != nil {
		s.discardAuthorizationJob(jobID)
		return Authorization{}, err
	}

	go s.pollAuthorization(jobCtx, jobID)
	return snapshotAuthorization(job), nil
}

func (s *Service) discardAuthorizationJob(jobID string) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	job := s.jobs[jobID]
	if job == nil {
		return
	}
	job.cancel()
	delete(s.jobs, jobID)
	if s.accountJobs[job.accountID] == jobID {
		delete(s.accountJobs, job.accountID)
	}
}

func (s *Service) Authorization(jobID string) (Authorization, error) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	job := s.jobs[jobID]
	if job == nil {
		return Authorization{}, ErrAuthorizationNotFound
	}
	return snapshotAuthorization(job), nil
}

func (s *Service) ConfirmAuthorization(ctx context.Context, jobID string) (Authorization, error) {
	s.jobsMu.Lock()
	job := s.jobs[jobID]
	if job == nil {
		s.jobsMu.Unlock()
		return Authorization{}, ErrAuthorizationNotFound
	}
	if job.state != "confirmation_required" || job.token == nil {
		s.jobsMu.Unlock()
		return Authorization{}, ErrAuthorizationState
	}
	if !s.now().UTC().Before(job.expiresAt) {
		job.state = "expired"
		job.errorCode = "confirmation_expired"
		job.message = "确认时间已过，请重新开始授权"
		result := snapshotAuthorization(job)
		s.jobsMu.Unlock()
		return result, ErrAuthorizationState
	}
	job.state = "finalizing"
	token := job.token
	profile := job.profile
	scopes := append([]string(nil), job.scopes...)
	clientID := job.clientID
	accountID := job.accountID
	publicID := job.accountPublicID
	s.jobsMu.Unlock()

	err := s.persistAuthorizationWithClientID(ctx, accountID, publicID, token, profile, scopes, clientID, AuthMethodWeb, true)
	s.finishAuthorization(jobID, err)
	return s.Authorization(jobID)
}

func (s *Service) RestartAuthorization(ctx context.Context, jobID string) (Authorization, error) {
	s.jobsMu.Lock()
	job := s.jobs[jobID]
	if job == nil {
		s.jobsMu.Unlock()
		return Authorization{}, ErrAuthorizationNotFound
	}
	if job.state != "confirmation_required" {
		s.jobsMu.Unlock()
		return Authorization{}, ErrAuthorizationState
	}
	publicID := job.accountPublicID
	accountID := job.accountID
	job.cancel()
	job.device = nil
	job.token = nil
	job.profile = microsoftProfile{}
	job.scopes = nil
	delete(s.jobs, jobID)
	if s.accountJobs[accountID] == jobID {
		delete(s.accountJobs, accountID)
	}
	s.jobsMu.Unlock()

	return s.StartAuthorization(ctx, publicID)
}

func (s *Service) CheckAccount(ctx context.Context, publicID string) error {
	accountID, _, status, err := s.accountIdentity(ctx, publicID)
	if err != nil {
		return err
	}
	if status == "disabled" {
		return ErrAccountDisabled
	}
	lease, err := s.manager.accessToken(ctx, accountID, false, nil)
	if err != nil {
		return err
	}
	_, err = s.fetchProfile(ctx, lease.value)
	var graphErr *graphHTTPError
	if errors.As(err, &graphErr) && graphErr.status == http.StatusUnauthorized {
		rejectedVersion := lease.version
		refreshedLease, refreshErr := s.manager.accessToken(ctx, accountID, true, &rejectedVersion)
		if refreshErr != nil {
			return refreshErr
		}
		lease = refreshedLease
		_, err = s.fetchProfile(ctx, lease.value)
	}
	if err != nil {
		s.recordGraphFailure(ctx, accountID, lease.version, err)
		return err
	}
	now := s.now().UTC()
	if _, err := s.db.ExecContext(ctx, `
		UPDATE accounts SET status = CASE WHEN sync_failures = 0 THEN 'active' ELSE status END,
			last_graph_success_at_utc = ?,
			last_oauth_error = NULL, consecutive_failures = 0, next_retry_at_utc = NULL,
			updated_at_utc = ? WHERE id = ? AND status != 'disabled'
			AND EXISTS (SELECT 1 FROM account_tokens WHERE account_id = ? AND token_version = ?)
	`, formatTime(now), formatTime(now), accountID, accountID, lease.version); err != nil {
		return fmt.Errorf("record Graph health: %w", err)
	}
	return nil
}

func (s *Service) pollAuthorization(ctx context.Context, jobID string) {
	s.jobsMu.Lock()
	job := s.jobs[jobID]
	if job == nil {
		s.jobsMu.Unlock()
		return
	}
	device := job.device
	clientID := job.clientID
	s.jobsMu.Unlock()

	pollCtx := context.WithValue(ctx, oauth2.HTTPClient, s.httpClient)
	token, err := newOAuthConfig(clientID, s.oauthEndpoint).DeviceAccessToken(pollCtx, device)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		s.failAuthorization(jobID, oauthFailureCode(err), authorizationFailureMessage(err))
		return
	}
	if token.AccessToken == "" || token.RefreshToken == "" || token.Expiry.IsZero() {
		s.failAuthorization(jobID, "incomplete_token", "Microsoft 未返回完整的离线访问凭据，请重新授权")
		return
	}
	scopes := tokenScopes(token)
	if !hasRequiredScopes(scopes) {
		s.failAuthorization(jobID, "insufficient_scope", "Microsoft 授权未包含项目所需权限，请重新同意")
		return
	}
	profile, err := s.fetchProfile(ctx, token.AccessToken)
	if err != nil {
		s.failAuthorization(jobID, "profile_unavailable", "无法读取 Microsoft 账号资料，请重试")
		return
	}
	microsoftEmail := profileEmail(profile)

	s.jobsMu.Lock()
	job = s.jobs[jobID]
	if job == nil || job.state != "waiting" {
		s.jobsMu.Unlock()
		return
	}
	if !strings.EqualFold(job.importedEmail, microsoftEmail) {
		job.state = "confirmation_required"
		job.profile = profile
		job.token = token
		job.scopes = scopes
		job.expiresAt = s.now().UTC().Add(10 * time.Minute)
		job.message = "Microsoft 返回的邮箱与导入地址不同，请确认这是同一账号的别名"
		s.jobsMu.Unlock()
		return
	}
	job.state = "finalizing"
	accountID := job.accountID
	publicID := job.accountPublicID
	s.jobsMu.Unlock()

	err = s.persistAuthorizationWithClientID(ctx, accountID, publicID, token, profile, scopes, clientID, AuthMethodWeb, false)
	s.finishAuthorization(jobID, err)
}

func (s *Service) persistAuthorization(
	ctx context.Context,
	accountID int64,
	publicID string,
	token *oauth2.Token,
	profile microsoftProfile,
	scopes []string,
	aliasConfirmed bool,
) error {
	config, err := s.GetMicrosoftConfig(ctx)
	if err != nil {
		return err
	}
	return s.persistAuthorizationWithClientID(ctx, accountID, publicID, token, profile, scopes, config.ClientID, AuthMethodWeb, aliasConfirmed)
}

func (s *Service) persistAuthorizationWithClientID(
	ctx context.Context,
	accountID int64,
	publicID string,
	token *oauth2.Token,
	profile microsoftProfile,
	scopes []string,
	clientID string,
	authMethod AuthMethod,
	aliasConfirmed bool,
) error {
	if authMethod != AuthMethodWeb && authMethod != AuthMethodOAuth {
		return errors.New("invalid account authorization method")
	}
	if profile.ID == "" || profileEmail(profile) == "" {
		return errors.New("Microsoft profile is incomplete")
	}
	accessCiphertext, err := s.keyring.SealString(token.AccessToken, tokenAssociatedData(accountID, "access"))
	if err != nil {
		return err
	}
	refreshCiphertext, err := s.keyring.SealString(token.RefreshToken, tokenAssociatedData(accountID, "refresh"))
	if err != nil {
		return err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin authorization save: %w", err)
	}
	defer tx.Rollback()
	var existingAccountID int64
	err = tx.QueryRowContext(ctx,
		"SELECT id FROM accounts WHERE microsoft_user_id = ?", profile.ID,
	).Scan(&existingAccountID)
	if err == nil && existingAccountID != accountID {
		return ErrDuplicateMicrosoftAccount
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check Microsoft account identity: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO account_tokens (
			account_id, access_token_ciphertext, access_expires_at_utc,
			refresh_token_ciphertext, token_type, granted_scopes, oauth_client_id, token_version,
			last_refresh_at_utc, last_refresh_success_at_utc, created_at_utc, updated_at_utc
		) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET
			access_token_ciphertext = excluded.access_token_ciphertext,
			access_expires_at_utc = excluded.access_expires_at_utc,
			refresh_token_ciphertext = excluded.refresh_token_ciphertext,
			token_type = excluded.token_type,
			granted_scopes = excluded.granted_scopes,
			oauth_client_id = excluded.oauth_client_id,
			token_version = account_tokens.token_version + 1,
			last_refresh_at_utc = excluded.last_refresh_at_utc,
			last_refresh_success_at_utc = excluded.last_refresh_success_at_utc,
			updated_at_utc = excluded.updated_at_utc
	`, accountID, accessCiphertext, formatTime(token.Expiry), refreshCiphertext,
		token.Type(), strings.Join(normalizeScopes(scopes), " "), clientID,
		formatTime(now), formatTime(now), formatTime(now), formatTime(now)); err != nil {
		return fmt.Errorf("save account tokens: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE accounts SET auth_method = ?, microsoft_user_id = ?, primary_email = ?, display_name = ?,
			status = 'active', reauth_reason = NULL, last_oauth_error = NULL,
			consecutive_failures = 0, next_retry_at_utc = NULL,
			last_graph_success_at_utc = ?, updated_at_utc = ? WHERE id = ? AND status != 'disabled'
	`, authMethod, profile.ID, strings.ToLower(profileEmail(profile)), profile.DisplayName,
		formatTime(now), formatTime(now), accountID)
	if err != nil {
		return fmt.Errorf("activate account: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read account activation result: %w", err)
	}
	if updated == 0 {
		return ErrAccountDisabled
	}
	if err := insertAudit(ctx, tx, "account_authorized", "admin", map[string]any{
		"account": publicID, "alias_confirmed": aliasConfirmed,
	}, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit authorization: %w", err)
	}
	return nil
}

func (s *Service) finishAuthorization(jobID string, err error) {
	s.jobsMu.Lock()
	job := s.jobs[jobID]
	if job == nil {
		s.jobsMu.Unlock()
		return
	}
	job.token = nil
	job.profile = microsoftProfile{}
	job.scopes = nil
	if err == nil {
		job.state = "completed"
		job.errorCode = ""
		job.message = "Microsoft 账号授权完成"
		s.jobsMu.Unlock()
		return
	}
	job.state = "failed"
	job.errorCode = "authorization_failed"
	job.message = "授权保存失败，请重试"
	if errors.Is(err, ErrDuplicateMicrosoftAccount) {
		job.errorCode = "duplicate_microsoft_account"
		job.message = "这个 Microsoft 账号已经绑定到其他导入记录"
	} else if errors.Is(err, ErrAccountDisabled) {
		job.errorCode = "account_disabled"
		job.message = "账号已停用，未保存本次授权"
	}
	accountID := job.accountID
	code := job.errorCode
	s.jobsMu.Unlock()
	s.recordAuthorizationFailure(accountID, code, false)
}

func (s *Service) failAuthorization(jobID, code, message string) {
	s.jobsMu.Lock()
	job := s.jobs[jobID]
	if job == nil {
		s.jobsMu.Unlock()
		return
	}
	job.state = "failed"
	if code == "expired_token" || code == "authorization_expired" {
		job.state = "expired"
	}
	job.errorCode = code
	job.message = message
	job.token = nil
	accountID := job.accountID
	reauth := code == "insufficient_scope" || code == "invalid_grant" || code == "interaction_required" || code == "consent_required"
	s.jobsMu.Unlock()
	s.recordAuthorizationFailure(accountID, code, reauth)
}

func (s *Service) recordAuthorizationFailure(accountID int64, code string, reauth bool) {
	now := s.now().UTC()
	if reauth {
		_, _ = s.db.Exec(`
			UPDATE accounts SET status = 'reauth_required', reauth_reason = ?,
				last_oauth_error = ?, consecutive_failures = consecutive_failures + 1,
				updated_at_utc = ? WHERE id = ? AND status != 'disabled'
		`, "Microsoft 授权需要重新确认", code, formatTime(now), accountID)
		return
	}
	_, _ = s.db.Exec(`
		UPDATE accounts SET last_oauth_error = ?,
			consecutive_failures = consecutive_failures + 1, updated_at_utc = ?
		WHERE id = ? AND status != 'disabled'
	`, code, formatTime(now), accountID)
}

func (s *Service) recordGraphFailure(ctx context.Context, accountID, tokenVersion int64, err error) {
	now := s.now().UTC()
	var graphErr *graphHTTPError
	if errors.As(err, &graphErr) && (graphErr.status == http.StatusUnauthorized || graphErr.status == http.StatusForbidden) {
		_, _ = s.db.ExecContext(ctx, `
			UPDATE accounts SET status = 'reauth_required', reauth_reason = ?,
				last_oauth_error = ?, consecutive_failures = consecutive_failures + 1,
			updated_at_utc = ? WHERE id = ? AND status != 'disabled'
			AND EXISTS (SELECT 1 FROM account_tokens WHERE account_id = ? AND token_version = ?)
		`, "Microsoft 拒绝了账号访问，请重新授权", fmt.Sprintf("graph_http_%d", graphErr.status),
			formatTime(now), accountID, accountID, tokenVersion)
		return
	}
	_, _ = s.db.ExecContext(ctx, `
		UPDATE accounts SET
			status = CASE WHEN consecutive_failures + 1 >= 3 THEN 'degraded' ELSE status END,
			last_oauth_error = 'graph_unavailable', consecutive_failures = consecutive_failures + 1,
		updated_at_utc = ? WHERE id = ? AND status != 'disabled'
		AND EXISTS (SELECT 1 FROM account_tokens WHERE account_id = ? AND token_version = ?)
	`, formatTime(now), accountID, accountID, tokenVersion)
}

func (s *Service) fetchProfile(ctx context.Context, accessToken string) (microsoftProfile, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.graphBaseURL+"/me?$select=id,displayName,mail,userPrincipalName", nil)
	if err != nil {
		return microsoftProfile{}, fmt.Errorf("create Microsoft Graph request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return microsoftProfile{}, fmt.Errorf("request Microsoft Graph profile: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return microsoftProfile{}, &graphHTTPError{status: response.StatusCode}
	}
	var profile microsoftProfile
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(&profile); err != nil {
		return microsoftProfile{}, fmt.Errorf("decode Microsoft Graph profile: %w", err)
	}
	if profile.ID == "" || profileEmail(profile) == "" {
		return microsoftProfile{}, errors.New("Microsoft Graph profile is incomplete")
	}
	return profile, nil
}

func profileEmail(profile microsoftProfile) string {
	if strings.TrimSpace(profile.Mail) != "" {
		return strings.TrimSpace(profile.Mail)
	}
	return strings.TrimSpace(profile.UserPrincipalName)
}

func tokenScopes(token *oauth2.Token) []string {
	value, _ := token.Extra("scope").(string)
	return strings.Fields(value)
}

func hasRequiredScopes(scopes []string) bool {
	if hasGraphDefaultScope(scopes) {
		return true
	}
	granted := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		granted[canonicalScopeName(scope)] = true
	}
	for _, required := range requiredAccessScopes {
		if !granted[canonicalScopeName(required)] {
			return false
		}
	}
	return true
}

func hasImportedMailScopes(scopes []string) bool {
	if hasGraphDefaultScope(scopes) {
		return true
	}
	for _, scope := range scopes {
		switch canonicalScopeName(scope) {
		case "mail.read", "mail.readwrite":
			return true
		}
	}
	return false
}

func hasGraphDefaultScope(scopes []string) bool {
	for _, scope := range scopes {
		if strings.EqualFold(strings.TrimSpace(scope), graphDefaultScope) {
			return true
		}
	}
	return false
}

func canonicalScopeName(scope string) string {
	value := strings.TrimSpace(strings.ToLower(scope))
	if index := strings.LastIndex(value, "/"); index >= 0 && index+1 < len(value) {
		value = value[index+1:]
	}
	return value
}

func hasPOPIMAPScopes(scopes []string) bool {
	for _, scope := range scopes {
		value := canonicalScopeName(scope)
		if strings.Contains(value, "imap.accessasuser.all") || strings.Contains(value, "pop.accessasuser.all") {
			return true
		}
	}
	return false
}

func normalizeScopes(scopes []string) []string {
	result := append([]string(nil), scopes...)
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i]) < strings.ToLower(result[j])
	})
	return result
}

func oauthFailureCode(err error) string {
	var retrieveError *oauth2.RetrieveError
	if errors.As(err, &retrieveError) && retrieveError.ErrorCode != "" {
		return retrieveError.ErrorCode
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "authorization_expired"
	}
	return "authorization_failed"
}

func authorizationFailureMessage(err error) string {
	switch oauthFailureCode(err) {
	case "access_denied", "authorization_declined":
		return "Microsoft 授权已被拒绝"
	case "expired_token", "authorization_expired":
		return "设备码已过期，请重新开始授权"
	case "invalid_client":
		return "Microsoft 应用配置无效，请在设置页检查 Client ID"
	default:
		return "Microsoft 授权未完成，请重试"
	}
}

func randomAuthorizationID(random io.Reader) (string, error) {
	value := make([]byte, 18)
	if _, err := io.ReadFull(random, value); err != nil {
		return "", fmt.Errorf("generate authorization id: %w", err)
	}
	return "oauth_" + base64.RawURLEncoding.EncodeToString(value), nil
}

func snapshotAuthorization(job *authorizationJob) Authorization {
	result := Authorization{
		ID: job.id, AccountPublicID: job.accountPublicID, ImportedEmail: job.importedEmail,
		State: job.state, ExpiresAt: job.expiresAt, ErrorCode: job.errorCode, Message: job.message,
	}
	if job.device != nil && authorizationActive(job.state) {
		result.UserCode = job.device.UserCode
		result.VerificationURI = job.device.VerificationURI
		result.VerificationURIComplete = job.device.VerificationURIComplete
	}
	if job.state == "confirmation_required" {
		result.MicrosoftEmail = profileEmail(job.profile)
		result.DisplayName = job.profile.DisplayName
	}
	return result
}

func authorizationActive(state string) bool {
	return state == "waiting" || state == "confirmation_required" || state == "finalizing"
}

func (s *Service) authorizationInProgress(accountID int64) bool {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	jobID := s.accountJobs[accountID]
	job := s.jobs[jobID]
	return job != nil && authorizationActive(job.state)
}

func (s *Service) cancelAccountAuthorization(accountID int64) {
	s.invalidateAuthorization(accountID, "", "account_disabled", "账号已停用")
}

func (s *Service) invalidateAuthorizationForAccountChange(accountID int64, previousEmail string) {
	s.invalidateAuthorization(accountID, previousEmail, "account_updated", "账号资料已更新，请重新开始授权")
}

func (s *Service) invalidateAuthorization(accountID int64, matchEmail, errorCode, message string) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	jobID := s.accountJobs[accountID]
	if job := s.jobs[jobID]; job != nil && (matchEmail == "" || strings.EqualFold(job.importedEmail, matchEmail)) {
		job.cancel()
		job.state = "failed"
		job.errorCode = errorCode
		job.message = message
	}
}
