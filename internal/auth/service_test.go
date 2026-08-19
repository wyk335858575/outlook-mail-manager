package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"outlook-mail-manager/internal/database"
	"outlook-mail-manager/internal/datakey"
)

func TestSetupLoginRestartAndLogout(t *testing.T) {
	ctx := context.Background()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	defer store.Close()

	now := time.Date(2026, time.August, 17, 8, 0, 0, 0, time.UTC)
	keyring := datakey.New(rand.Reader)
	service, err := New(store.DB, Options{
		Keyring: keyring,
		Now:     func() time.Time { return now },
		Random:  rand.Reader,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := service.ValidateStartup(ctx); err != nil {
		t.Fatalf("ValidateStartup() error = %v", err)
	}

	password := "correct horse 电池订书钉"
	start, err := service.StartSetup(ctx, "admin.root", password)
	if err != nil {
		t.Fatalf("StartSetup() error = %v", err)
	}
	passcode, err := totp.GenerateCode(start.Secret, now)
	if err != nil {
		t.Fatalf("totp.GenerateCode() error = %v", err)
	}
	setupGrant, err := service.CompleteSetup(ctx, start.ChallengeID, passcode)
	if err != nil {
		t.Fatalf("CompleteSetup() error = %v", err)
	}
	if keyring.Locked() {
		t.Fatal("data key remains locked after setup")
	}

	status, err := service.Status(ctx, setupGrant.Token)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Initialized || !status.Authenticated || status.Username != "admin.root" {
		t.Fatalf("Status() = %+v", status)
	}
	if _, err := service.Login(ctx, "admin.root", password, passcode); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login(replayed TOTP) error = %v, want invalid credentials", err)
	}

	now = now.Add(30 * time.Second)
	passcode, err = totp.GenerateCode(start.Secret, now)
	if err != nil {
		t.Fatalf("totp.GenerateCode() error = %v", err)
	}
	if _, err := service.Login(ctx, "wrong-admin", password, passcode); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login(wrong username) error = %v, want invalid credentials", err)
	}
	if _, err := service.Login(ctx, "admin.root", "wrong password", passcode); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login(wrong password) error = %v, want invalid credentials", err)
	}
	loginGrant, err := service.Login(ctx, "ADMIN.ROOT", password, passcode)
	if err != nil {
		t.Fatalf("Login(TOTP) error = %v", err)
	}
	if err := service.Logout(ctx, loginGrant.Token, "wrong-csrf"); !errors.Is(err, ErrInvalidCSRF) {
		t.Fatalf("Logout(wrong CSRF) error = %v, want invalid CSRF", err)
	}
	if err := service.Logout(ctx, loginGrant.Token, loginGrant.CSRFToken); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	status, err = service.Status(ctx, loginGrant.Token)
	if err != nil {
		t.Fatalf("Status(after logout) error = %v", err)
	}
	if status.Authenticated {
		t.Fatal("revoked session remains authenticated")
	}

	var passwordHash, encryptedSecret, wrappedDataKey string
	if err := store.DB.QueryRow(`
		SELECT password_hash, totp_secret_ciphertext, wrapped_data_key FROM admins WHERE id = 1
	`).Scan(&passwordHash, &encryptedSecret, &wrappedDataKey); err != nil {
		t.Fatalf("load protected administrator data: %v", err)
	}
	if strings.Contains(passwordHash, password) || strings.Contains(encryptedSecret, start.Secret) || wrappedDataKey == "" {
		t.Fatal("administrator credentials were not protected at rest")
	}
	var recoveryTables int
	if err := store.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'recovery_codes'").Scan(&recoveryTables); err != nil {
		t.Fatalf("check recovery_codes table: %v", err)
	}
	if recoveryTables != 0 {
		t.Fatal("recovery_codes table still exists")
	}

	restartedKeyring := datakey.New(rand.Reader)
	restartedService, err := New(store.DB, Options{
		Keyring: restartedKeyring,
		Now:     func() time.Time { return now },
		Random:  rand.Reader,
	})
	if err != nil {
		t.Fatalf("New(restarted) error = %v", err)
	}
	if err := restartedService.ValidateStartup(ctx); err != nil {
		t.Fatalf("ValidateStartup(restarted) error = %v", err)
	}
	if !restartedKeyring.Locked() {
		t.Fatal("restart unexpectedly unlocked the data key")
	}
	status, err = restartedService.Status(ctx, setupGrant.Token)
	if err != nil {
		t.Fatalf("Status(after restart) error = %v", err)
	}
	if !status.Initialized || status.Authenticated {
		t.Fatalf("Status(after restart) = %+v", status)
	}

	now = now.Add(30 * time.Second)
	passcode, err = totp.GenerateCode(start.Secret, now)
	if err != nil {
		t.Fatalf("totp.GenerateCode(restart) error = %v", err)
	}
	if _, err := restartedService.Login(ctx, "admin.root", password, passcode); err != nil {
		t.Fatalf("Login(after restart) error = %v", err)
	}
	if restartedKeyring.Locked() {
		t.Fatal("login did not unlock the data key after restart")
	}
}

func TestAuditEventsAreImmutable(t *testing.T) {
	ctx := context.Background()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	defer store.Close()

	if err := insertAudit(ctx, store.DB, "test_event", "system", nil, time.Now()); err != nil {
		t.Fatalf("insertAudit() error = %v", err)
	}
	if _, err := store.DB.ExecContext(ctx, "UPDATE audit_events SET event_type = 'changed'"); err == nil {
		t.Fatal("audit event update unexpectedly succeeded")
	}
	if _, err := store.DB.ExecContext(ctx, "DELETE FROM audit_events"); err == nil {
		t.Fatal("audit event delete unexpectedly succeeded")
	}
}
