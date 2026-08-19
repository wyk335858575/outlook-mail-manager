package accounts

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestTokenManagerRefreshesOnceForConcurrentRequests(t *testing.T) {
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		requests.Add(1)
		writeProviderJSON(w, http.StatusOK, map[string]any{
			"access_token": "access-new", "refresh_token": "refresh-new",
			"token_type": "Bearer", "expires_in": 3600,
			"scope": strings.Join(requestedScopes, " "),
		})
	}))
	defer provider.Close()
	service, store := newTestService(t, Options{
		ClientID: testMicrosoftClientID, AuthorityBaseURL: provider.URL, HTTPClient: provider.Client(),
	})
	accountID := seedStoredToken(t, service, store.DB, time.Now().Add(-time.Minute), "access-old", "refresh-old")

	const callers = 100
	var wait sync.WaitGroup
	errorsFound := make(chan error, callers)
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			token, err := service.manager.AccessToken(context.Background(), accountID, false)
			if err != nil {
				errorsFound <- err
				return
			}
			if token != "access-new" {
				errorsFound <- errors.New("unexpected refreshed access token")
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatalf("AccessToken() error = %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("refresh requests = %d, want 1", requests.Load())
	}

	var refreshCipher string
	var version int
	if err := store.DB.QueryRow(
		"SELECT refresh_token_ciphertext, token_version FROM account_tokens WHERE account_id = ?", accountID,
	).Scan(&refreshCipher, &version); err != nil {
		t.Fatalf("load refreshed token: %v", err)
	}
	refreshToken, err := service.keyring.OpenString(refreshCipher, tokenAssociatedData(accountID, "refresh"))
	if err != nil || refreshToken != "refresh-new" || version != 2 {
		t.Fatalf("refresh token = %q, version = %d, error = %v", refreshToken, version, err)
	}
}

func TestTokenManagerPreservesRefreshTokenWhenProviderOmitsReplacement(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeProviderJSON(w, http.StatusOK, map[string]any{
			"access_token": "access-new", "token_type": "Bearer", "expires_in": 3600,
			"scope": strings.Join(requestedScopes, " "),
		})
	}))
	defer provider.Close()
	service, store := newTestService(t, Options{
		ClientID: testMicrosoftClientID, AuthorityBaseURL: provider.URL, HTTPClient: provider.Client(),
	})
	accountID := seedStoredToken(t, service, store.DB, time.Now().Add(-time.Minute), "access-old", "refresh-old")
	if _, err := service.manager.AccessToken(context.Background(), accountID, false); err != nil {
		t.Fatalf("AccessToken() error = %v", err)
	}
	var refreshCipher string
	if err := store.DB.QueryRow(
		"SELECT refresh_token_ciphertext FROM account_tokens WHERE account_id = ?", accountID,
	).Scan(&refreshCipher); err != nil {
		t.Fatalf("load refresh token: %v", err)
	}
	refreshToken, err := service.keyring.OpenString(refreshCipher, tokenAssociatedData(accountID, "refresh"))
	if err != nil || refreshToken != "refresh-old" {
		t.Fatalf("refresh token = %q, error = %v", refreshToken, err)
	}
}

func TestSuccessfulTokenRefreshDoesNotHideExistingSyncFailure(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeProviderJSON(w, http.StatusOK, map[string]any{
			"access_token": "access-new", "refresh_token": "refresh-new",
			"token_type": "Bearer", "expires_in": 3600,
			"scope": strings.Join(requestedScopes, " "),
		})
	}))
	defer provider.Close()
	service, store := newTestService(t, Options{
		ClientID: testMicrosoftClientID, AuthorityBaseURL: provider.URL, HTTPClient: provider.Client(),
	})
	accountID := seedStoredToken(t, service, store.DB, time.Now().Add(-time.Minute), "access-old", "refresh-old")
	if _, err := store.DB.Exec(`
		UPDATE accounts SET status = 'degraded', last_sync_error = 'sync_failed', sync_failures = 3
		WHERE id = ?
	`, accountID); err != nil {
		t.Fatalf("seed sync failure: %v", err)
	}

	if _, err := service.manager.AccessToken(context.Background(), accountID, false); err != nil {
		t.Fatalf("AccessToken() error = %v", err)
	}
	var status string
	if err := store.DB.QueryRow("SELECT status FROM accounts WHERE id = ?", accountID).Scan(&status); err != nil {
		t.Fatalf("load account status: %v", err)
	}
	if status != "degraded" {
		t.Fatalf("status = %q, want degraded", status)
	}
}

func TestTokenManagerInvalidGrantKeepsTokensAndRequiresReauthorization(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeProviderJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
	}))
	defer provider.Close()
	service, store := newTestService(t, Options{
		ClientID: testMicrosoftClientID, AuthorityBaseURL: provider.URL, HTTPClient: provider.Client(),
	})
	accountID := seedStoredToken(t, service, store.DB, time.Now().Add(-time.Minute), "access-old", "refresh-old")
	var beforeAccess, beforeRefresh string
	if err := store.DB.QueryRow(`
		SELECT access_token_ciphertext, refresh_token_ciphertext FROM account_tokens WHERE account_id = ?
	`, accountID).Scan(&beforeAccess, &beforeRefresh); err != nil {
		t.Fatalf("load original tokens: %v", err)
	}
	if _, err := service.manager.AccessToken(context.Background(), accountID, false); !errors.Is(err, ErrReauthorizationRequired) {
		t.Fatalf("AccessToken() error = %v", err)
	}
	var status, afterAccess, afterRefresh string
	if err := store.DB.QueryRow(`
		SELECT a.status, t.access_token_ciphertext, t.refresh_token_ciphertext
		FROM accounts a JOIN account_tokens t ON t.account_id = a.id WHERE a.id = ?
	`, accountID).Scan(&status, &afterAccess, &afterRefresh); err != nil {
		t.Fatalf("load failed refresh state: %v", err)
	}
	if status != "reauth_required" || beforeAccess != afterAccess || beforeRefresh != afterRefresh {
		t.Fatalf("status = %q, tokens changed = %v/%v", status, beforeAccess != afterAccess, beforeRefresh != afterRefresh)
	}
}

func TestStaleRefreshFailureDoesNotOverrideNewAuthorization(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-releaseResponse
		writeProviderJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
	}))
	defer provider.Close()
	service, store := newTestService(t, Options{
		ClientID: testMicrosoftClientID, AuthorityBaseURL: provider.URL, HTTPClient: provider.Client(),
	})
	accountID := seedStoredToken(t, service, store.DB, time.Now().Add(-time.Minute), "access-old", "refresh-old")
	items, _ := service.List(context.Background(), "")

	type tokenResult struct {
		value string
		err   error
	}
	result := make(chan tokenResult, 1)
	go func() {
		value, err := service.manager.AccessToken(context.Background(), accountID, false)
		result <- tokenResult{value: value, err: err}
	}()
	<-requestStarted

	newToken := &oauth2.Token{
		AccessToken: "access-new", RefreshToken: "refresh-new", TokenType: "Bearer",
		Expiry: time.Now().Add(time.Hour),
	}
	profile := microsoftProfile{ID: "stable-user", Mail: "user@outlook.com"}
	if err := service.persistAuthorization(context.Background(), accountID, items[0].PublicID, newToken, profile, requestedScopes, false); err != nil {
		t.Fatalf("persistAuthorization() error = %v", err)
	}
	close(releaseResponse)

	got := <-result
	if got.err != nil || got.value != "access-new" {
		t.Fatalf("AccessToken() = %q, error = %v", got.value, got.err)
	}
	var status string
	var version int
	if err := store.DB.QueryRow(`
		SELECT a.status, t.token_version FROM accounts a
		JOIN account_tokens t ON t.account_id = a.id WHERE a.id = ?
	`, accountID).Scan(&status, &version); err != nil {
		t.Fatalf("load refreshed account: %v", err)
	}
	if status != "active" || version != 2 {
		t.Fatalf("status = %q, token version = %d", status, version)
	}
}

func TestTokenManagerRefreshesWithTokensOriginalClientID(t *testing.T) {
	var requestedClientID atomic.Value
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		requestedClientID.Store(r.Form.Get("client_id"))
		writeProviderJSON(w, http.StatusOK, map[string]any{
			"access_token": "access-new", "refresh_token": "refresh-new",
			"token_type": "Bearer", "expires_in": 3600,
			"scope": strings.Join(requestedScopes, " "),
		})
	}))
	defer provider.Close()
	service, store := newTestService(t, Options{
		ClientID: secondMicrosoftClientID, AuthorityBaseURL: provider.URL, HTTPClient: provider.Client(),
	})
	accountID := seedStoredTokenWithClientID(
		t, service, store.DB, time.Now().Add(-time.Minute), "access-old", "refresh-old", testMicrosoftClientID,
	)
	if _, err := service.manager.AccessToken(context.Background(), accountID, false); err != nil {
		t.Fatalf("AccessToken() error = %v", err)
	}
	if got, _ := requestedClientID.Load().(string); got != testMicrosoftClientID {
		t.Fatalf("refresh client ID = %q, want %q", got, testMicrosoftClientID)
	}
}

func seedStoredToken(
	t *testing.T,
	service *Service,
	db *sql.DB,
	expiresAt time.Time,
	accessToken string,
	refreshToken string,
) int64 {
	return seedStoredTokenWithClientID(t, service, db, expiresAt, accessToken, refreshToken, testMicrosoftClientID)
}

func seedStoredTokenWithClientID(
	t *testing.T,
	service *Service,
	db *sql.DB,
	expiresAt time.Time,
	accessToken string,
	refreshToken string,
	clientID string,
) int64 {
	t.Helper()
	if _, err := service.Import(context.Background(), "user@outlook.com"); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	items, _ := service.List(context.Background(), "")
	accountID, _, _, err := service.accountIdentity(context.Background(), items[0].PublicID)
	if err != nil {
		t.Fatalf("accountIdentity() error = %v", err)
	}
	accessCipher, err := service.keyring.SealString(accessToken, tokenAssociatedData(accountID, "access"))
	if err != nil {
		t.Fatalf("SealString(access) error = %v", err)
	}
	refreshCipher, err := service.keyring.SealString(refreshToken, tokenAssociatedData(accountID, "refresh"))
	if err != nil {
		t.Fatalf("SealString(refresh) error = %v", err)
	}
	now := time.Now().UTC()
	if _, err := db.Exec(`
		INSERT INTO account_tokens (
			account_id, access_token_ciphertext, access_expires_at_utc,
			refresh_token_ciphertext, token_type, granted_scopes, oauth_client_id, token_version,
			last_refresh_at_utc, last_refresh_success_at_utc, created_at_utc, updated_at_utc
		) VALUES (?, ?, ?, ?, 'Bearer', ?, ?, 1, ?, ?, ?, ?)
	`, accountID, accessCipher, formatTime(expiresAt), refreshCipher,
		strings.Join(requestedScopes, " "), clientID, formatTime(now), formatTime(now), formatTime(now), formatTime(now)); err != nil {
		t.Fatalf("insert account token: %v", err)
	}
	if _, err := db.Exec(`
		UPDATE accounts SET status = 'active', microsoft_user_id = 'stable-user',
			primary_email = imported_email, updated_at_utc = ? WHERE id = ?
	`, formatTime(now), accountID); err != nil {
		t.Fatalf("activate account: %v", err)
	}
	return accountID
}
