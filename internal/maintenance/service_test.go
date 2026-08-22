package maintenance

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"outlook-mail-manager/internal/database"
)

func TestDeleteBackupRemovesRegularBackupAndAudits(t *testing.T) {
	ctx := context.Background()
	store, err := database.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer store.Close()
	service, err := New(store.DB, filepath.Dir(store.DatabasePath), Options{Now: func() time.Time {
		return time.Date(2026, 8, 19, 3, 4, 5, 0, time.UTC)
	}, Random: rand.Reader})
	if err != nil {
		t.Fatalf("new maintenance service: %v", err)
	}
	backup, err := service.CreateBackup(ctx)
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	if err := service.DeleteBackup(ctx, backup.Name); err != nil {
		t.Fatalf("DeleteBackup() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(store.DatabasePath), "backups", backup.Name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup still exists or stat failed: %v", err)
	}
	var count int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event_type = 'backup.deleted' AND entity_public_id = ?`, backup.Name).Scan(&count); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if count != 1 {
		t.Fatalf("deleted audit count = %d, want 1", count)
	}
}

func TestDeleteBackupRejectsUnsafeTargets(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, err := database.Open(ctx, dataDir)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer store.Close()
	service, err := New(store.DB, dataDir, Options{})
	if err != nil {
		t.Fatalf("new maintenance service: %v", err)
	}
	for _, name := range []string{"../outlook-manager-evil.db", "outlook-manager.db", "other.db", "outlook-manager-evil.txt"} {
		if err := service.DeleteBackup(ctx, name); !errors.Is(err, ErrInvalidBackupName) {
			t.Fatalf("DeleteBackup(%q) error = %v, want ErrInvalidBackupName", name, err)
		}
	}
	if err := service.DeleteBackup(ctx, "outlook-manager-20260819T000000Z.db"); !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("missing backup error = %v, want ErrBackupNotFound", err)
	}
}

func TestDeleteBackupRejectsSymlink(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlink creation is not reliably available on Windows test hosts")
	}
	ctx := context.Background()
	dataDir := t.TempDir()
	store, err := database.Open(ctx, dataDir)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer store.Close()
	service, err := New(store.DB, dataDir, Options{})
	if err != nil {
		t.Fatalf("new maintenance service: %v", err)
	}
	backupDir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatalf("create backup directory: %v", err)
	}
	target := filepath.Join(dataDir, "outside.db")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	name := "outlook-manager-20260819T000000Z.db"
	if err := os.Symlink(target, filepath.Join(backupDir, name)); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if err := service.DeleteBackup(ctx, name); !errors.Is(err, ErrInvalidBackupName) {
		t.Fatalf("symlink error = %v, want ErrInvalidBackupName", err)
	}
}

func TestRestoreReplacesStaleTemporaryFile(t *testing.T) {
	sourceDir := filepath.Join(t.TempDir(), "source")
	source, err := database.Open(context.Background(), sourceDir)
	if err != nil {
		t.Fatalf("open source database: %v", err)
	}
	sourcePath := source.DatabasePath
	if err := source.Close(); err != nil {
		t.Fatalf("close source database: %v", err)
	}

	targetDir := filepath.Join(t.TempDir(), "target")
	target, err := database.Open(context.Background(), targetDir)
	if err != nil {
		t.Fatalf("open target database: %v", err)
	}
	if err := target.Close(); err != nil {
		t.Fatalf("close target database: %v", err)
	}
	stale := filepath.Join(targetDir, "outlook-manager.db.restore-new")
	if err := os.WriteFile(stale, []byte("incomplete restore"), 0o600); err != nil {
		t.Fatalf("write stale restore file: %v", err)
	}

	if _, err := Restore(targetDir, sourcePath); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if err := verifySQLite(filepath.Join(targetDir, "outlook-manager.db")); err != nil {
		t.Fatalf("restored database: %v", err)
	}
}

func TestCreateUpdateBackupPreservesAllApplicationRows(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, err := database.Open(ctx, dataDir)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if _, err := store.DB.ExecContext(ctx, `
		INSERT INTO accounts (public_id, imported_email, created_at_utc, updated_at_utc)
		VALUES ('acc_backup', 'backup@example.com', '2026-08-19T00:00:00Z', '2026-08-19T00:00:00Z')
	`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	backup, err := CreateUpdateBackup(ctx, dataDir)
	if err != nil {
		t.Fatalf("CreateUpdateBackup() error = %v", err)
	}
	backupDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dataDir, "backups", backup.Name))+"?mode=ro")
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backupDB.Close()
	var accounts, settings int
	if err := backupDB.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&accounts); err != nil {
		t.Fatalf("count backup accounts: %v", err)
	}
	if err := backupDB.QueryRow(`SELECT COUNT(*) FROM app_settings WHERE id = 1`).Scan(&settings); err != nil {
		t.Fatalf("count backup settings: %v", err)
	}
	if accounts != 1 || settings != 1 {
		t.Fatalf("backup rows = accounts:%d settings:%d", accounts, settings)
	}
}

func TestCreateUpdateBackupRejectsMissingSettings(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, err := database.Open(ctx, dataDir)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if _, err := store.DB.ExecContext(ctx, `DELETE FROM app_settings`); err != nil {
		t.Fatalf("delete settings: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	if _, err := CreateUpdateBackup(ctx, dataDir); err == nil || !strings.Contains(err.Error(), "expected one application settings row") {
		t.Fatalf("CreateUpdateBackup() error = %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "backups"))
	if err != nil {
		t.Fatalf("read backup directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid update backup was retained: %v", entries)
	}
}

func TestRestorePreservesPreviousDatabaseAsReadableSafetyCopy(t *testing.T) {
	ctx := context.Background()
	targetDir := t.TempDir()
	target, err := database.Open(ctx, targetDir)
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	if _, err := target.DB.ExecContext(ctx, `UPDATE app_metadata SET value = 'previous' WHERE key = 'installation_state'`); err != nil {
		t.Fatalf("update target: %v", err)
	}
	if err := target.Close(); err != nil {
		t.Fatalf("close target: %v", err)
	}
	sourceDir := t.TempDir()
	source, err := database.Open(ctx, sourceDir)
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close source: %v", err)
	}
	safety, err := Restore(targetDir, source.DatabasePath)
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if safety == "" {
		t.Fatal("Restore() did not preserve the previous database")
	}
	safetyDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(safety)+"?mode=ro")
	if err != nil {
		t.Fatalf("open safety database: %v", err)
	}
	defer safetyDB.Close()
	var value string
	if err := safetyDB.QueryRow(`SELECT value FROM app_metadata WHERE key = 'installation_state'`).Scan(&value); err != nil {
		t.Fatalf("read safety database: %v", err)
	}
	if value != "previous" {
		t.Fatalf("safety database value = %q", value)
	}
}

func TestRestoreRejectsStructurallyValidDatabaseMissingSettings(t *testing.T) {
	ctx := context.Background()
	sourceDir := t.TempDir()
	source, err := database.Open(ctx, sourceDir)
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	if _, err := source.DB.ExecContext(ctx, `DELETE FROM app_settings`); err != nil {
		t.Fatalf("delete source settings: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close source: %v", err)
	}
	targetDir := t.TempDir()
	target, err := database.Open(ctx, targetDir)
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	if err := target.Close(); err != nil {
		t.Fatalf("close target: %v", err)
	}
	if _, err := Restore(targetDir, source.DatabasePath); err == nil || !strings.Contains(err.Error(), "application data validation") {
		t.Fatalf("Restore() error = %v", err)
	}
	valid, err := database.Open(ctx, targetDir)
	if err != nil {
		t.Fatalf("target was changed by rejected restore: %v", err)
	}
	valid.Close()
}
