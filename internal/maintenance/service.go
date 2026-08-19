package maintenance

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Options struct {
	Now              func() time.Time
	Random           io.Reader
	Version          string
	UpdateRepository string
	UpdateImage      string
	UpdateSocket     string
	GitHubAPIBaseURL string
	HTTPClient       *http.Client
}

type Service struct {
	db               *sql.DB
	dataDir          string
	databasePath     string
	now              func() time.Time
	random           io.Reader
	version          string
	updateRepository string
	updateImage      string
	updateSocket     string
	githubAPIBaseURL string
	httpClient       *http.Client
}

type Backup struct {
	Name      string    `json:"name"`
	SizeBytes int64     `json:"size_bytes"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"created_at"`
}

var ErrBackupNotFound = errors.New("backup not found")
var ErrInvalidBackupName = errors.New("invalid backup name")

type Status struct {
	DatabaseOK          bool      `json:"database_ok"`
	DatabaseSizeBytes   int64     `json:"database_size_bytes"`
	SchemaVersion       int       `json:"schema_version"`
	BackupCount         int       `json:"backup_count"`
	LatestBackup        *Backup   `json:"latest_backup,omitempty"`
	FailedNotifications int       `json:"failed_notifications"`
	CleanupFailures     int       `json:"cleanup_failures"`
	CheckedAt           time.Time `json:"checked_at"`
}

func New(db *sql.DB, dataDir string, options Options) (*Service, error) {
	if db == nil || strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("maintenance database and data directory are required")
	}
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve maintenance data directory: %w", err)
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	return &Service{
		db: db, dataDir: absolute, databasePath: filepath.Join(absolute, "outlook-manager.db"),
		now: options.Now, random: options.Random, version: strings.TrimSpace(options.Version),
		updateRepository: strings.TrimSpace(options.UpdateRepository), updateImage: strings.TrimSpace(options.UpdateImage),
		updateSocket: strings.TrimSpace(options.UpdateSocket), githubAPIBaseURL: strings.TrimRight(options.GitHubAPIBaseURL, "/"),
		httpClient: options.HTTPClient,
	}, nil
}

func (s *Service) CreateBackup(ctx context.Context) (Backup, error) {
	backupDir := filepath.Join(s.dataDir, "backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return Backup{}, fmt.Errorf("create backup directory: %w", err)
	}
	name := "outlook-manager-" + s.now().UTC().Format("20060102T150405Z") + ".db"
	path := filepath.Join(backupDir, name)
	if _, err := os.Stat(path); err == nil {
		suffix, randomErr := randomID(s.random)
		if randomErr != nil {
			return Backup{}, randomErr
		}
		name = "outlook-manager-" + s.now().UTC().Format("20060102T150405Z") + "-" + suffix + ".db"
		path = filepath.Join(backupDir, name)
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO '"+escapeSQLiteLiteral(filepath.ToSlash(path))+"'"); err != nil {
		return Backup{}, fmt.Errorf("create SQLite backup: %w", err)
	}
	backup, err := inspectBackup(path)
	if err != nil {
		return Backup{}, err
	}
	_ = s.recordAudit(ctx, "backup.created", name, map[string]any{"sha256": backup.SHA256, "size_bytes": backup.SizeBytes})
	return backup, nil
}

func (s *Service) ListBackups(ctx context.Context) ([]Backup, error) {
	_ = ctx
	backupDir := filepath.Join(s.dataDir, "backups")
	entries, err := os.ReadDir(backupDir)
	if errors.Is(err, os.ErrNotExist) {
		return []Backup{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	items := make([]Backup, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".db") {
			continue
		}
		backup, err := inspectBackup(filepath.Join(backupDir, entry.Name()))
		if err != nil {
			continue
		}
		items = append(items, backup)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return items, nil
}

func (s *Service) DeleteBackup(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || filepath.Base(name) != name || !strings.HasPrefix(name, "outlook-manager-") || !strings.HasSuffix(strings.ToLower(name), ".db") {
		return ErrInvalidBackupName
	}
	path := filepath.Join(s.dataDir, "backups", name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrBackupNotFound
	}
	if err != nil {
		return fmt.Errorf("inspect backup for deletion: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidBackupName
	}
	if err := s.recordAudit(ctx, "backup.delete_requested", name, map[string]any{"size_bytes": info.Size()}); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		_ = s.recordAudit(ctx, "backup.delete_failed", name, map[string]any{"size_bytes": info.Size()})
		return fmt.Errorf("delete backup: %w", err)
	}
	_ = s.recordAudit(ctx, "backup.deleted", name, map[string]any{"size_bytes": info.Size()})
	return nil
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	status := Status{CheckedAt: s.now().UTC()}
	var quickCheck string
	if err := s.db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&quickCheck); err != nil {
		return Status{}, fmt.Errorf("check database integrity: %w", err)
	}
	status.DatabaseOK = quickCheck == "ok"
	if info, err := os.Stat(s.databasePath); err == nil {
		status.DatabaseSizeBytes = info.Size()
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&status.SchemaVersion); err != nil {
		return Status{}, fmt.Errorf("read schema version: %w", err)
	}
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_deliveries WHERE status = 'failed'`).Scan(&status.FailedNotifications)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cleanup_actions WHERE state = 'failed'`).Scan(&status.CleanupFailures)
	backups, err := s.ListBackups(ctx)
	if err != nil {
		return Status{}, err
	}
	status.BackupCount = len(backups)
	if len(backups) > 0 {
		status.LatestBackup = &backups[0]
	}
	return status, nil
}

func Restore(dataDir, sourcePath string) (string, error) {
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return "", fmt.Errorf("resolve restore data directory: %w", err)
	}
	sourcePath, err = filepath.Abs(sourcePath)
	if err != nil {
		return "", fmt.Errorf("resolve restore source: %w", err)
	}
	if info, err := os.Stat(sourcePath); err != nil || info.IsDir() {
		return "", errors.New("restore source is not a database file")
	}
	if err := verifySQLite(sourcePath); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", fmt.Errorf("create restore data directory: %w", err)
	}
	target := filepath.Join(dataDir, "outlook-manager.db")
	temporary := target + ".restore-new"
	if err := os.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("remove stale restore staging file: %w", err)
	}
	if err := copyFile(sourcePath, temporary); err != nil {
		return "", err
	}
	if err := verifySQLite(temporary); err != nil {
		_ = os.Remove(temporary)
		return "", err
	}
	safety := ""
	if _, err := os.Stat(target); err == nil {
		safety = target + ".before-restore-" + time.Now().UTC().Format("20060102T150405Z")
		if err := os.Rename(target, safety); err != nil {
			_ = os.Remove(temporary)
			return "", fmt.Errorf("preserve current database: %w", err)
		}
	}
	if err := os.Rename(temporary, target); err != nil {
		if safety != "" {
			_ = os.Rename(safety, target)
		}
		return "", fmt.Errorf("activate restored database: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		path := target + suffix
		if _, err := os.Stat(path); err == nil {
			archived := path + ".before-restore-" + time.Now().UTC().Format("20060102T150405Z")
			_ = os.Rename(path, archived)
		}
	}
	return safety, nil
}

func inspectBackup(path string) (Backup, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Backup{}, fmt.Errorf("inspect backup: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return Backup{}, fmt.Errorf("open backup: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return Backup{}, fmt.Errorf("hash backup: %w", err)
	}
	return Backup{
		Name: filepath.Base(path), SizeBytes: info.Size(), SHA256: hex.EncodeToString(hash.Sum(nil)),
		CreatedAt: info.ModTime().UTC(),
	}, nil
}

func verifySQLite(path string) error {
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open restore database: %w", err)
	}
	defer db.Close()
	var result string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&result); err != nil || result != "ok" {
		return errors.New("restore database failed integrity check")
	}
	return nil
}

func copyFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open restore source: %w", err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create restore staging file: %w", err)
	}
	_, copyErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return fmt.Errorf("copy restore database: %w", copyErr)
	}
	if syncErr != nil {
		return fmt.Errorf("sync restore database: %w", syncErr)
	}
	return closeErr
}

func (s *Service) recordAudit(ctx context.Context, eventType, entityPublicID string, details map[string]any) error {
	encoded, _ := json.Marshal(details)
	auditID, err := randomID(s.random)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO audit_events (public_id, event_type, actor_type, entity_type, entity_public_id, details_json, created_at_utc)
		VALUES (?, ?, 'admin', 'backup', ?, ?, ?)
	`, "audit_"+auditID, eventType, entityPublicID, string(encoded), s.now().UTC().Format(time.RFC3339Nano))
	return err
}

func randomID(source io.Reader) (string, error) {
	value := make([]byte, 12)
	if _, err := io.ReadFull(source, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func escapeSQLiteLiteral(value string) string { return strings.ReplaceAll(value, "'", "''") }
