package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"outlook-mail-manager/internal/database"
	"outlook-mail-manager/internal/datakey"
)

func TestOAuthImportValidatesAndEncryptsCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			w.Header().Set("Content-Type", "application/json")
			if r.Form.Get("refresh_token") != "refresh-input" || r.Form.Get("client_id") != testMicrosoftClientID {
				t.Fatalf("token request contained unexpected credential fields")
			}
			if scope := r.Form.Get("scope"); scope != graphDefaultScope {
				t.Fatalf("token request scope = %q", scope)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-new", "refresh_token": "refresh-new", "token_type": "Bearer",
				"expires_in": 3600, "scope": strings.Join(requestedScopes, " "),
			})
		case "/graph/me":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"id": "microsoft-user-1", "mail": "user@outlook.com", "displayName": "User",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service, store := newTestService(t, Options{
		AuthorityBaseURL: server.URL, GraphBaseURL: server.URL + "/graph", HTTPClient: server.Client(),
	})
	job, err := service.StartOAuthImport(context.Background(), []OAuthImportInput{{
		Email: "user@outlook.com", ClientID: testMicrosoftClientID, RefreshToken: "refresh-input",
	}}, false)
	if err != nil {
		t.Fatalf("StartOAuthImport() error = %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for job.State != "completed" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		job, err = service.GetOAuthImport(context.Background(), job.ID)
		if err != nil {
			t.Fatalf("GetOAuthImport() error = %v", err)
		}
	}
	if job.Created != 1 || job.Failed != 0 || len(job.Items) != 1 || job.Items[0].State != "created" {
		t.Fatalf("OAuth import job = %+v", job)
	}
	var accountID int64
	var refreshCipher, storedClientID string
	var authMethod AuthMethod
	if err := store.DB.QueryRow(`
		SELECT a.id, t.refresh_token_ciphertext, t.oauth_client_id, a.auth_method FROM accounts a
		JOIN account_tokens t ON t.account_id = a.id WHERE a.imported_email = 'user@outlook.com'
	`).Scan(&accountID, &refreshCipher, &storedClientID, &authMethod); err != nil {
		t.Fatalf("load imported token: %v", err)
	}
	refreshToken, err := service.keyring.OpenString(refreshCipher, tokenAssociatedData(accountID, "refresh"))
	if err != nil || refreshToken != "refresh-new" || storedClientID != testMicrosoftClientID || authMethod != AuthMethodOAuth {
		t.Fatalf("stored credential = %q, %q, %q, %v", refreshToken, storedClientID, authMethod, err)
	}
	var staged sql.NullString
	if err := store.DB.QueryRow(`SELECT refresh_token_ciphertext FROM oauth_import_items WHERE job_public_id = ?`, job.ID).Scan(&staged); err != nil {
		t.Fatalf("load staged credential: %v", err)
	}
	if staged.Valid {
		t.Fatal("completed OAuth import retained staged refresh token")
	}
}

func TestOAuthImportAcceptsFullyQualifiedGraphScopes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-new", "token_type": "Bearer", "expires_in": 3600,
				"scope": "https://graph.microsoft.com/User.Read https://graph.microsoft.com/Mail.ReadWrite",
			})
		case "/graph/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "microsoft-user-1", "mail": "user@outlook.com"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service, store := newTestService(t, Options{AuthorityBaseURL: server.URL, GraphBaseURL: server.URL + "/graph", HTTPClient: server.Client()})
	job, err := service.StartOAuthImport(context.Background(), []OAuthImportInput{{Email: "user@outlook.com", ClientID: testMicrosoftClientID, RefreshToken: "refresh-input"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	job = waitOAuthImport(t, service, job.ID)
	if job.Created != 1 || job.Failed != 0 {
		t.Fatalf("OAuth import job = %+v", job)
	}
	var scopes string
	if err := store.DB.QueryRow(`SELECT granted_scopes FROM account_tokens`).Scan(&scopes); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(scopes, "https://graph.microsoft.com/User.Read") || !strings.Contains(scopes, "https://graph.microsoft.com/Mail.ReadWrite") {
		t.Fatalf("stored scopes = %q", scopes)
	}
}

func TestOAuthImportUsesGraphDefaultForLegacyRefreshToken(t *testing.T) {
	var tokenRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			tokenRequests.Add(1)
			if r.Form.Get("scope") != graphDefaultScope {
				t.Fatalf("scope = %q", r.Form.Get("scope"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "graph-access", "refresh_token": "graph-refresh",
				"token_type": "Bearer", "expires_in": 3600,
			})
		case "/graph/me":
			if r.Header.Get("Authorization") != "Bearer graph-access" {
				t.Fatalf("Graph authorization = %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "microsoft-user-legacy", "mail": "user@outlook.com"})
		case "/graph/me/mailFolders/inbox/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service, _ := newTestService(t, Options{AuthorityBaseURL: server.URL, GraphBaseURL: server.URL + "/graph", HTTPClient: server.Client()})
	job, err := service.StartOAuthImport(context.Background(), []OAuthImportInput{{Email: "user@outlook.com", ClientID: testMicrosoftClientID, RefreshToken: "legacy-refresh"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	job = waitOAuthImport(t, service, job.ID)
	if job.Created != 1 || job.Failed != 0 || tokenRequests.Load() != 1 {
		t.Fatalf("OAuth import job = %+v, token requests = %d", job, tokenRequests.Load())
	}
}

func TestOAuthImportUpgradesPendingBasicAccountWithoutOverwrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "graph-access", "refresh_token": "graph-refresh",
				"token_type": "Bearer", "expires_in": 3600, "scope": "Mail.ReadWrite",
			})
		case "/graph/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "microsoft-user-basic", "mail": "user@outlook.com"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service, store := newTestService(t, Options{AuthorityBaseURL: server.URL, GraphBaseURL: server.URL + "/graph", HTTPClient: server.Client()})
	if _, err := service.Import(context.Background(), "user@outlook.com"); err != nil {
		t.Fatal(err)
	}
	job, err := service.StartOAuthImport(context.Background(), []OAuthImportInput{{Email: "user@outlook.com", ClientID: testMicrosoftClientID, RefreshToken: "refresh-input"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	job = waitOAuthImport(t, service, job.ID)
	if job.Updated != 1 || job.Created != 0 || job.Skipped != 0 || job.Failed != 0 {
		t.Fatalf("OAuth import job = %+v", job)
	}
	var accounts, tokens int
	_ = store.DB.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&accounts)
	_ = store.DB.QueryRow(`SELECT COUNT(*) FROM account_tokens`).Scan(&tokens)
	if accounts != 1 || tokens != 1 {
		t.Fatalf("accounts = %d, tokens = %d", accounts, tokens)
	}
}

func TestOAuthImportAcceptsMailReadScopeForReadOnlySync(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "read-access", "refresh_token": "read-refresh",
				"token_type": "Bearer", "expires_in": 3600, "scope": "Mail.Read",
			})
		case "/graph/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "microsoft-user-read", "mail": "user@outlook.com"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service, _ := newTestService(t, Options{AuthorityBaseURL: server.URL, GraphBaseURL: server.URL + "/graph", HTTPClient: server.Client()})
	job, err := service.StartOAuthImport(context.Background(), []OAuthImportInput{{Email: "user@outlook.com", ClientID: testMicrosoftClientID, RefreshToken: "read-refresh"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	job = waitOAuthImport(t, service, job.ID)
	if job.Created != 1 || job.Failed != 0 {
		t.Fatalf("OAuth import job = %+v", job)
	}
}

func TestOAuthEndpointsSeparateDeviceAndImportedGraphFlows(t *testing.T) {
	service, _ := newTestService(t, Options{})
	if service.oauthEndpoint.TokenURL != defaultAuthorityBaseURL+"/token" {
		t.Fatalf("device token endpoint = %q", service.oauthEndpoint.TokenURL)
	}
	if service.graphOAuthEndpoint.TokenURL != defaultGraphAuthorityBaseURL+"/token" {
		t.Fatalf("Graph token endpoint = %q", service.graphOAuthEndpoint.TokenURL)
	}
	if service.manager.endpoint.TokenURL != service.graphOAuthEndpoint.TokenURL {
		t.Fatalf("token manager endpoint = %q", service.manager.endpoint.TokenURL)
	}
}

func TestOAuthImportUsesMailboxIdentityWhenUserReadIsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "mail-access", "refresh_token": "mail-refresh",
				"token_type": "Bearer", "expires_in": 3600, "scope": graphDefaultScope,
			})
		case "/graph/me":
			w.WriteHeader(http.StatusUnauthorized)
		case "/graph/me/mailFolders/inbox/messages":
			if r.Header.Get("Authorization") != "Bearer mail-access" {
				t.Fatalf("mailbox authorization = %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service, store := newTestService(t, Options{AuthorityBaseURL: server.URL, GraphBaseURL: server.URL + "/graph", HTTPClient: server.Client()})
	job, err := service.StartOAuthImport(context.Background(), []OAuthImportInput{{Email: "user@outlook.com", ClientID: testMicrosoftClientID, RefreshToken: "mail-refresh"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	job = waitOAuthImport(t, service, job.ID)
	if job.Created != 1 || job.Failed != 0 {
		t.Fatalf("OAuth import job = %+v", job)
	}
	var microsoftID string
	if err := store.DB.QueryRow(`SELECT microsoft_user_id FROM accounts WHERE imported_email = 'user@outlook.com'`).Scan(&microsoftID); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(microsoftID, "imported-email:") || strings.Contains(microsoftID, "user@outlook.com") {
		t.Fatalf("synthetic Microsoft ID = %q", microsoftID)
	}
}

func TestOAuthImportExplainsPOPIMAPOnlyCredential(t *testing.T) {
	const refreshSecret = "refresh-must-not-leak"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-new", "refresh_token": "refresh-new", "token_type": "Bearer", "expires_in": 3600,
			"scope": "https://outlook.office.com/IMAP.AccessAsUser.All https://outlook.office.com/POP.AccessAsUser.All offline_access",
		})
	}))
	defer server.Close()
	service, _ := newTestService(t, Options{AuthorityBaseURL: server.URL, GraphBaseURL: server.URL + "/graph", HTTPClient: server.Client()})
	job, err := service.StartOAuthImport(context.Background(), []OAuthImportInput{{Email: "user@outlook.com", ClientID: testMicrosoftClientID, RefreshToken: refreshSecret}}, false)
	if err != nil {
		t.Fatal(err)
	}
	job = waitOAuthImport(t, service, job.ID)
	if job.Failed != 1 || len(job.Items) != 1 || job.Items[0].ErrorCode != "pop_imap_only" || !strings.Contains(job.Items[0].Message, "POP/IMAP") {
		t.Fatalf("OAuth import job = %+v", job)
	}
	if strings.Contains(fmt.Sprintf("%+v", job), refreshSecret) {
		t.Fatal("OAuth import result leaked the refresh token")
	}
}

func TestOAuthImportExplainsRejectedRefreshTokenWithoutLeakingProviderDetails(t *testing.T) {
	const providerSecret = "provider-detail-must-not-leak"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "invalid_grant", "error_description": "expired " + providerSecret,
		})
	}))
	defer server.Close()
	service, _ := newTestService(t, Options{AuthorityBaseURL: server.URL, GraphBaseURL: server.URL + "/graph", HTTPClient: server.Client()})
	job, err := service.StartOAuthImport(context.Background(), []OAuthImportInput{{Email: "user@outlook.com", ClientID: testMicrosoftClientID, RefreshToken: "refresh-input"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	job = waitOAuthImport(t, service, job.ID)
	if job.Failed != 1 || len(job.Items) != 1 || job.Items[0].ErrorCode != "refresh_token_rejected" || !strings.Contains(job.Items[0].Message, "重新生成") {
		t.Fatalf("OAuth import job = %+v", job)
	}
	if strings.Contains(fmt.Sprintf("%+v", job), providerSecret) {
		t.Fatal("OAuth import result leaked the provider error description")
	}
}

func waitOAuthImport(t *testing.T, service *Service, jobID string) OAuthImportJob {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	job, err := service.GetOAuthImport(context.Background(), jobID)
	for err == nil && job.State != "completed" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		job, err = service.GetOAuthImport(context.Background(), jobID)
	}
	if err != nil {
		t.Fatalf("GetOAuthImport() error = %v", err)
	}
	return job
}

func TestOAuthImportsShareFourValidationSlotsAcrossJobs(t *testing.T) {
	var active, maximum atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			current := active.Add(1)
			for {
				previous := maximum.Load()
				if current <= previous || maximum.CompareAndSwap(previous, current) {
					break
				}
			}
			time.Sleep(40 * time.Millisecond)
			active.Add(-1)
			index := strings.TrimPrefix(r.Form.Get("refresh_token"), "refresh-")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-" + index, "refresh_token": "new-" + index, "token_type": "Bearer", "expires_in": 3600, "scope": strings.Join(requestedScopes, " ")})
		case "/graph/me":
			index := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer access-")
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "id-" + index, "mail": "user" + index + "@outlook.com", "displayName": "User " + index})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	service, _ := newTestService(t, Options{AuthorityBaseURL: server.URL, GraphBaseURL: server.URL + "/graph", HTTPClient: server.Client()})
	makeInputs := func(start int) []OAuthImportInput {
		items := make([]OAuthImportInput, 4)
		for index := range items {
			value := start + index
			items[index] = OAuthImportInput{Email: fmt.Sprintf("user%d@outlook.com", value), ClientID: testMicrosoftClientID, RefreshToken: fmt.Sprintf("refresh-%d", value)}
		}
		return items
	}
	first, err := service.StartOAuthImport(context.Background(), makeInputs(0), false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.StartOAuthImport(context.Background(), makeInputs(4), false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		first, _ = service.GetOAuthImport(context.Background(), first.ID)
		second, _ = service.GetOAuthImport(context.Background(), second.ID)
		if first.State == "completed" && second.State == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if first.Processed != 4 || second.Processed != 4 || maximum.Load() != oauthImportConcurrency {
		t.Fatalf("jobs = %+v %+v, maximum concurrency = %d", first, second, maximum.Load())
	}
}

func TestUpdateAccountReplacesMetadata(t *testing.T) {
	service, _ := newTestService(t, Options{})
	if _, err := service.Import(context.Background(), "old@outlook.com,Old,old-tag,Old note"); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	items, _ := service.List(context.Background(), "")
	if len(items) != 1 || items[0].AuthMethod != AuthMethodWeb {
		t.Fatalf("basic import auth method = %+v", items)
	}
	updated, err := service.UpdateAccount(context.Background(), items[0].PublicID, AccountUpdate{
		ImportedEmail: "new@outlook.com", Notes: "New note", Groups: []string{"Finance"}, Tags: []string{"important"},
	})
	if err != nil {
		t.Fatalf("UpdateAccount() error = %v", err)
	}
	if updated.ImportedEmail != "new@outlook.com" || updated.AuthMethod != AuthMethodWeb || updated.Notes != "New note" || !containsName(updated.Groups, "Finance") || !containsName(updated.Tags, "important") {
		t.Fatalf("updated account = %+v", updated)
	}
}

func TestUpdateAccountRejectsDuplicateImportedEmail(t *testing.T) {
	service, _ := newTestService(t, Options{})
	if _, err := service.Import(context.Background(), "first@outlook.com\nsecond@outlook.com"); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	items, _ := service.List(context.Background(), "")
	var second Account
	for _, item := range items {
		if item.ImportedEmail == "second@outlook.com" {
			second = item
		}
	}
	_, err := service.UpdateAccount(context.Background(), second.PublicID, AccountUpdate{ImportedEmail: "first@outlook.com"})
	var validationError *ImportValidationError
	if !errors.As(err, &validationError) {
		t.Fatalf("duplicate email error = %v", err)
	}
}

const (
	testMicrosoftClientID   = "11111111-2222-4333-8444-555555555555"
	secondMicrosoftClientID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
)

func TestMicrosoftConfigPersistsAndRejectsInvalidClientID(t *testing.T) {
	service, store := newTestService(t, Options{ClientID: testMicrosoftClientID})

	config, err := service.GetMicrosoftConfig(context.Background())
	if err != nil {
		t.Fatalf("GetMicrosoftConfig() error = %v", err)
	}
	if !config.MicrosoftConfigured || config.ClientID != testMicrosoftClientID || config.UpdatedAt == "" {
		t.Fatalf("initial Microsoft config = %+v", config)
	}

	updated, err := service.UpdateMicrosoftConfig(context.Background(), strings.ToUpper(secondMicrosoftClientID))
	if err != nil {
		t.Fatalf("UpdateMicrosoftConfig() error = %v", err)
	}
	if updated.ClientID != secondMicrosoftClientID {
		t.Fatalf("updated client ID = %q", updated.ClientID)
	}
	if _, err := service.UpdateMicrosoftConfig(context.Background(), "not-a-guid"); !errors.Is(err, ErrInvalidMicrosoftClientID) {
		t.Fatalf("invalid UpdateMicrosoftConfig() error = %v", err)
	}

	restarted, err := New(store.DB, Options{Keyring: service.keyring, ClientID: testMicrosoftClientID})
	if err != nil {
		t.Fatalf("New(restarted) error = %v", err)
	}
	defer restarted.Close()
	reloaded, err := restarted.GetMicrosoftConfig(context.Background())
	if err != nil {
		t.Fatalf("restarted GetMicrosoftConfig() error = %v", err)
	}
	if reloaded.ClientID != secondMicrosoftClientID {
		t.Fatalf("persisted client ID = %q, want %q", reloaded.ClientID, secondMicrosoftClientID)
	}

	var auditCount int
	if err := store.DB.QueryRow(`
		SELECT COUNT(*) FROM audit_events WHERE event_type = 'microsoft_oauth_config_updated'
	`).Scan(&auditCount); err != nil {
		t.Fatalf("count Microsoft config audit events: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("Microsoft config audit events = %d, want 1", auditCount)
	}
}

func TestMicrosoftConfigBootstrapBackfillsLegacyTokens(t *testing.T) {
	service, store := newTestService(t, Options{})
	if _, err := service.Import(context.Background(), "user@outlook.com"); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	items, _ := service.List(context.Background(), "")
	accountID, _, _, err := service.accountIdentity(context.Background(), items[0].PublicID)
	if err != nil {
		t.Fatalf("accountIdentity() error = %v", err)
	}
	accessCipher, err := service.keyring.SealString("access-old", tokenAssociatedData(accountID, "access"))
	if err != nil {
		t.Fatalf("SealString(access) error = %v", err)
	}
	refreshCipher, err := service.keyring.SealString("refresh-old", tokenAssociatedData(accountID, "refresh"))
	if err != nil {
		t.Fatalf("SealString(refresh) error = %v", err)
	}
	now := time.Now().UTC()
	if _, err := store.DB.Exec(`
		INSERT INTO account_tokens (
			account_id, access_token_ciphertext, access_expires_at_utc,
			refresh_token_ciphertext, token_type, granted_scopes,
			created_at_utc, updated_at_utc
		) VALUES (?, ?, ?, ?, 'Bearer', ?, ?, ?)
	`, accountID, accessCipher, formatTime(now.Add(time.Hour)), refreshCipher,
		strings.Join(requestedScopes, " "), formatTime(now), formatTime(now)); err != nil {
		t.Fatalf("insert legacy account token: %v", err)
	}

	bootstrapped, err := New(store.DB, Options{Keyring: service.keyring, ClientID: testMicrosoftClientID})
	if err != nil {
		t.Fatalf("New(bootstrapped) error = %v", err)
	}
	defer bootstrapped.Close()
	var tokenClientID string
	if err := store.DB.QueryRow(
		"SELECT oauth_client_id FROM account_tokens WHERE account_id = ?", accountID,
	).Scan(&tokenClientID); err != nil {
		t.Fatalf("load backfilled token client ID: %v", err)
	}
	if tokenClientID != testMicrosoftClientID {
		t.Fatalf("backfilled token client ID = %q", tokenClientID)
	}
}

func TestImportAccountsWithGroupsTagsAndNotes(t *testing.T) {
	service, _ := newTestService(t, Options{})
	result, err := service.Import(context.Background(), `email,group,tags,notes
First@outlook.com,Personal,verification|important,Primary account
second@hotmail.com,,,`)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if result.Created != 2 || result.Existing != 0 {
		t.Fatalf("Import() = %+v", result)
	}
	result, err = service.Import(context.Background(), "first@outlook.com,Archive,new-tag,Updated note")
	if err != nil {
		t.Fatalf("Import(existing) error = %v", err)
	}
	if result.Created != 0 || result.Existing != 1 {
		t.Fatalf("Import(existing) = %+v", result)
	}

	items, err := service.List(context.Background(), "pending")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("accounts = %d, want 2", len(items))
	}
	var first Account
	for _, item := range items {
		if item.ImportedEmail == "first@outlook.com" {
			first = item
		}
	}
	if first.PublicID == "" || first.Notes != "Updated note" {
		t.Fatalf("first account = %+v", first)
	}
	if !containsName(first.Groups, "Personal") || !containsName(first.Groups, "Archive") {
		t.Fatalf("groups = %v", first.Groups)
	}
	if !containsName(first.Tags, "verification") || !containsName(first.Tags, "important") || !containsName(first.Tags, "new-tag") {
		t.Fatalf("tags = %v", first.Tags)
	}
}

func TestImportRejectsPasswordColumnsAndInvalidAddresses(t *testing.T) {
	service, _ := newTestService(t, Options{})
	if _, err := service.Import(context.Background(), "email,password\nuser@outlook.com,secret"); err == nil || !strings.Contains(err.Error(), "密码") {
		t.Fatalf("password import error = %v", err)
	}
	if _, err := service.Import(context.Background(), "Name <user@outlook.com>"); err == nil {
		t.Fatal("Import() accepted a display-name address")
	}
}

func TestImportMapsOptionalHeaderColumns(t *testing.T) {
	service, _ := newTestService(t, Options{})
	if _, err := service.Import(context.Background(), "email,notes\nuser@outlook.com,Primary account"); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	items, err := service.List(context.Background(), "pending")
	if err != nil || len(items) != 1 {
		t.Fatalf("accounts = %v, error = %v", items, err)
	}
	if items[0].Notes != "Primary account" || len(items[0].Groups) != 0 {
		t.Fatalf("optional columns were mapped incorrectly: %+v", items[0])
	}
}

func TestListAccountsSearchesMetadataAndPaginates(t *testing.T) {
	service, store := newTestService(t, Options{})
	data := "email,group,tags,notes\nalpha@outlook.com,Finance,priority,Billing owner\nbeta@outlook.com,Personal,archive,Contains %_ marker\ngamma@outlook.com,Finance,archive,General"
	if _, err := service.Import(context.Background(), data); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if _, err := store.DB.Exec(`UPDATE accounts SET display_name = 'Alpha Account', primary_email = 'alpha.alias@outlook.com' WHERE imported_email = 'alpha@outlook.com'`); err != nil {
		t.Fatalf("update account metadata: %v", err)
	}

	result, err := service.ListAccounts(context.Background(), AccountListOptions{Query: "Finance", Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("ListAccounts() error = %v", err)
	}
	if result.Total != 2 || len(result.Accounts) != 1 || result.PageSize != 1 || result.StatusCounts["pending"] != 2 {
		t.Fatalf("paged account result = %+v", result)
	}
	result, err = service.ListAccounts(context.Background(), AccountListOptions{Query: "alpha.alias"})
	if err != nil || len(result.Accounts) != 1 || result.Accounts[0].DisplayName != "Alpha Account" {
		t.Fatalf("primary email search = %+v, error = %v", result, err)
	}
	result, err = service.ListAccounts(context.Background(), AccountListOptions{Query: "%_"})
	if err != nil || len(result.Accounts) != 1 || result.Accounts[0].ImportedEmail != "beta@outlook.com" {
		t.Fatalf("escaped wildcard search = %+v, error = %v", result, err)
	}
}

func TestListAccountsAuthMethodCountsFollowStatusFilter(t *testing.T) {
	service, store := newTestService(t, Options{})
	if _, err := service.Import(context.Background(), "web@outlook.com\noauth@outlook.com"); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if _, err := store.DB.Exec(`
		UPDATE accounts SET status = CASE imported_email
			WHEN 'web@outlook.com' THEN 'active'
			ELSE 'disabled'
		END,
		auth_method = CASE imported_email
			WHEN 'web@outlook.com' THEN 'web'
			ELSE 'oauth'
		END
	`); err != nil {
		t.Fatalf("seed account types: %v", err)
	}

	result, err := service.ListAccounts(context.Background(), AccountListOptions{Status: "active", Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("ListAccounts() error = %v", err)
	}
	if result.Total != 1 || result.AuthMethodCounts[string(AuthMethodWeb)] != 1 || result.AuthMethodCounts[string(AuthMethodOAuth)] != 0 {
		t.Fatalf("filtered auth method counts = %+v", result)
	}
}

func TestSelectAccountIDsReturnsOneThousandMatchingAccounts(t *testing.T) {
	service, _ := newTestService(t, Options{})
	var input strings.Builder
	input.WriteString("email,group\n")
	for index := 0; index < maxImportRows; index++ {
		fmt.Fprintf(&input, "bulk-%04d@outlook.com,Bulk selection\n", index)
	}
	if _, err := service.Import(context.Background(), input.String()); err != nil {
		t.Fatalf("Import() error = %v", err)
	}

	ids, err := service.SelectAccountIDs(context.Background(), "Bulk selection", "pending")
	if err != nil {
		t.Fatalf("SelectAccountIDs() error = %v", err)
	}
	if len(ids) != maxImportRows {
		t.Fatalf("selected accounts = %d, want %d", len(ids), maxImportRows)
	}
	seen := make(map[string]struct{}, len(ids))
	for _, publicID := range ids {
		if _, exists := seen[publicID]; exists {
			t.Fatalf("duplicate account ID %q", publicID)
		}
		seen[publicID] = struct{}{}
	}
}

func TestBatchStatusRestoresStoredTokenWithoutImmediateReauthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graph/me" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "stable-user", "mail": "user@outlook.com"})
	}))
	defer server.Close()
	service, store := newTestService(t, Options{GraphBaseURL: server.URL + "/graph", HTTPClient: server.Client()})
	seedStoredToken(t, service, store.DB, time.Now().Add(time.Hour), "access", "refresh")
	items, _ := service.List(context.Background(), "")
	publicID := items[0].PublicID

	if _, err := service.SetDisabledBatch(context.Background(), []string{publicID}, true); err != nil {
		t.Fatalf("SetDisabledBatch(true) error = %v", err)
	}
	result, err := service.SetDisabledBatch(context.Background(), []string{publicID, publicID, "missing"}, false)
	if err != nil {
		t.Fatalf("SetDisabledBatch(false) error = %v", err)
	}
	if result.Requested != 2 || result.Succeeded != 1 || result.Failed != 1 || result.Results[0].Status != "active" {
		t.Fatalf("batch enable result = %+v", result)
	}
	var status string
	if err := store.DB.QueryRow(`SELECT status FROM accounts WHERE public_id = ?`, publicID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status == "reauth_required" {
		t.Fatal("enabled account was forced into reauthorization")
	}
}

func TestDeleteBatchRemovesFoundAccountsAndReportsMissing(t *testing.T) {
	service, store := newTestService(t, Options{})
	if _, err := service.Import(context.Background(), "first@outlook.com\nsecond@outlook.com"); err != nil {
		t.Fatal(err)
	}
	items, _ := service.List(context.Background(), "")
	result, err := service.DeleteBatch(context.Background(), []string{items[0].PublicID, "missing", items[1].PublicID})
	if err != nil {
		t.Fatalf("DeleteBatch() error = %v", err)
	}
	if result.Succeeded != 2 || result.Failed != 1 || result.Results[1].Error != "not_found" {
		t.Fatalf("delete batch result = %+v", result)
	}
	var accountCount, auditCount int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&accountCount); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event_type = 'account_deleted'`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if accountCount != 0 || auditCount != 2 {
		t.Fatalf("accounts = %d, audits = %d", accountCount, auditCount)
	}
}

func TestDisableAndEnableAccount(t *testing.T) {
	service, _ := newTestService(t, Options{})
	if _, err := service.Import(context.Background(), "user@outlook.com"); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	items, err := service.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if err := service.SetDisabled(context.Background(), items[0].PublicID, true); err != nil {
		t.Fatalf("SetDisabled(true) error = %v", err)
	}
	disabled, err := service.List(context.Background(), "disabled")
	if err != nil || len(disabled) != 1 {
		t.Fatalf("disabled accounts = %v, error = %v", disabled, err)
	}
	if err := service.SetDisabled(context.Background(), items[0].PublicID, false); err != nil {
		t.Fatalf("SetDisabled(false) error = %v", err)
	}
	pending, err := service.List(context.Background(), "pending")
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending accounts = %v, error = %v", pending, err)
	}
}

func TestDeleteAccountRemovesLocalDataAndKeepsAudit(t *testing.T) {
	service, store := newTestService(t, Options{ClientID: testMicrosoftClientID})
	accountID := seedStoredToken(t, service, store.DB, time.Now().Add(time.Hour), "access", "refresh")
	items, err := service.List(context.Background(), "")
	if err != nil || len(items) != 1 {
		t.Fatalf("List() = %v, error = %v", items, err)
	}
	now := formatTime(time.Now().UTC())
	result, err := store.DB.Exec(`
		INSERT INTO folders (account_id, graph_id, well_known_name, display_name, created_at_utc, updated_at_utc)
		VALUES (?, 'inbox', 'inbox', 'Inbox', ?, ?)
	`, accountID, now, now)
	if err != nil {
		t.Fatalf("insert folder: %v", err)
	}
	folderID, _ := result.LastInsertId()
	result, err = store.DB.Exec(`
		INSERT INTO messages (
			public_id, account_id, folder_id, immutable_id, subject, received_at_utc, created_at_utc, updated_at_utc
		) VALUES ('msg_delete_test', ?, ?, 'immutable-delete-test', 'Delete test', ?, ?, ?)
	`, accountID, folderID, now, now, now)
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}
	messageID, _ := result.LastInsertId()
	if _, err := store.DB.Exec(`
		INSERT INTO cleanup_actions (public_id, message_id, candidate_reason, created_at_utc, updated_at_utc)
		VALUES ('clean_delete_test', ?, 'test', ?, ?)
	`, messageID, now, now); err != nil {
		t.Fatalf("insert cleanup action: %v", err)
	}
	result, err = store.DB.Exec(`
		INSERT INTO notification_channels (
			public_id, name, kind, config_ciphertext, created_at_utc, updated_at_utc
		) VALUES ('channel_delete_test', 'Test', 'webhook', 'ciphertext', ?, ?)
	`, now, now)
	if err != nil {
		t.Fatalf("insert notification channel: %v", err)
	}
	channelID, _ := result.LastInsertId()
	if _, err := store.DB.Exec(`
		INSERT INTO notification_deliveries (
			public_id, message_id, channel_id, event_type, dedupe_key, payload_json, created_at_utc, updated_at_utc
		) VALUES ('delivery_delete_test', ?, ?, 'mail', 'delete-test', '{}', ?, ?)
	`, messageID, channelID, now, now); err != nil {
		t.Fatalf("insert notification delivery: %v", err)
	}
	if err := service.Delete(context.Background(), items[0].PublicID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	for _, table := range []string{"accounts", "account_tokens"} {
		var count int
		if err := store.DB.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE "+map[string]string{
			"accounts": "id", "account_tokens": "account_id",
		}[table]+" = ?", accountID).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows = %d, want 0", table, count)
		}
	}
	for table, publicID := range map[string]string{
		"messages": "msg_delete_test", "cleanup_actions": "clean_delete_test",
		"notification_deliveries": "delivery_delete_test",
	} {
		var count int
		if err := store.DB.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE public_id = ?", publicID).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows = %d, want 0", table, count)
		}
	}
	var indexed int
	if err := store.DB.QueryRow("SELECT COUNT(*) FROM message_fts WHERE rowid = ?", messageID).Scan(&indexed); err != nil {
		t.Fatalf("count message FTS row: %v", err)
	}
	if indexed != 0 {
		t.Fatalf("message FTS rows = %d, want 0", indexed)
	}
	var auditCount int
	if err := store.DB.QueryRow(`
		SELECT COUNT(*) FROM audit_events WHERE event_type = 'account_deleted'
	`).Scan(&auditCount); err != nil {
		t.Fatalf("count account deletion audit: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("account deletion audit events = %d, want 1", auditCount)
	}
	if err := service.Delete(context.Background(), items[0].PublicID); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("Delete(missing) error = %v", err)
	}
}

func newTestService(t *testing.T, options Options) (*Service, *database.Store) {
	t.Helper()
	store, err := database.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	if options.Keyring == nil {
		options.Keyring = datakey.New(nil)
		if err := options.Keyring.Unlock(make([]byte, 32)); err != nil {
			store.Close()
			t.Fatalf("unlock test data key: %v", err)
		}
	}
	service, err := New(store.DB, options)
	if err != nil {
		store.Close()
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		service.Close()
		store.Close()
	})
	return service, store
}

func containsName(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
