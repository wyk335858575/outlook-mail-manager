package httpserver

import (
	"bytes"
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pquerna/otp/totp"

	"outlook-mail-manager/internal/auth"
	"outlook-mail-manager/internal/database"
	"outlook-mail-manager/internal/datakey"
)

func TestHealthzReportsDatabaseReadiness(t *testing.T) {
	store, err := database.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	defer store.Close()

	handler := testHandler(t, store)
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if response.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("body = %q", response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", response.Header().Get("Cache-Control"))
	}
}

func TestHealthzFailsAfterDatabaseCloses(t *testing.T) {
	store, err := database.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	store.Close()

	handler := testHandler(t, store)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

func TestSPAUsesIndexFallbackAndSecurityHeaders(t *testing.T) {
	store, err := database.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	defer store.Close()

	handler := testHandler(t, store)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/settings", nil))

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "app-root") {
		t.Fatalf("SPA response = %d %q", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("Content-Security-Policy is missing")
	}
	if response.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q", response.Header().Get("Referrer-Policy"))
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", response.Header().Get("Cache-Control"))
	}
}

func TestSPAIndexDisablesCaching(t *testing.T) {
	store, err := database.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	defer store.Close()

	handler := testHandler(t, store)
	for target, expectedStatus := range map[string]int{"/": http.StatusOK, "/index.html": http.StatusMovedPermanently} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != expectedStatus {
			t.Fatalf("%s status = %d, want %d", target, response.Code, expectedStatus)
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", target, response.Header().Get("Cache-Control"))
		}
	}
}

func TestSPADoesNotMaskMissingAssets(t *testing.T) {
	store, err := database.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	defer store.Close()

	handler := testHandler(t, store)
	request := httptest.NewRequest(http.MethodGet, "/missing.js", nil)
	request.Header.Set("Accept", "*/*")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestRequestLoggerDoesNotLogQueryCredentials(t *testing.T) {
	var logs bytes.Buffer
	handler := requestLogger(
		slog.New(slog.NewTextHandler(&logs, nil)),
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/accounts?access_token=omm_log_secret", nil)
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if strings.Contains(logs.String(), "omm_log_secret") || strings.Contains(logs.String(), "access_token") {
		t.Fatalf("request log exposed query credentials: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "path=/api/v1/accounts") {
		t.Fatalf("request path missing from log: %s", logs.String())
	}
}

func testAssets() fs.FS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(`<div id="app-root"></div>`)},
	}
}

func testHandler(t *testing.T, store *database.Store) http.Handler {
	t.Helper()
	authService, err := auth.New(store.DB, auth.Options{
		Keyring: datakey.New(nil),
	})
	if err != nil {
		t.Fatalf("auth.New() error = %v", err)
	}
	return New(store.DB, slog.New(slog.NewTextHandler(testWriter{t}, nil)), testAssets(), authService, nil, nil, nil, nil, nil)
}

func testAuthenticatedAuthService(
	t *testing.T,
	store *database.Store,
	now func() time.Time,
) (*auth.Service, *datakey.Store, auth.SessionGrant) {
	t.Helper()
	keyring := datakey.New(nil)
	service, err := auth.New(store.DB, auth.Options{Keyring: keyring, Now: now})
	if err != nil {
		t.Fatalf("auth.New() error = %v", err)
	}
	start, err := service.StartSetup(context.Background(), "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("StartSetup() error = %v", err)
	}
	passcode, err := totp.GenerateCode(start.Secret, now())
	if err != nil {
		t.Fatalf("GenerateCode() error = %v", err)
	}
	grant, err := service.CompleteSetup(context.Background(), start.ChallengeID, passcode)
	if err != nil {
		t.Fatalf("CompleteSetup() error = %v", err)
	}
	return service, keyring, grant
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}

