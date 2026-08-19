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

func countAuditDetailsContaining(t *testing.T, db *sql.DB, value string) int {
	t.Helper()
	rows, err := db.Query(`SELECT details_json FROM audit_events`)
	if err != nil {
		t.Fatalf("query audit details: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var details string
		if err := rows.Scan(&details); err != nil {
			t.Fatalf("scan audit details: %v", err)
		}
		if strings.Contains(details, value) {
			count++
		}
	}
	return count
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
