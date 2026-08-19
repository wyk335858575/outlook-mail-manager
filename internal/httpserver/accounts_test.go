package httpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"outlook-mail-manager/internal/accounts"
	"outlook-mail-manager/internal/database"
)

func TestAccountsAPIRequiresSessionAndCSRF(t *testing.T) {
	ctx := context.Background()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	defer store.Close()
	now := time.Date(2026, time.August, 17, 8, 0, 0, 0, time.UTC)
	authService, keyring, grant := testAuthenticatedAuthService(t, store, func() time.Time { return now })
	accountService, err := accounts.New(store.DB, accounts.Options{Keyring: keyring})
	if err != nil {
		t.Fatalf("accounts.New() error = %v", err)
	}
	defer accountService.Close()
	handler := New(store.DB, slog.New(slog.NewTextHandler(testWriter{t}, nil)), testAssets(), authService, accountService, nil, nil, nil, nil)
	cookie := &http.Cookie{Name: authService.CookieName(), Value: grant.Token}

	response := performJSON(t, handler, http.MethodPost, "/api/accounts/import", map[string]string{
		"data": "user@outlook.com",
	}, cookie, "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("import without CSRF status = %d", response.Code)
	}
	response = performJSON(t, handler, http.MethodPost, "/api/accounts/import", map[string]string{
		"data": "user@outlook.com,Personal,verification,Primary",
	}, cookie, grant.CSRFToken)
	if response.Code != http.StatusCreated {
		t.Fatalf("import status = %d, body = %s", response.Code, response.Body.String())
	}

	response = performJSON(t, handler, http.MethodGet, "/api/accounts", nil, cookie, "")
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", response.Code, response.Body.String())
	}
	var list struct {
		Accounts []accounts.Account `json:"accounts"`
	}
	decodeResponse(t, response, &list)
	if len(list.Accounts) != 1 || list.Accounts[0].ImportedEmail != "user@outlook.com" {
		t.Fatalf("accounts = %+v", list.Accounts)
	}

	response = performJSON(t, handler, http.MethodPost,
		"/api/accounts/"+list.Accounts[0].PublicID+"/oauth/start", nil, cookie, grant.CSRFToken)
	if response.Code != http.StatusConflict {
		t.Fatalf("OAuth without MS_CLIENT_ID status = %d, body = %s", response.Code, response.Body.String())
	}

	response = performJSON(t, handler, http.MethodPut, "/api/accounts/config", map[string]string{
		"client_id": "not-a-guid",
	}, cookie, grant.CSRFToken)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_microsoft_client_id") {
		t.Fatalf("invalid Microsoft config status = %d, body = %s", response.Code, response.Body.String())
	}
	clientID := "11111111-2222-4333-8444-555555555555"
	response = performJSON(t, handler, http.MethodPut, "/api/accounts/config", map[string]string{
		"client_id": clientID,
	}, cookie, "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("Microsoft config without CSRF status = %d", response.Code)
	}
	response = performJSON(t, handler, http.MethodPut, "/api/accounts/config", map[string]string{
		"client_id": clientID,
	}, cookie, grant.CSRFToken)
	if response.Code != http.StatusOK {
		t.Fatalf("Microsoft config update status = %d, body = %s", response.Code, response.Body.String())
	}
	var config accounts.MicrosoftConfig
	if err := json.Unmarshal(response.Body.Bytes(), &config); err != nil {
		t.Fatalf("decode Microsoft config: %v", err)
	}
	if !config.MicrosoftConfigured || config.ClientID != clientID {
		t.Fatalf("Microsoft config = %+v", config)
	}

	secret := "must-not-be-accepted"
	response = performJSON(t, handler, http.MethodPost, "/api/accounts/oauth-imports", map[string]any{
		"accounts": []map[string]string{{
			"email": "user@outlook.com", "password": secret, "client_id": clientID, "refresh_token": "refresh-value",
		}},
	}, cookie, grant.CSRFToken)
	if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), secret) {
		t.Fatalf("OAuth import with password status = %d, body = %s", response.Code, response.Body.String())
	}

	response = performJSON(t, handler, http.MethodDelete,
		"/api/accounts/"+list.Accounts[0].PublicID, nil, cookie, "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("delete without CSRF status = %d", response.Code)
	}
	response = performJSON(t, handler, http.MethodDelete,
		"/api/accounts/"+list.Accounts[0].PublicID, nil, cookie, grant.CSRFToken)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestAccountsAPISearchSelectionAndBatchActions(t *testing.T) {
	ctx := context.Background()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, time.August, 19, 8, 0, 0, 0, time.UTC)
	authService, keyring, grant := testAuthenticatedAuthService(t, store, func() time.Time { return now })
	accountService, err := accounts.New(store.DB, accounts.Options{Keyring: keyring})
	if err != nil {
		t.Fatal(err)
	}
	defer accountService.Close()
	handler := New(store.DB, slog.New(slog.NewTextHandler(testWriter{t}, nil)), testAssets(), authService, accountService, nil, nil, nil, nil)
	cookie := &http.Cookie{Name: authService.CookieName(), Value: grant.Token}

	response := performJSON(t, handler, http.MethodPost, "/api/accounts/import", map[string]string{
		"data": "email,group\nalpha@outlook.com,Finance\nbeta@outlook.com,Personal",
	}, cookie, grant.CSRFToken)
	if response.Code != http.StatusCreated {
		t.Fatalf("import status = %d, body = %s", response.Code, response.Body.String())
	}
	response = performJSON(t, handler, http.MethodGet, "/api/accounts?q=Finance&page=1&page_size=25", nil, cookie, "")
	if response.Code != http.StatusOK {
		t.Fatalf("search status = %d, body = %s", response.Code, response.Body.String())
	}
	var page accounts.AccountList
	decodeResponse(t, response, &page)
	if page.Total != 1 || len(page.Accounts) != 1 || page.PageSize != 25 {
		t.Fatalf("account page = %+v", page)
	}
	response = performJSON(t, handler, http.MethodGet, "/api/accounts/selection?q=Finance", nil, cookie, "")
	var selection struct {
		PublicIDs []string `json:"public_ids"`
	}
	decodeResponse(t, response, &selection)
	if len(selection.PublicIDs) != 1 {
		t.Fatalf("selection = %+v", selection)
	}
	response = performJSON(t, handler, http.MethodPost, "/api/accounts/batch/status", map[string]any{
		"public_ids": selection.PublicIDs, "disabled": true,
	}, cookie, "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("batch status without CSRF = %d", response.Code)
	}
	response = performJSON(t, handler, http.MethodPost, "/api/accounts/batch/status", map[string]any{
		"public_ids": selection.PublicIDs, "disabled": true,
	}, cookie, grant.CSRFToken)
	if response.Code != http.StatusOK {
		t.Fatalf("batch status = %d, body = %s", response.Code, response.Body.String())
	}
	response = performJSON(t, handler, http.MethodPost, "/api/accounts/batch/delete", map[string]any{
		"public_ids": selection.PublicIDs, "confirm": "wrong",
	}, cookie, grant.CSRFToken)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("batch delete without confirmation = %d", response.Code)
	}
	response = performJSON(t, handler, http.MethodPost, "/api/accounts/batch/delete", map[string]any{
		"public_ids": selection.PublicIDs, "confirm": "DELETE_LOCAL_ACCOUNTS",
	}, cookie, grant.CSRFToken)
	if response.Code != http.StatusOK {
		t.Fatalf("batch delete = %d, body = %s", response.Code, response.Body.String())
	}
}
