package accounts

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"outlook-mail-manager/internal/datakey"
)

const (
	refreshBaseWindow  = 5 * time.Minute
	refreshJitterMax   = 2 * time.Minute
	refreshConcurrency = 4
	degradedThreshold  = 3
)

var (
	ErrTokenUnavailable   = errors.New("account token is unavailable")
	ErrOAuthConfiguration = errors.New("Microsoft OAuth application configuration is invalid")
	errTokenChanged       = errors.New("account token changed")
)

type RetryError struct {
	RetryAt time.Time
}

func (e *RetryError) Error() string {
	return "Microsoft token refresh is temporarily unavailable"
}

type TokenManager struct {
	db          *sql.DB
	keyring     *datakey.Store
	endpoint    oauth2.Endpoint
	httpClient  *http.Client
	now         func() time.Time
	refreshGate chan struct{}
	locks       sync.Map
}

type storedToken struct {
	accountID     int64
	status        string
	accessCipher  string
	refreshCipher string
	expiresAt     time.Time
	tokenType     string
	scopes        string
	clientID      string
	version       int64
	failures      int
	nextRetryAt   *time.Time
}

type tokenLease struct {
	value   string
	version int64
}

type AccessTokenLease struct {
	AccessToken string
	Version     int64
}

func newTokenManager(
	db *sql.DB,
	keyring *datakey.Store,
	endpoint oauth2.Endpoint,
	httpClient *http.Client,
	now func() time.Time,
) *TokenManager {
	return &TokenManager{
		db: db, keyring: keyring, endpoint: endpoint, httpClient: httpClient, now: now,
		refreshGate: make(chan struct{}, refreshConcurrency),
	}
}

func (m *TokenManager) AccessToken(ctx context.Context, accountID int64, forceRefresh bool) (string, error) {
	lease, err := m.accessToken(ctx, accountID, forceRefresh, nil)
	return lease.value, err
}

func (m *TokenManager) Acquire(
	ctx context.Context,
	accountID int64,
	forceRefresh bool,
	rejectedVersion *int64,
) (AccessTokenLease, error) {
	lease, err := m.accessToken(ctx, accountID, forceRefresh, rejectedVersion)
	return AccessTokenLease{AccessToken: lease.value, Version: lease.version}, err
}

func (m *TokenManager) accessToken(
	ctx context.Context,
	accountID int64,
	forceRefresh bool,
	rejectedVersion *int64,
) (tokenLease, error) {
	lock := m.accountLock(accountID)
	lock.Lock()
	defer lock.Unlock()

	for attempt := 0; attempt < 2; attempt++ {
		stored, err := m.load(ctx, accountID)
		if err != nil {
			return tokenLease{}, err
		}
		switch stored.status {
		case "disabled":
			return tokenLease{}, ErrAccountDisabled
		case "reauth_required":
			return tokenLease{}, ErrReauthorizationRequired
		}

		now := m.now().UTC()
		accessToken, err := m.keyring.OpenString(stored.accessCipher, tokenAssociatedData(accountID, "access"))
		if err != nil {
			return tokenLease{}, fmt.Errorf("decrypt access token: %w", err)
		}
		lease := tokenLease{value: accessToken, version: stored.version}
		shouldForce := forceRefresh && (rejectedVersion == nil || stored.version == *rejectedVersion)
		refreshAt := stored.expiresAt.Add(-refreshBaseWindow - stableJitter(accountID, stored.version, refreshJitterMax))
		if !shouldForce && now.Before(refreshAt) {
			return lease, nil
		}
		if !shouldForce && stored.nextRetryAt != nil && now.Before(*stored.nextRetryAt) {
			if now.Before(stored.expiresAt) {
				return lease, nil
			}
			return tokenLease{}, &RetryError{RetryAt: *stored.nextRetryAt}
		}

		select {
		case m.refreshGate <- struct{}{}:
		case <-ctx.Done():
			return tokenLease{}, ctx.Err()
		}
		refreshed, refreshErr := m.refresh(ctx, stored, accessToken)
		<-m.refreshGate
		if refreshErr != nil {
			if errors.Is(refreshErr, errTokenChanged) {
				continue
			}
			code, kind := classifyRefreshError(refreshErr)
			switch kind {
			case "reauth":
				committed, err := m.markReauthorization(ctx, accountID, stored.version, code)
				if err != nil {
					return tokenLease{}, err
				}
				if !committed {
					continue
				}
				return tokenLease{}, ErrReauthorizationRequired
			case "configuration":
				committed, err := m.recordConfigurationError(ctx, accountID, stored.version, code)
				if err != nil {
					return tokenLease{}, err
				}
				if !committed {
					continue
				}
				return tokenLease{}, ErrOAuthConfiguration
			default:
				retryAt, committed, err := m.recordTemporaryFailure(ctx, stored, code)
				if err != nil {
					return tokenLease{}, err
				}
				if !committed {
					continue
				}
				if now.Before(stored.expiresAt) {
					return lease, nil
				}
				return tokenLease{}, &RetryError{RetryAt: retryAt}
			}
		}
		if refreshed.AccessToken == "" || refreshed.Expiry.IsZero() {
			retryAt, committed, err := m.recordTemporaryFailure(ctx, stored, "incomplete_token")
			if err != nil {
				return tokenLease{}, err
			}
			if !committed {
				continue
			}
			if now.Before(stored.expiresAt) {
				return lease, nil
			}
			return tokenLease{}, &RetryError{RetryAt: retryAt}
		}
		if refreshed.RefreshToken == "" {
			refreshToken, err := m.keyring.OpenString(stored.refreshCipher, tokenAssociatedData(accountID, "refresh"))
			if err != nil {
				return tokenLease{}, fmt.Errorf("decrypt refresh token: %w", err)
			}
			refreshed.RefreshToken = refreshToken
		}
		scopes := tokenScopes(refreshed)
		if len(scopes) == 0 {
			scopes = strings.Fields(stored.scopes)
		}
		if !hasImportedMailScopes(scopes) {
			committed, err := m.markReauthorization(ctx, accountID, stored.version, "insufficient_scope")
			if err != nil {
				return tokenLease{}, err
			}
			if !committed {
				continue
			}
			return tokenLease{}, ErrReauthorizationRequired
		}
		committed, err := m.commitRefresh(ctx, stored, refreshed, scopes)
		if err != nil {
			return tokenLease{}, err
		}
		if !committed {
			continue
		}
		return tokenLease{value: refreshed.AccessToken, version: stored.version + 1}, nil
	}
	return tokenLease{}, errors.New("account token changed during refresh")
}

func (m *TokenManager) refresh(ctx context.Context, stored storedToken, accessToken string) (*oauth2.Token, error) {
	if strings.TrimSpace(stored.clientID) == "" {
		return nil, ErrOAuthConfiguration
	}
	refreshToken, err := m.keyring.OpenString(stored.refreshCipher, tokenAssociatedData(stored.accountID, "refresh"))
	if err != nil {
		return nil, fmt.Errorf("decrypt refresh token: %w", err)
	}
	now := m.now().UTC()
	result, err := m.db.ExecContext(ctx,
		"UPDATE account_tokens SET last_refresh_at_utc = ?, updated_at_utc = ? WHERE account_id = ? AND token_version = ?",
		formatTime(now), formatTime(now), stored.accountID, stored.version,
	)
	if err != nil {
		return nil, fmt.Errorf("record token refresh attempt: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("read token refresh attempt result: %w", err)
	}
	if updated == 0 {
		return nil, errTokenChanged
	}
	return refreshMicrosoftToken(ctx, m.httpClient, m.endpoint, stored.clientID, refreshToken, m.now)
}

func (m *TokenManager) commitRefresh(
	ctx context.Context,
	stored storedToken,
	token *oauth2.Token,
	scopes []string,
) (bool, error) {
	accessCipher, err := m.keyring.SealString(token.AccessToken, tokenAssociatedData(stored.accountID, "access"))
	if err != nil {
		return false, err
	}
	refreshCipher, err := m.keyring.SealString(token.RefreshToken, tokenAssociatedData(stored.accountID, "refresh"))
	if err != nil {
		return false, err
	}
	now := m.now().UTC()
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin token refresh save: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE account_tokens SET access_token_ciphertext = ?, access_expires_at_utc = ?,
			refresh_token_ciphertext = ?, token_type = ?, granted_scopes = ?,
			token_version = token_version + 1, last_refresh_at_utc = ?,
			last_refresh_success_at_utc = ?, updated_at_utc = ?
		WHERE account_id = ? AND token_version = ?
	`, accessCipher, formatTime(token.Expiry), refreshCipher, token.Type(),
		strings.Join(normalizeScopes(scopes), " "), formatTime(now), formatTime(now), formatTime(now),
		stored.accountID, stored.version)
	if err != nil {
		return false, fmt.Errorf("save refreshed token: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read token refresh result: %w", err)
	}
	if rows == 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE accounts SET status = CASE WHEN sync_failures = 0 THEN 'active' ELSE status END,
			reauth_reason = NULL, last_oauth_error = NULL,
			consecutive_failures = 0, next_retry_at_utc = NULL, updated_at_utc = ?
		WHERE id = ? AND status != 'disabled'
	`, formatTime(now), stored.accountID); err != nil {
		return false, fmt.Errorf("record successful token refresh: %w", err)
	}
	if err := insertAudit(ctx, tx, "account_token_refreshed", "system", map[string]any{
		"account_id": stored.accountID, "token_version": stored.version + 1,
	}, now); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit token refresh: %w", err)
	}
	return true, nil
}

func (m *TokenManager) load(ctx context.Context, accountID int64) (storedToken, error) {
	var stored storedToken
	var expiryValue string
	var nextRetry sql.NullString
	err := m.db.QueryRowContext(ctx, `
		SELECT a.id, a.status, t.access_token_ciphertext, t.refresh_token_ciphertext,
			t.access_expires_at_utc, t.token_type, t.granted_scopes, t.oauth_client_id, t.token_version,
			a.consecutive_failures, a.next_retry_at_utc
		FROM accounts a JOIN account_tokens t ON t.account_id = a.id WHERE a.id = ?
	`, accountID).Scan(&stored.accountID, &stored.status, &stored.accessCipher, &stored.refreshCipher,
		&expiryValue, &stored.tokenType, &stored.scopes, &stored.clientID, &stored.version, &stored.failures, &nextRetry)
	if errors.Is(err, sql.ErrNoRows) {
		return storedToken{}, ErrTokenUnavailable
	}
	if err != nil {
		return storedToken{}, fmt.Errorf("load account token: %w", err)
	}
	stored.expiresAt, err = time.Parse(time.RFC3339Nano, expiryValue)
	if err != nil {
		return storedToken{}, fmt.Errorf("parse access token expiry: %w", err)
	}
	stored.nextRetryAt = parseOptionalTime(nextRetry)
	return stored, nil
}

func (m *TokenManager) markReauthorization(ctx context.Context, accountID, version int64, code string) (bool, error) {
	now := m.now().UTC()
	result, err := m.db.ExecContext(ctx, `
		UPDATE accounts SET status = 'reauth_required', reauth_reason = ?,
			last_oauth_error = ?, consecutive_failures = consecutive_failures + 1,
			next_retry_at_utc = NULL, updated_at_utc = ?
		WHERE id = ? AND status != 'disabled'
			AND EXISTS (SELECT 1 FROM account_tokens WHERE account_id = ? AND token_version = ?)
	`, "Microsoft 授权已失效，需要重新登录并同意权限", code, formatTime(now), accountID, accountID, version)
	if err != nil {
		return false, fmt.Errorf("mark account for reauthorization: %w", err)
	}
	return changed(result)
}

func (m *TokenManager) recordConfigurationError(ctx context.Context, accountID, version int64, code string) (bool, error) {
	now := m.now().UTC()
	result, err := m.db.ExecContext(ctx, `
		UPDATE accounts SET last_oauth_error = ?, updated_at_utc = ? WHERE id = ?
			AND EXISTS (SELECT 1 FROM account_tokens WHERE account_id = ? AND token_version = ?)
	`, code, formatTime(now), accountID, accountID, version)
	if err != nil {
		return false, fmt.Errorf("record OAuth configuration error: %w", err)
	}
	return changed(result)
}

func (m *TokenManager) recordTemporaryFailure(ctx context.Context, stored storedToken, code string) (time.Time, bool, error) {
	now := m.now().UTC()
	failures := stored.failures + 1
	retryAt := now.Add(refreshBackoff(stored.accountID, stored.version, failures))
	status := stored.status
	if failures >= degradedThreshold && status == "active" {
		status = "degraded"
	}
	result, err := m.db.ExecContext(ctx, `
		UPDATE accounts SET status = ?, last_oauth_error = ?, consecutive_failures = ?,
			next_retry_at_utc = ?, updated_at_utc = ? WHERE id = ? AND status != 'disabled'
			AND EXISTS (SELECT 1 FROM account_tokens WHERE account_id = ? AND token_version = ?)
	`, status, code, failures, formatTime(retryAt), formatTime(now), stored.accountID, stored.accountID, stored.version)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("record token refresh failure: %w", err)
	}
	committed, err := changed(result)
	return retryAt, committed, err
}

func changed(result sql.Result) (bool, error) {
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read account state update result: %w", err)
	}
	return rows == 1, nil
}

func (m *TokenManager) accountLock(accountID int64) *sync.Mutex {
	value, _ := m.locks.LoadOrStore(accountID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func classifyRefreshError(err error) (string, string) {
	if errors.Is(err, ErrOAuthConfiguration) {
		return "missing_client_id", "configuration"
	}
	var retrieveError *oauth2.RetrieveError
	if errors.As(err, &retrieveError) {
		code := retrieveError.ErrorCode
		switch code {
		case "invalid_grant", "interaction_required", "consent_required":
			return code, "reauth"
		case "invalid_client":
			return code, "configuration"
		case "temporarily_unavailable":
			return code, "temporary"
		}
		if retrieveError.Response != nil && (retrieveError.Response.StatusCode == http.StatusTooManyRequests || retrieveError.Response.StatusCode >= 500) {
			return fmt.Sprintf("http_%d", retrieveError.Response.StatusCode), "temporary"
		}
		if code != "" {
			return code, "temporary"
		}
	}
	return "network_error", "temporary"
}

func stableJitter(accountID, version int64, maximum time.Duration) time.Duration {
	if maximum <= 0 {
		return 0
	}
	value := sha256.Sum256([]byte(fmt.Sprintf("refresh:%d:%d", accountID, version)))
	n := uint64(value[0])<<56 | uint64(value[1])<<48 | uint64(value[2])<<40 | uint64(value[3])<<32 |
		uint64(value[4])<<24 | uint64(value[5])<<16 | uint64(value[6])<<8 | uint64(value[7])
	return time.Duration(n % uint64(maximum))
}

func refreshBackoff(accountID, version int64, failures int) time.Duration {
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
	return base + stableJitter(accountID, version+int64(failures), 15*time.Second)
}
