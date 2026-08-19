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
	return s.createBackup(ctx, false)
}

// CreateUpdateBackup creates a verified backup while the application container
// is stopped. It deliberately opens SQLite without running application migrations.
func CreateUpdateBackup(ctx context.Context, dataDir string) (Backup, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(dataDir))
	if err != nil || strings.TrimSpace(dataDir) == "" {
		return Backup{}, errors.New("resolve update backup data directory")
	}
	databasePath := filepath.Join(absolute, "outlook-manager.db")
	if info, err := os.Stat(databasePath); err != nil || !info.Mode().IsRegular() {
		return Backup{}, errors.New("update backup source is not a database file")
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return Backup{}, fmt.Errorf("open update backup database: %w", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return Backup{}, fmt.Errorf("ping update backup database: %w", err)
	}
	var busy, logFrames, checkpointedFrames int
	if err := db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		return Backup{}, fmt.Errorf("checkpoint database before update backup: %w", err)
	}
	if busy != 0 || checkpointedFrames != logFrames {
		return Backup{}, fmt.Errorf("checkpoint database before update backup: busy=%d frames=%d checkpointed=%d", busy, logFrames, checkpointedFrames)
	}
	service, err := New(db, absolute, Options{})
	if err != nil {
		return Backup{}, err
	}
	return service.createBackup(ctx, true)
}

func (s *Service) createBackup(ctx context.Context, strict bool) (Backup, error) {
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
	if strict {
		if err := validateApplicationDatabase(ctx, s.db); err != nil {
			_ = os.Remove(path)
			return Backup{}, fmt.Errorf("validate update backup source: %w", err)
		}
		if err := verifySQLiteIntegrity(path); err != nil {
			_ = os.Remove(path)
			return Backup{}, err
		}
		if err := compareDatabaseCounts(ctx, s.db, path); err != nil {
			_ = os.Remove(path)
			return Backup{}, fmt.Errorf("validate update backup contents: %w", err)
		}
	}
	backup, err := inspectBackup(path)
	if err != nil {
		_ = os.Remove(path)
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
	if err := verifySQLiteIntegrity(sourcePath); err != nil {
		return "", err
	}
	if err := validateSQLiteApplicationDatabase(sourcePath); err != nil {
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
	if _, err := os.Stat(target); err == nil {
		if err := checkpointSQLite(target); err != nil {
			_ = os.Remove(temporary)
			return "", fmt.Errorf("checkpoint current database before restore: %w", err)
		}
	}
	safety := ""
	stamp := time.Now().UTC().Format("20060102T150405Z")
	archived := make([][2]string, 0, 3)
	if _, err := os.Stat(target); err == nil {
		safety = target + ".before-restore-" + stamp
		if err := os.Rename(target, safety); err != nil {
			_ = os.Remove(temporary)
			return "", fmt.Errorf("preserve current database: %w", err)
		}
		archived = append(archived, [2]string{safety, target})
		for _, suffix := range []string{"-wal", "-shm"} {
			currentSidecar, safetySidecar := target+suffix, safety+suffix
			if _, err := os.Stat(currentSidecar); err == nil {
				if err := os.Rename(currentSidecar, safetySidecar); err != nil {
					restoreArchivedFiles(archived)
					_ = os.Remove(temporary)
					return "", fmt.Errorf("preserve current database sidecar: %w", err)
				}
				archived = append(archived, [2]string{safetySidecar, currentSidecar})
			}
		}
	}
	if err := os.Rename(temporary, target); err != nil {
		restoreArchivedFiles(archived)
		return "", fmt.Errorf("activate restored database: %w", err)
	}
	if err := verifySQLiteIntegrity(target); err != nil {
		_ = os.Remove(target)
		restoreArchivedFiles(archived)
		return "", fmt.Errorf("verify activated restore database: %w", err)
	}
	return safety, nil
}

func restoreArchivedFiles(archived [][2]string) {
	for index := len(archived) - 1; index >= 0; index-- {
		_ = os.Rename(archived[index][0], archived[index][1])
	}
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
	return verifySQLitePragma(path, "quick_check")
}

func verifySQLiteIntegrity(path string) error {
	return verifySQLitePragma(path, "integrity_check")
}

func verifySQLitePragma(path, pragma string) error {
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open restore database: %w", err)
	}
	defer db.Close()
	var result string
	if err := db.QueryRow("PRAGMA " + pragma).Scan(&result); err != nil || result != "ok" {
		return errors.New("restore database failed integrity check")
	}
	return nil
}

func checkpointSQLite(path string) error {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	var busy, logFrames, checkpointedFrames int
	if err := db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		return err
	}
	if busy != 0 || checkpointedFrames != logFrames {
		return fmt.Errorf("database is busy: busy=%d frames=%d checkpointed=%d", busy, logFrames, checkpointedFrames)
	}
	return nil
}

func validateApplicationDatabase(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version >= 6 {
		var settings int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_settings WHERE id = 1`).Scan(&settings); err != nil {
			return fmt.Errorf("read application settings: %w", err)
		}
		if settings != 1 {
			return fmt.Errorf("expected one application settings row, found %d", settings)
		}
	}
	var admins int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&admins); err != nil {
		return fmt.Errorf("read administrators: %w", err)
	}
	if admins > 1 {
		return fmt.Errorf("expected at most one administrator, found %d", admins)
	}
	return nil
}

func validateSQLiteApplicationDatabase(path string) error {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return fmt.Errorf("open application database for validation: %w", err)
	}
	defer db.Close()
	if err := validateApplicationDatabase(context.Background(), db); err != nil {
		return fmt.Errorf("restore database failed application data validation: %w", err)
	}
	return nil
}

func compareDatabaseCounts(ctx context.Context, source *sql.DB, backupPath string) error {
	backup, err := sql.Open("sqlite", "file:"+filepath.ToSlash(backupPath)+"?mode=ro")
	if err != nil {
		return err
	}
	defer backup.Close()
	rows, err := source.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return err
	}
	tables := make([]string, 0)
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, table := range tables {
		quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		var sourceCount, backupCount int64
		if err := source.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoted).Scan(&sourceCount); err != nil {
			return fmt.Errorf("count source table %s: %w", table, err)
		}
		if err := backup.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoted).Scan(&backupCount); err != nil {
			return fmt.Errorf("count backup table %s: %w", table, err)
		}
		if sourceCount != backupCount {
			return fmt.Errorf("table %s row count changed from %d to %d", table, sourceCount, backupCount)
		}
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

