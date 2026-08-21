package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"outlook-mail-manager/internal/apitoken"
	"outlook-mail-manager/internal/database"
)

type fakeExternalTokenService struct {
	grant       apitoken.Grant
	verifyError error
	secrets     []string
}

func (s *fakeExternalTokenService) Create(context.Context, apitoken.TokenInput) (apitoken.CreatedToken, error) {
	return apitoken.CreatedToken{}, nil
}
func (s *fakeExternalTokenService) List(context.Context) ([]apitoken.Token, error) { return nil, nil }
func (s *fakeExternalTokenService) Revoke(context.Context, string) error           { return nil }
func (s *fakeExternalTokenService) Delete(context.Context, string) error           { return nil }
func (s *fakeExternalTokenService) Verify(_ context.Context, secret, _, _ string) (apitoken.Grant, error) {
	s.secrets = append(s.secrets, secret)
	return s.grant, s.verifyError
}

func TestExternalAPIAuthorizeAcceptsAccessTokenQueryForGET(t *testing.T) {
	tokens := &fakeExternalTokenService{grant: apitoken.Grant{TokenPublicID: "token_1"}}
	api := newExternalAPI(nil, tokens, nil, nil, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/accounts?access_token=omm_query_secret", nil)
	request.RemoteAddr = "127.0.0.1:4321"
	response := httptest.NewRecorder()

	if _, ok := api.authorize(response, request, "accounts:read", 60); !ok {
		t.Fatalf("authorize() rejected query token: %d %s", response.Code, response.Body.String())
	}
	if len(tokens.secrets) != 1 || tokens.secrets[0] != "omm_query_secret" {
		t.Fatalf("verified secrets = %#v", tokens.secrets)
	}
}

func TestExternalAPIAuthorizePrefersAuthorizationHeader(t *testing.T) {
	tokens := &fakeExternalTokenService{grant: apitoken.Grant{TokenPublicID: "token_1"}}
	api := newExternalAPI(nil, tokens, nil, nil, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/accounts?access_token=omm_query_secret", nil)
	request.Header.Set("Authorization", "Bearer omm_header_secret")
	request.RemoteAddr = "127.0.0.1:4321"
	response := httptest.NewRecorder()

	if _, ok := api.authorize(response, request, "accounts:read", 60); !ok {
		t.Fatalf("authorize() rejected bearer token: %d %s", response.Code, response.Body.String())
	}
	if len(tokens.secrets) != 1 || tokens.secrets[0] != "omm_header_secret" {
		t.Fatalf("verified secrets = %#v", tokens.secrets)
	}
}

func TestExternalAPIAuthorizeDoesNotFallbackFromInvalidHeaderOrNonGET(t *testing.T) {
	for _, test := range []struct {
		name   string
		method string
		header string
	}{
		{name: "invalid header", method: http.MethodGet, header: "Basic invalid"},
		{name: "non GET", method: http.MethodPost},
	} {
		t.Run(test.name, func(t *testing.T) {
			tokens := &fakeExternalTokenService{grant: apitoken.Grant{TokenPublicID: "token_1"}}
			api := newExternalAPI(nil, tokens, nil, nil, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
			request := httptest.NewRequest(test.method, "/api/v1/accounts?access_token=omm_query_secret", nil)
			if test.header != "" {
				request.Header.Set("Authorization", test.header)
			}
			response := httptest.NewRecorder()

			if _, ok := api.authorize(response, request, "accounts:read", 60); ok {
				t.Fatal("authorize() accepted unsupported credentials")
			}
			if len(tokens.secrets) != 0 {
				t.Fatalf("verified secrets = %#v", tokens.secrets)
			}
			if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), "omm_query_secret") {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestExternalAPIAuthorizeDoesNotEchoRejectedQueryToken(t *testing.T) {
	tokens := &fakeExternalTokenService{verifyError: errors.New("rejected omm_query_secret")}
	api := newExternalAPI(nil, tokens, nil, nil, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/accounts?access_token=omm_query_secret", nil)
	response := httptest.NewRecorder()

	if _, ok := api.authorize(response, request, "accounts:read", 60); ok {
		t.Fatal("authorize() accepted rejected token")
	}
	if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), "omm_query_secret") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestExternalMessagesReturnsEmptyWhenTokenScopeResolvesToNoAccounts(t *testing.T) {
	store, err := database.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	defer store.Close()
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := store.DB.Exec(`
		INSERT INTO accounts (public_id, imported_email, status, created_at_utc, updated_at_utc)
		VALUES ('acc_1', 'one@example.com', 'active', ?, ?)
	`, now, now); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	mail := &fakeMailService{}
	tokens := &fakeExternalTokenService{grant: apitoken.Grant{TokenPublicID: "token_1", Scopes: []string{"mail:read"}, GroupNames: []string{"missing"}}}
	api := newExternalAPI(store.DB, tokens, mail, nil, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/messages", nil)
	request.Header.Set("Authorization", "Bearer omm_test")
	request.RemoteAddr = "127.0.0.1:4321"
	response := httptest.NewRecorder()

	api.messages(response, request)

	if response.Code != http.StatusOK || response.Body.String() != "{\"messages\":[],\"next_cursor\":\"\"}\n" {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
	if mail.filter.Limit != 0 {
		t.Fatalf("mail search was called with filter %#v", mail.filter)
	}
}

func TestExternalMessagesIgnoresLegacyTimeFilters(t *testing.T) {
	store, err := database.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mail := &fakeMailService{}
	tokens := &fakeExternalTokenService{grant: apitoken.Grant{TokenPublicID: "token_1", Scopes: []string{"mail:read"}, AccountPublicIDs: []string{"acc_1"}}}
	api := newExternalAPI(store.DB, tokens, mail, nil, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/messages?account=one@example.com&since=invalid&until=also-invalid", nil)
	request.Header.Set("Authorization", "Bearer omm_test")
	request.RemoteAddr = "127.0.0.1:4321"
	response := httptest.NewRecorder()

	if _, err := store.DB.Exec(`INSERT INTO accounts (public_id, imported_email, status, created_at_utc, updated_at_utc) VALUES ('acc_1', 'one@example.com', 'active', '2026-08-17T00:00:00Z', '2026-08-17T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	api.messages(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if mail.filter.Since != nil || mail.filter.Until != nil {
		t.Fatalf("legacy time filters were applied: %#v", mail.filter)
	}
	if mail.filter.Account != "acc_1" {
		t.Fatalf("account lookup was not normalized to public ID: %#v", mail.filter)
	}
}

func TestExternalLatestOTPUsesRecentWindowAndLatestMessage(t *testing.T) {
	ctx := context.Background()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if _, err := store.DB.Exec(`INSERT INTO accounts (public_id, imported_email, status, created_at_utc, updated_at_utc) VALUES ('acc_1', 'one@example.com', 'active', ?, ?)`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	var accountID int64
	if err := store.DB.QueryRow(`SELECT id FROM accounts WHERE public_id = 'acc_1'`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	result, err := store.DB.Exec(`INSERT INTO folders (account_id, graph_id, well_known_name, display_name, created_at_utc, updated_at_utc) VALUES (?, 'inbox', 'inbox', '收件箱', ?, ?)`, accountID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	folderID, _ := result.LastInsertId()
	insertMessage := func(publicID, code string, received time.Time) {
		t.Helper()
		_, err := store.DB.Exec(`INSERT INTO messages (public_id, account_id, folder_id, immutable_id, subject, sender_address, received_at_utc, verification_code, body_text, created_at_utc, updated_at_utc) VALUES (?, ?, ?, ?, '验证码', 'sender@example.com', ?, ?, ?, ?, ?)`, publicID, accountID, folderID, publicID+"-immutable", received.Format(time.RFC3339Nano), code, "验证码正文", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		if err != nil {
			t.Fatal(err)
		}
	}
	insertMessage("msg_latest", "654321", now.Add(-time.Minute))
	insertMessage("msg_older", "123456", now.Add(-10*time.Minute))
	insertMessage("msg_expired", "000000", now.Add(-16*time.Minute))

	mail := &fakeMailService{}
	tokens := &fakeExternalTokenService{grant: apitoken.Grant{TokenPublicID: "token_1", Scopes: []string{"otp:read"}, AccountPublicIDs: []string{"acc_1"}}}
	api := newExternalAPI(store.DB, tokens, mail, nil, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/otp/latest?account=acc_1&after=2099-01-01T00:00:00Z", nil)
	request.Header.Set("Authorization", "Bearer omm_test")
	request.RemoteAddr = "127.0.0.1:4321"
	response := httptest.NewRecorder()
	api.latestOTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "654321" || body["message_public_id"] != "msg_latest" {
		t.Fatalf("latest OTP response = %#v", body)
	}
	if _, err := store.DB.Exec(`UPDATE messages SET received_at_utc = ? WHERE public_id IN ('msg_latest', 'msg_older')`, now.Add(-16*time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	api.latestOTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expired response = %d %s", response.Code, response.Body.String())
	}
	body = map[string]any{}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != nil {
		t.Fatalf("expired OTP response = %#v", body)
	}
}

