package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"outlook-mail-manager/internal/datakey"
)

func TestDeviceAuthorizationPersistsEncryptedTokens(t *testing.T) {
	var tokenRequests atomic.Int32
	var requestedScopeValue atomic.Value
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/devicecode":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm() error = %v", err)
			}
			requestedScopeValue.Store(r.Form.Get("scope"))
			if r.Form.Get("client_secret") != "" {
				t.Fatal("device authorization sent a client secret")
			}
			writeProviderJSON(w, http.StatusOK, map[string]any{
				"device_code": "device-secret", "user_code": "ABCD-EFGH",
				"verification_uri": "https://microsoft.com/devicelogin",
				"expires_in":       30, "interval": 1,
			})
		case "/token":
			if tokenRequests.Add(1) == 1 {
				writeProviderJSON(w, http.StatusBadRequest, map[string]string{"error": "authorization_pending"})
				return
			}
			writeProviderJSON(w, http.StatusOK, map[string]any{
				"access_token": "access-secret", "refresh_token": "refresh-secret",
				"token_type": "Bearer", "expires_in": 3600,
				"scope": strings.Join(requestedScopes, " "),
			})
		case "/v1.0/me":
			if r.Header.Get("Authorization") != "Bearer access-secret" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			writeProviderJSON(w, http.StatusOK, map[string]string{
				"id": "microsoft-user-1", "displayName": "Primary User",
				"mail": "user@outlook.com", "userPrincipalName": "user@outlook.com",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	service, store := newTestService(t, Options{
		ClientID: testMicrosoftClientID, AuthorityBaseURL: provider.URL,
		GraphBaseURL: provider.URL + "/v1.0", HTTPClient: provider.Client(),
	})
	if _, err := service.Import(context.Background(), "user@outlook.com"); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	items, _ := service.List(context.Background(), "")
	job, err := service.StartAuthorization(context.Background(), items[0].PublicID)
	if err != nil {
		t.Fatalf("StartAuthorization() error = %v", err)
	}
	job = waitAuthorization(t, service, job.ID, "completed")
	if job.UserCode != "" || job.ErrorCode != "" {
		t.Fatalf("completed authorization = %+v", job)
	}
	if scopes, _ := requestedScopeValue.Load().(string); strings.Contains(scopes, "Mail.Send") || !strings.Contains(scopes, "Mail.ReadWrite") {
		t.Fatalf("requested scopes = %q", scopes)
	}

	items, err = service.List(context.Background(), "active")
	if err != nil || len(items) != 1 {
		t.Fatalf("active accounts = %v, error = %v", items, err)
	}
	var accountID int64
	var accessCipher, refreshCipher, oauthClientID string
	if err := store.DB.QueryRow(`
		SELECT a.id, t.access_token_ciphertext, t.refresh_token_ciphertext, t.oauth_client_id
		FROM accounts a JOIN account_tokens t ON t.account_id = a.id
	`).Scan(&accountID, &accessCipher, &refreshCipher, &oauthClientID); err != nil {
		t.Fatalf("load stored tokens: %v", err)
	}
	if oauthClientID != testMicrosoftClientID {
		t.Fatalf("stored OAuth client ID = %q", oauthClientID)
	}
	if strings.Contains(accessCipher, "access-secret") || strings.Contains(refreshCipher, "refresh-secret") {
		t.Fatal("OAuth token was stored as plaintext")
	}
	access, err := service.keyring.OpenString(accessCipher, tokenAssociatedData(accountID, "access"))
	if err != nil || access != "access-secret" {
		t.Fatalf("decrypted access token = %q, error = %v", access, err)
	}
	wrongKey := make([]byte, 32)
	wrongKey[0] = 1
	wrongKeyring := datakey.New(nil)
	if err := wrongKeyring.Unlock(wrongKey); err != nil {
		t.Fatalf("Unlock(wrong key) error = %v", err)
	}
	if _, err := wrongKeyring.OpenString(accessCipher, tokenAssociatedData(accountID, "access")); err == nil {
		t.Fatal("OAuth token decrypted with the wrong data key")
	}
}

func TestAuthorizationRequiresConfirmationForAlias(t *testing.T) {
	provider := oauthTestProvider(t, "alias@outlook.com", "microsoft-user-alias")
	defer provider.Close()
	service, _ := newTestService(t, Options{
		ClientID: testMicrosoftClientID, AuthorityBaseURL: provider.URL,
		GraphBaseURL: provider.URL + "/v1.0", HTTPClient: provider.Client(),
	})
	if _, err := service.Import(context.Background(), "imported@outlook.com"); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	items, _ := service.List(context.Background(), "")
	job, err := service.StartAuthorization(context.Background(), items[0].PublicID)
	if err != nil {
		t.Fatalf("StartAuthorization() error = %v", err)
	}
	job = waitAuthorization(t, service, job.ID, "confirmation_required")
	if job.MicrosoftEmail != "alias@outlook.com" || job.ImportedEmail != "imported@outlook.com" {
		t.Fatalf("alias confirmation = %+v", job)
	}
	job, err = service.ConfirmAuthorization(context.Background(), job.ID)
	if err != nil || job.State != "completed" {
		t.Fatalf("ConfirmAuthorization() = %+v, error = %v", job, err)
	}
	items, _ = service.List(context.Background(), "active")
	if len(items) != 1 || items[0].PrimaryEmail != "alias@outlook.com" || items[0].ImportedEmail != "imported@outlook.com" {
		t.Fatalf("authorized alias account = %+v", items)
	}
}

func TestAuthorizationRestartDiscardsWrongAccountJob(t *testing.T) {
	var deviceRequests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/devicecode":
			userCode := "FIRST-CODE"
			deviceCode := "first-device"
			if deviceRequests.Add(1) == 2 {
				userCode = "SECOND-CODE"
				deviceCode = "second-device"
			}
			writeProviderJSON(w, http.StatusOK, map[string]any{
				"device_code": deviceCode, "user_code": userCode,
				"verification_uri": "https://microsoft.com/devicelogin",
				"expires_in":       30, "interval": 1,
			})
		case "/token":
			writeProviderJSON(w, http.StatusOK, map[string]any{
				"access_token": "access-secret", "refresh_token": "refresh-secret",
				"token_type": "Bearer", "expires_in": 3600,
				"scope": strings.Join(requestedScopes, " "),
			})
		case "/v1.0/me":
			writeProviderJSON(w, http.StatusOK, map[string]string{
				"id": "previous-microsoft-user", "displayName": "Previous User",
				"mail": "previous@outlook.com", "userPrincipalName": "previous@outlook.com",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	service, _ := newTestService(t, Options{
		ClientID: testMicrosoftClientID, AuthorityBaseURL: provider.URL,
		GraphBaseURL: provider.URL + "/v1.0", HTTPClient: provider.Client(),
	})
	if _, err := service.Import(context.Background(), "target@outlook.com"); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	items, _ := service.List(context.Background(), "")
	first, err := service.StartAuthorization(context.Background(), items[0].PublicID)
	if err != nil {
		t.Fatalf("StartAuthorization() error = %v", err)
	}
	first = waitAuthorization(t, service, first.ID, "confirmation_required")

	second, err := service.RestartAuthorization(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("RestartAuthorization() error = %v", err)
	}
	if second.ID == first.ID || second.UserCode != "SECOND-CODE" {
		t.Fatalf("restarted authorization = %+v, previous = %+v", second, first)
	}
	if _, err := service.Authorization(first.ID); !errors.Is(err, ErrAuthorizationNotFound) {
		t.Fatalf("old Authorization() error = %v, want ErrAuthorizationNotFound", err)
	}
	waitAuthorization(t, service, second.ID, "confirmation_required")
}

func TestEditingImportedEmailInvalidatesStaleAuthorizationJob(t *testing.T) {
	var deviceRequests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/devicecode":
			request := deviceRequests.Add(1)
			writeProviderJSON(w, http.StatusOK, map[string]any{
				"device_code": fmt.Sprintf("device-%d", request), "user_code": fmt.Sprintf("CODE-%d", request),
				"verification_uri": "https://microsoft.com/devicelogin", "expires_in": 120, "interval": 60,
			})
		case "/token":
			writeProviderJSON(w, http.StatusBadRequest, map[string]string{"error": "authorization_pending"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	service, _ := newTestService(t, Options{
		ClientID: testMicrosoftClientID, AuthorityBaseURL: provider.URL,
		GraphBaseURL: provider.URL + "/v1.0", HTTPClient: provider.Client(),
	})
	if _, err := service.Import(context.Background(), "old@outlook.com"); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	items, _ := service.List(context.Background(), "")
	first, err := service.StartAuthorization(context.Background(), items[0].PublicID)
	if err != nil {
		t.Fatalf("first StartAuthorization() error = %v", err)
	}

	updated, err := service.UpdateAccount(context.Background(), items[0].PublicID, AccountUpdate{ImportedEmail: "new@outlook.com"})
	if err != nil {
		t.Fatalf("UpdateAccount() error = %v", err)
	}
	if updated.ImportedEmail != "new@outlook.com" {
		t.Fatalf("updated account = %+v", updated)
	}
	stale, err := service.Authorization(first.ID)
	if err != nil || stale.State != "failed" || stale.ErrorCode != "account_updated" {
		t.Fatalf("stale authorization = %+v, error = %v", stale, err)
	}

	second, err := service.StartAuthorization(context.Background(), items[0].PublicID)
	if err != nil {
		t.Fatalf("second StartAuthorization() error = %v", err)
	}
	if second.ID == first.ID || second.ImportedEmail != "new@outlook.com" || second.UserCode != "CODE-2" {
		t.Fatalf("new authorization = %+v, previous = %+v", second, first)
	}
	if _, err := service.Authorization(first.ID); !errors.Is(err, ErrAuthorizationNotFound) {
		t.Fatalf("old authorization error = %v, want ErrAuthorizationNotFound", err)
	}
}

func TestAuthorizationStartupFailureDoesNotLeaveStuckJob(t *testing.T) {
	var deviceRequests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/devicecode":
			request := deviceRequests.Add(1)
			writeProviderJSON(w, http.StatusOK, map[string]any{
				"device_code":      fmt.Sprintf("device-%d", request),
				"user_code":        fmt.Sprintf("CODE-%d", request),
				"verification_uri": "https://microsoft.com/devicelogin",
				"expires_in":       30,
				"interval":         1,
			})
		case "/token":
			writeProviderJSON(w, http.StatusBadRequest, map[string]string{"error": "authorization_pending"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	service, store := newTestService(t, Options{
		ClientID: testMicrosoftClientID, AuthorityBaseURL: provider.URL,
		GraphBaseURL: provider.URL + "/v1.0", HTTPClient: provider.Client(),
	})
	if _, err := service.Import(context.Background(), "user@outlook.com"); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	items, _ := service.List(context.Background(), "")
	if _, err := store.DB.Exec(`
		CREATE TRIGGER reject_authorization_start
		BEFORE INSERT ON audit_events
		WHEN NEW.event_type = 'account_authorization_started'
		BEGIN
			SELECT RAISE(ABORT, 'test audit failure');
		END
	`); err != nil {
		t.Fatalf("create audit failure trigger: %v", err)
	}
	if _, err := service.StartAuthorization(context.Background(), items[0].PublicID); err == nil {
		t.Fatal("StartAuthorization() error = nil, want audit failure")
	}
	if _, err := store.DB.Exec(`DROP TRIGGER reject_authorization_start`); err != nil {
		t.Fatalf("drop audit failure trigger: %v", err)
	}

	job, err := service.StartAuthorization(context.Background(), items[0].PublicID)
	if err != nil {
		t.Fatalf("retry StartAuthorization() error = %v", err)
	}
	if deviceRequests.Load() != 2 || job.UserCode != "CODE-2" {
		t.Fatalf("device requests = %d, job = %+v", deviceRequests.Load(), job)
	}
}

func TestStableMicrosoftUserIDPreventsDuplicateBinding(t *testing.T) {
	service, _ := newTestService(t, Options{})
	if _, err := service.Import(context.Background(), "first@outlook.com\nsecond@outlook.com"); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	items, _ := service.List(context.Background(), "")
	token := &oauth2.Token{
		AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer",
		Expiry: time.Now().Add(time.Hour),
	}
	profile := microsoftProfile{ID: "stable-user-id", Mail: "first@outlook.com"}
	firstID, _, _, _ := service.accountIdentity(context.Background(), items[0].PublicID)
	if err := service.persistAuthorization(context.Background(), firstID, items[0].PublicID, token, profile, requestedScopes, false); err != nil {
		t.Fatalf("persistAuthorization(first) error = %v", err)
	}
	secondID, _, _, _ := service.accountIdentity(context.Background(), items[1].PublicID)
	profile.Mail = "second@outlook.com"
	if err := service.persistAuthorization(context.Background(), secondID, items[1].PublicID, token, profile, requestedScopes, false); !errors.Is(err, ErrDuplicateMicrosoftAccount) {
		t.Fatalf("persistAuthorization(duplicate) error = %v", err)
	}
}

func TestAuthorizationCannotReactivateDisabledAccount(t *testing.T) {
	service, store := newTestService(t, Options{})
	if _, err := service.Import(context.Background(), "user@outlook.com"); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	items, _ := service.List(context.Background(), "")
	if err := service.SetDisabled(context.Background(), items[0].PublicID, true); err != nil {
		t.Fatalf("SetDisabled() error = %v", err)
	}
	accountID, _, _, _ := service.accountIdentity(context.Background(), items[0].PublicID)
	token := &oauth2.Token{
		AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer",
		Expiry: time.Now().Add(time.Hour),
	}
	profile := microsoftProfile{ID: "stable-user-id", Mail: "user@outlook.com"}
	if err := service.persistAuthorization(context.Background(), accountID, items[0].PublicID, token, profile, requestedScopes, false); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("persistAuthorization() error = %v", err)
	}
	var status string
	var tokenCount int
	if err := store.DB.QueryRow("SELECT status FROM accounts WHERE id = ?", accountID).Scan(&status); err != nil {
		t.Fatalf("load account status: %v", err)
	}
	if err := store.DB.QueryRow("SELECT COUNT(*) FROM account_tokens WHERE account_id = ?", accountID).Scan(&tokenCount); err != nil {
		t.Fatalf("count account tokens: %v", err)
	}
	if status != "disabled" || tokenCount != 0 {
		t.Fatalf("status = %q, token count = %d", status, tokenCount)
	}
}

func TestGraphUnauthorizedUsesNewerAuthorizationWithoutRefreshingIt(t *testing.T) {
	oldRequestStarted := make(chan struct{})
	releaseOldResponse := make(chan struct{})
	var started sync.Once
	var tokenRequests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/me":
			switch r.Header.Get("Authorization") {
			case "Bearer access-old":
				started.Do(func() { close(oldRequestStarted) })
				<-releaseOldResponse
				w.WriteHeader(http.StatusUnauthorized)
			case "Bearer access-new":
				writeProviderJSON(w, http.StatusOK, map[string]string{
					"id": "stable-user", "displayName": "Primary User",
					"mail": "user@outlook.com", "userPrincipalName": "user@outlook.com",
				})
			default:
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
		case "/token":
			tokenRequests.Add(1)
			writeProviderJSON(w, http.StatusInternalServerError, map[string]string{"error": "unexpected_refresh"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	service, store := newTestService(t, Options{
		ClientID: testMicrosoftClientID, AuthorityBaseURL: provider.URL,
		GraphBaseURL: provider.URL + "/v1.0", HTTPClient: provider.Client(),
	})
	accountID := seedStoredToken(t, service, store.DB, time.Now().Add(time.Hour), "access-old", "refresh-old")
	items, _ := service.List(context.Background(), "")
	result := make(chan error, 1)
	go func() {
		result <- service.CheckAccount(context.Background(), items[0].PublicID)
	}()
	<-oldRequestStarted

	newToken := &oauth2.Token{
		AccessToken: "access-new", RefreshToken: "refresh-new", TokenType: "Bearer",
		Expiry: time.Now().Add(time.Hour),
	}
	profile := microsoftProfile{ID: "stable-user", Mail: "user@outlook.com"}
	if err := service.persistAuthorization(context.Background(), accountID, items[0].PublicID, newToken, profile, requestedScopes, false); err != nil {
		t.Fatalf("persistAuthorization() error = %v", err)
	}
	close(releaseOldResponse)
	if err := <-result; err != nil {
		t.Fatalf("CheckAccount() error = %v", err)
	}
	if tokenRequests.Load() != 0 {
		t.Fatalf("refresh requests = %d, want 0", tokenRequests.Load())
	}
}

func TestCheckAccountDoesNotHideExistingSyncFailure(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1.0/me" {
			http.NotFound(w, r)
			return
		}
		writeProviderJSON(w, http.StatusOK, map[string]string{
			"id": "stable-user", "displayName": "Primary User",
			"mail": "user@outlook.com", "userPrincipalName": "user@outlook.com",
		})
	}))
	defer provider.Close()
	service, store := newTestService(t, Options{
		ClientID: testMicrosoftClientID, GraphBaseURL: provider.URL + "/v1.0", HTTPClient: provider.Client(),
	})
	accountID := seedStoredToken(t, service, store.DB, time.Now().Add(time.Hour), "access", "refresh")
	if _, err := store.DB.Exec(`
		UPDATE accounts SET status = 'degraded', last_sync_error = 'sync_failed', sync_failures = 3
		WHERE id = ?
	`, accountID); err != nil {
		t.Fatalf("seed sync failure: %v", err)
	}
	items, _ := service.List(context.Background(), "")

	if err := service.CheckAccount(context.Background(), items[0].PublicID); err != nil {
		t.Fatalf("CheckAccount() error = %v", err)
	}
	var status string
	if err := store.DB.QueryRow("SELECT status FROM accounts WHERE id = ?", accountID).Scan(&status); err != nil {
		t.Fatalf("load account status: %v", err)
	}
	if status != "degraded" {
		t.Fatalf("status = %q, want degraded", status)
	}
}

func TestAuthorizationJobKeepsClientIDUsedForDeviceCode(t *testing.T) {
	tokenRequestStarted := make(chan struct{})
	releaseTokenResponse := make(chan struct{})
	var deviceClientID atomic.Value
	var tokenClientID atomic.Value
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/devicecode":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm(devicecode) error = %v", err)
			}
			deviceClientID.Store(r.Form.Get("client_id"))
			writeProviderJSON(w, http.StatusOK, map[string]any{
				"device_code": "device-secret", "user_code": "ABCD-EFGH",
				"verification_uri": "https://microsoft.com/devicelogin",
				"expires_in":       30, "interval": 1,
			})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm(token) error = %v", err)
			}
			tokenClientID.Store(r.Form.Get("client_id"))
			close(tokenRequestStarted)
			<-releaseTokenResponse
			writeProviderJSON(w, http.StatusOK, map[string]any{
				"access_token": "access-secret", "refresh_token": "refresh-secret",
				"token_type": "Bearer", "expires_in": 3600,
				"scope": strings.Join(requestedScopes, " "),
			})
		case "/v1.0/me":
			writeProviderJSON(w, http.StatusOK, map[string]string{
				"id": "stable-user", "displayName": "Primary User",
				"mail": "user@outlook.com", "userPrincipalName": "user@outlook.com",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	service, store := newTestService(t, Options{
		ClientID: testMicrosoftClientID, AuthorityBaseURL: provider.URL,
		GraphBaseURL: provider.URL + "/v1.0", HTTPClient: provider.Client(),
	})
	if _, err := service.Import(context.Background(), "user@outlook.com"); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	items, _ := service.List(context.Background(), "")
	job, err := service.StartAuthorization(context.Background(), items[0].PublicID)
	if err != nil {
		t.Fatalf("StartAuthorization() error = %v", err)
	}
	<-tokenRequestStarted
	if _, err := service.UpdateMicrosoftConfig(context.Background(), secondMicrosoftClientID); err != nil {
		t.Fatalf("UpdateMicrosoftConfig() error = %v", err)
	}
	close(releaseTokenResponse)
	waitAuthorization(t, service, job.ID, "completed")

	if got, _ := deviceClientID.Load().(string); got != testMicrosoftClientID {
		t.Fatalf("device-code client ID = %q", got)
	}
	if got, _ := tokenClientID.Load().(string); got != testMicrosoftClientID {
		t.Fatalf("token client ID = %q", got)
	}
	var storedClientID string
	if err := store.DB.QueryRow("SELECT oauth_client_id FROM account_tokens").Scan(&storedClientID); err != nil {
		t.Fatalf("load stored OAuth client ID: %v", err)
	}
	if storedClientID != testMicrosoftClientID {
		t.Fatalf("stored OAuth client ID = %q", storedClientID)
	}
}

func oauthTestProvider(t *testing.T, email, userID string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/devicecode":
			writeProviderJSON(w, http.StatusOK, map[string]any{
				"device_code": "device-secret", "user_code": "ABCD-EFGH",
				"verification_uri": "https://microsoft.com/devicelogin",
				"expires_in":       30, "interval": 1,
			})
		case "/token":
			writeProviderJSON(w, http.StatusOK, map[string]any{
				"access_token": "access-secret", "refresh_token": "refresh-secret",
				"token_type": "Bearer", "expires_in": 3600,
				"scope": strings.Join(requestedScopes, " "),
			})
		case "/v1.0/me":
			writeProviderJSON(w, http.StatusOK, map[string]string{
				"id": userID, "displayName": "Alias User", "mail": email,
				"userPrincipalName": email,
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func waitAuthorization(t *testing.T, service *Service, jobID, target string) Authorization {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := service.Authorization(jobID)
		if err != nil {
			t.Fatalf("Authorization() error = %v", err)
		}
		if job.State == target {
			return job
		}
		if job.State == "failed" || job.State == "expired" {
			t.Fatalf("authorization ended in %s: %+v", job.State, job)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("authorization did not reach %q", target)
	return Authorization{}
}

func writeProviderJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
