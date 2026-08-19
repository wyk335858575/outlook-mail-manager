package apitoken

import (
	"context"
	"testing"
	"time"

	"outlook-mail-manager/internal/database"
)

func TestTokenVerifyEnforcesScopeAccountAndIP(t *testing.T) {
	ctx := context.Background()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	_, err = store.DB.ExecContext(ctx, `
		INSERT INTO accounts (public_id, imported_email, status, created_at_utc, updated_at_utc)
		VALUES ('acc_1', 'one@example.com', 'active', ?, ?)
	`, formatTime(now), formatTime(now))
	if err != nil {
		t.Fatal(err)
	}
	service, _ := New(store.DB, Options{Now: func() time.Time { return now }})
	created, err := service.Create(ctx, TokenInput{
		Name: "reader", Scopes: []string{"mail:read"}, AccountPublicIDs: []string{"acc_1"},
		IPCIDRs: []string{"203.0.113.0/24"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	grant, err := service.Verify(ctx, created.Secret, "203.0.113.8", "mail:read")
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	allowed, err := grant.AllowsAccount(ctx, store.DB, "acc_1")
	if err != nil || !allowed {
		t.Fatalf("AllowsAccount() = %v, %v", allowed, err)
	}
	if _, err := service.Verify(ctx, created.Secret, "198.51.100.8", "mail:read"); err != ErrUnauthorized {
		t.Fatalf("wrong IP error = %v", err)
	}
	if _, err := service.Verify(ctx, created.Secret, "203.0.113.8", "otp:read"); err != ErrScopeDenied {
		t.Fatalf("wrong scope error = %v", err)
	}
}

func TestTokenRevocationIsImmediate(t *testing.T) {
	ctx := context.Background()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	_, _ = store.DB.ExecContext(ctx, `
		INSERT INTO accounts (public_id, imported_email, status, created_at_utc, updated_at_utc)
		VALUES ('acc_1', 'one@example.com', 'active', ?, ?)
	`, formatTime(now), formatTime(now))
	service, _ := New(store.DB, Options{Now: func() time.Time { return now }})
	created, err := service.Create(ctx, TokenInput{Name: "reader", Scopes: []string{"accounts:read"}, AccountPublicIDs: []string{"acc_1"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Revoke(ctx, created.PublicID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Verify(ctx, created.Secret, "127.0.0.1", "accounts:read"); err != ErrUnauthorized {
		t.Fatalf("Verify() after revoke error = %v", err)
	}
}
