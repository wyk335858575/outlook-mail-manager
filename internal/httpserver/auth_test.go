package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pquerna/otp/totp"

	"outlook-mail-manager/internal/auth"
	"outlook-mail-manager/internal/database"
	"outlook-mail-manager/internal/datakey"
)

func TestAuthenticationHTTPFlowAndCookieSecurity(t *testing.T) {
	store, err := database.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	defer store.Close()

	now := time.Date(2026, time.August, 17, 8, 0, 0, 0, time.UTC)
	keyring := datakey.New(nil)
	authService, err := auth.New(store.DB, auth.Options{
		Keyring:       keyring,
		SecureCookies: true,
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("auth.New() error = %v", err)
	}
	handler := New(
		store.DB,
		slog.New(slog.NewTextHandler(testWriter{t}, nil)),
		fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("app")}},
		authService,
		nil,
		nil,
		nil,
		nil,
		nil,
	)

	startResponse := performJSON(t, handler, http.MethodPost, "/api/auth/setup/start", map[string]string{
		"username":              "admin.root",
		"password":              "correct horse 电池订书钉",
		"password_confirmation": "correct horse 电池订书钉",
	}, nil, "")
	if startResponse.Code != http.StatusOK {
		t.Fatalf("setup start status = %d, body = %s", startResponse.Code, startResponse.Body.String())
	}
	var start struct {
		ChallengeID string `json:"challenge_id"`
		Secret      string `json:"secret"`
	}
	decodeResponse(t, startResponse, &start)
	passcode, err := totp.GenerateCode(start.Secret, now)
	if err != nil {
		t.Fatalf("totp.GenerateCode() error = %v", err)
	}

	completeResponse := performJSON(t, handler, http.MethodPost, "/api/auth/setup/complete", map[string]string{
		"challenge_id": start.ChallengeID,
		"passcode":     passcode,
	}, nil, "")
	if completeResponse.Code != http.StatusCreated {
		t.Fatalf("setup complete status = %d, body = %s", completeResponse.Code, completeResponse.Body.String())
	}
	cookies := completeResponse.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookies = %d, want 1", len(cookies))
	}
	sessionCookie := cookies[0]
	if sessionCookie.Name != "__Host-omm_session" || !sessionCookie.Secure || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteLaxMode || sessionCookie.Path != "/" {
		t.Fatalf("session cookie has unsafe attributes: %+v", sessionCookie)
	}
	statusResponse := performJSON(t, handler, http.MethodGet, "/api/auth/status", nil, sessionCookie, "")
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("status response = %d", statusResponse.Code)
	}
	var status struct {
		Authenticated bool   `json:"authenticated"`
		Username      string `json:"username"`
		CSRFToken     string `json:"csrf_token"`
	}
	decodeResponse(t, statusResponse, &status)
	if !status.Authenticated || status.Username != "admin.root" || status.CSRFToken == "" {
		t.Fatalf("authenticated status = %+v", status)
	}

	logoutResponse := performJSON(t, handler, http.MethodPost, "/api/auth/logout", nil, sessionCookie, "")
	if logoutResponse.Code != http.StatusForbidden {
		t.Fatalf("logout without CSRF status = %d, want 403", logoutResponse.Code)
	}
	logoutResponse = performJSON(t, handler, http.MethodPost, "/api/auth/logout", nil, sessionCookie, status.CSRFToken)
	if logoutResponse.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, body = %s", logoutResponse.Code, logoutResponse.Body.String())
	}

	statusResponse = performJSON(t, handler, http.MethodGet, "/api/auth/status", nil, sessionCookie, "")
	var loggedOutStatus struct {
		Authenticated bool `json:"authenticated"`
	}
	decodeResponse(t, statusResponse, &loggedOutStatus)
	if loggedOutStatus.Authenticated {
		t.Fatal("revoked cookie remains authenticated")
	}

	now = now.Add(30 * time.Second)
	passcode, err = totp.GenerateCode(start.Secret, now)
	if err != nil {
		t.Fatalf("totp.GenerateCode(login) error = %v", err)
	}
	loginResponse := performJSON(t, handler, http.MethodPost, "/api/auth/login", map[string]string{
		"username": "admin.root",
		"password": "correct horse 电池订书钉",
		"passcode": passcode,
	}, nil, "")
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", loginResponse.Code, loginResponse.Body.String())
	}
}

func TestFixedWindowLimiter(t *testing.T) {
	limiter := &fixedWindowLimiter{limit: 2, window: time.Minute}
	now := time.Date(2026, time.August, 17, 8, 0, 0, 0, time.UTC)
	if allowed, _ := limiter.allow(now); !allowed {
		t.Fatal("first request was rate limited")
	}
	if allowed, _ := limiter.allow(now); !allowed {
		t.Fatal("second request was rate limited")
	}
	if allowed, retry := limiter.allow(now); allowed || retry != 60 {
		t.Fatalf("third request = allowed %v, retry %d", allowed, retry)
	}
	if allowed, _ := limiter.allow(now.Add(time.Minute)); !allowed {
		t.Fatal("request after window reset was rate limited")
	}
}

func performJSON(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	body any,
	cookie *http.Cookie,
	csrfToken string,
) *httptest.ResponseRecorder {
	t.Helper()
	var encoded bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&encoded).Encode(body); err != nil {
			t.Fatalf("encode request: %v", err)
		}
	}
	request := httptest.NewRequest(method, path, &encoded)
	request.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if csrfToken != "" {
		request.Header.Set("X-CSRF-Token", csrfToken)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, destination any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}
