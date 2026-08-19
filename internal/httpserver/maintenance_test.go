package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"outlook-mail-manager/internal/database"
	"outlook-mail-manager/internal/maintenance"
)

func TestBackupDeleteRequiresCSRF(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, err := database.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authService, _, grant := testAuthenticatedAuthService(t, store, func() time.Time {
		return time.Date(2026, 8, 19, 5, 0, 0, 0, time.UTC)
	})
	service, err := maintenance.New(store.DB, dataDir, maintenance.Options{})
	if err != nil {
		t.Fatal(err)
	}
	backup, err := service.CreateBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(store.DB, slog.New(slog.NewTextHandler(testWriter{t}, nil)), testAssets(), authService, nil, nil, nil, nil, service)
	cookie := &http.Cookie{Name: authService.CookieName(), Value: grant.Token}

	response := performJSON(t, handler, http.MethodDelete, "/api/backups/"+backup.Name, nil, cookie, "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("delete without CSRF status = %d", response.Code)
	}
	response = performJSON(t, handler, http.MethodDelete, "/api/backups/"+backup.Name, nil, cookie, grant.CSRFToken)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", response.Code, response.Body.String())
	}

	response = performJSON(t, handler, http.MethodPost, "/api/update/jobs", nil, cookie, "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("update without CSRF status = %d", response.Code)
	}
	response = performJSON(t, handler, http.MethodGet, "/api/update/status", nil, cookie, "")
	if response.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", response.Code, response.Body.String())
	}
}
