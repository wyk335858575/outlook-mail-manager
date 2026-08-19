package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"outlook-mail-manager/internal/apitoken"
	"outlook-mail-manager/internal/database"
)

type fakeExternalTokenService struct{ grant apitoken.Grant }

func (s *fakeExternalTokenService) Create(context.Context, apitoken.TokenInput) (apitoken.CreatedToken, error) {
	return apitoken.CreatedToken{}, nil
}
func (s *fakeExternalTokenService) List(context.Context) ([]apitoken.Token, error) { return nil, nil }
func (s *fakeExternalTokenService) Revoke(context.Context, string) error           { return nil }
func (s *fakeExternalTokenService) Verify(context.Context, string, string, string) (apitoken.Grant, error) {
	return s.grant, nil
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
	if mail.filter.Limit != 0 {
		t.Fatalf("mail search was called with filter %#v", mail.filter)
	}
}
