package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"outlook-mail-manager/internal/database"
	"outlook-mail-manager/internal/datakey"
)

func TestSetupLoginRestartAndLogout(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, err := database.Open(ctx, dataDir)
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	defer store.Close()

	now := time.Date(2026, time.August, 17, 8, 0, 0, 0, time.UTC)
	keyring := datakey.New(rand.Reader)
	service, err := New(store.DB, Options{
		Keyring: keyring,
		DataDir: dataDir,
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
	if keyring.Locked() {
		t.Fatal("logout locked the data key and stopped background work")
	}

	var passwordHash, encryptedSecret, wrappedDataKey, autoUnlockCiphertext string
	if err := store.DB.QueryRow(`
		SELECT password_hash, totp_secret_ciphertext, wrapped_data_key, auto_unlock_ciphertext
		FROM admins WHERE id = 1
	`).Scan(&passwordHash, &encryptedSecret, &wrappedDataKey, &autoUnlockCiphertext); err != nil {
		t.Fatalf("load protected administrator data: %v", err)
	}
	if strings.Contains(passwordHash, password) || strings.Contains(encryptedSecret, start.Secret) || wrappedDataKey == "" || autoUnlockCiphertext == "" {
		t.Fatal("administrator credentials were not protected at rest")
	}
	autoUnlockFile := filepath.Join(dataDir, autoUnlockKeyFile)
	info, err := os.Stat(autoUnlockFile)
	if err != nil {
		t.Fatalf("stat automatic unlock key: %v", err)
	}
	if info.Size() != dataKeyBytes {
		t.Fatalf("automatic unlock key size = %d, want %d", info.Size(), dataKeyBytes)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("automatic unlock key mode = %o, want 600", info.Mode().Perm())
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
		DataDir: dataDir,
		Now:     func() time.Time { return now },
		Random:  rand.Reader,
	})
	if err != nil {
		t.Fatalf("New(restarted) error = %v", err)
	}
	if err := restartedService.ValidateStartup(ctx); err != nil {
		t.Fatalf("ValidateStartup(restarted) error = %v", err)
	}
	autoUnlocked, err := restartedService.AutoUnlock(ctx)
	if err != nil {
		t.Fatalf("AutoUnlock(restarted) error = %v", err)
	}
	if !autoUnlocked || restartedKeyring.Locked() {
		t.Fatal("restart did not unlock the data key automatically")
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

func TestLegacyLoginProvisionsAutomaticUnlock(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, err := database.Open(ctx, dataDir)
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	defer store.Close()

	now := time.Date(2026, time.August, 17, 9, 0, 0, 0, time.UTC)
	password := "legacy administrator password"
	manualKeyring := datakey.New(rand.Reader)
	manualService, err := New(store.DB, Options{
		Keyring: manualKeyring,
		Now:     func() time.Time { return now },
		Random:  rand.Reader,
	})
	if err != nil {
		t.Fatalf("New(manual) error = %v", err)
	}
	start, err := manualService.StartSetup(ctx, "legacy.admin", password)
	if err != nil {
		t.Fatalf("StartSetup() error = %v", err)
	}
	passcode, err := totp.GenerateCode(start.Secret, now)
	if err != nil {
		t.Fatalf("totp.GenerateCode(setup) error = %v", err)
	}
	if _, err := manualService.CompleteSetup(ctx, start.ChallengeID, passcode); err != nil {
		t.Fatalf("CompleteSetup() error = %v", err)
	}

	var ciphertext string
	if err := store.DB.QueryRow("SELECT auto_unlock_ciphertext FROM admins WHERE id = 1").Scan(&ciphertext); err != nil {
		t.Fatalf("load legacy automatic unlock metadata: %v", err)
	}
	if ciphertext != "" {
		t.Fatal("legacy setup unexpectedly created automatic unlock metadata")
	}

	restartedKeyring := datakey.New(rand.Reader)
	restartedService, err := New(store.DB, Options{
		Keyring: restartedKeyring,
		DataDir: dataDir,
		Now:     func() time.Time { return now },
		Random:  rand.Reader,
	})
	if err != nil {
		t.Fatalf("New(restarted) error = %v", err)
	}
	if err := restartedService.ValidateStartup(ctx); err != nil {
		t.Fatalf("ValidateStartup(restarted) error = %v", err)
	}
	if unlocked, err := restartedService.AutoUnlock(ctx); err != nil || unlocked {
		t.Fatalf("AutoUnlock(legacy) = %v, %v; want false, nil", unlocked, err)
	}
	if !restartedKeyring.Locked() {
		t.Fatal("legacy restart unexpectedly unlocked without a login")
	}

	now = now.Add(30 * time.Second)
	passcode, err = totp.GenerateCode(start.Secret, now)
	if err != nil {
		t.Fatalf("totp.GenerateCode(login) error = %v", err)
	}
	if _, err := restartedService.Login(ctx, "legacy.admin", password, passcode); err != nil {
		t.Fatalf("Login(legacy) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, autoUnlockKeyFile)); err != nil {
		t.Fatalf("legacy login did not create automatic unlock key: %v", err)
	}

	secondKeyring := datakey.New(rand.Reader)
	secondService, err := New(store.DB, Options{
		Keyring: secondKeyring,
		DataDir: dataDir,
		Now:     func() time.Time { return now },
		Random:  rand.Reader,
	})
	if err != nil {
		t.Fatalf("New(second restart) error = %v", err)
	}
	if err := secondService.ValidateStartup(ctx); err != nil {
		t.Fatalf("ValidateStartup(second restart) error = %v", err)
	}
	if unlocked, err := secondService.AutoUnlock(ctx); err != nil || !unlocked || secondKeyring.Locked() {
		t.Fatalf("AutoUnlock(second restart) = %v, %v, locked=%v", unlocked, err, secondKeyring.Locked())
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
