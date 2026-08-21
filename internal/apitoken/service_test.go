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

func TestAllAccountsGrantIncludesFutureAccounts(t *testing.T) {
	ctx := context.Background()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	service, _ := New(store.DB, Options{})
	created, err := service.Create(ctx, TokenInput{
		Name: "all", Scopes: []string{"accounts:read"}, AllAccounts: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := service.Verify(ctx, created.Secret, "127.0.0.1", "accounts:read")
	if err != nil {
		t.Fatal(err)
	}
	if !grant.AllAccounts {
		t.Fatal("grant does not preserve all_accounts")
	}
	allowed, err := grant.AllowsAccount(ctx, store.DB, "future_account")
	if err != nil || !allowed {
		t.Fatalf("future account allowed = %v, %v", allowed, err)
	}
}

func TestTokenDeleteRequiresInactiveTokenAndRemovesIt(t *testing.T) {
	ctx := context.Background()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	service, _ := New(store.DB, Options{Now: func() time.Time { return now }})
	created, err := service.Create(ctx, TokenInput{Name: "reader", Scopes: []string{"accounts:read"}, AllAccounts: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, created.PublicID); err != ErrTokenActive {
		t.Fatalf("Delete(active) error = %v", err)
	}
	if err := service.Revoke(ctx, created.PublicID); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, created.PublicID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Verify(ctx, created.Secret, "127.0.0.1", "accounts:read"); err != ErrUnauthorized {
		t.Fatalf("Verify() after delete error = %v", err)
	}

	expired, err := service.Create(ctx, TokenInput{Name: "expired", Scopes: []string{"accounts:read"}, AllAccounts: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`UPDATE api_tokens SET expires_at_utc = ? WHERE public_id = ?`, formatTime(now.Add(-time.Minute)), expired.PublicID); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, expired.PublicID); err != nil {
		t.Fatalf("Delete(expired) error = %v", err)
	}
	var auditCount int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event_type = 'api_token.deleted'`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 2 {
		t.Fatalf("deleted audit count = %d, want 2", auditCount)
	}
}
