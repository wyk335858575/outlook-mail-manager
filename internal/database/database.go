package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct {
	DB           *sql.DB
	DataDir      string
	DatabasePath string
}

func Open(ctx context.Context, dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	databasePath, err := filepath.Abs(filepath.Join(dataDir, "outlook-manager.db"))
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}

	databaseURIPath := filepath.ToSlash(databasePath)
	if filepath.VolumeName(databasePath) != "" && !strings.HasPrefix(databaseURIPath, "/") {
		databaseURIPath = "/" + databaseURIPath
	}
	dsnURL := &url.URL{Scheme: "file", Path: databaseURIPath}
	query := dsnURL.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "journal_mode(WAL)")
	dsnURL.RawQuery = query.Encode()

	db, err := sql.Open("sqlite", dsnURL.String())
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)

	store := &Store{DB: db, DataDir: dataDir, DatabasePath: databasePath}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := store.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}

	return store, nil
}

func (s *Store) Close() error {
	return s.DB.Close()
}

func (s *Store) CurrentVersion(ctx context.Context) (int, error) {
	var version int
	err := s.DB.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.DB.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at_utc TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	entries, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(entries)
	currentVersion, err := s.CurrentVersion(ctx)
	if err != nil {
		return err
	}
	latestVersion := currentVersion
	for _, name := range entries {
		version, versionErr := migrationVersion(name)
		if versionErr != nil {
			return versionErr
		}
		if version > latestVersion {
			latestVersion = version
		}
	}
	if currentVersion > 0 && latestVersion > currentVersion {
		if err := s.backupBeforeMigration(ctx, currentVersion, latestVersion); err != nil {
			return err
		}
	}

	for _, name := range entries {
		version, err := migrationVersion(name)
		if err != nil {
			return err
		}

		var applied bool
		if err := s.DB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = ?)", version).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %d: %w", version, err)
		}
		if applied {
			continue
		}

		script, err := migrationFiles.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %d: %w", version, err)
		}

		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, string(script)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, name, applied_at_utc) VALUES (?, ?, ?)",
			version, filepath.Base(name), time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}

	return nil
}

func (s *Store) backupBeforeMigration(ctx context.Context, currentVersion, nextVersion int) error {
	backupDir := filepath.Join(s.DataDir, "backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return fmt.Errorf("create migration backup directory: %w", err)
	}
	name := fmt.Sprintf("outlook-manager.before-v%d-to-v%d-%s.db", currentVersion, nextVersion,
		time.Now().UTC().Format("20060102T150405Z"))
	backupPath := filepath.Join(backupDir, name)
	if _, err := s.DB.ExecContext(ctx, "VACUUM INTO '"+escapeSQLiteLiteral(filepath.ToSlash(backupPath))+"'"); err != nil {
		return fmt.Errorf("create pre-migration backup: %w", err)
	}
	return nil
}

func escapeSQLiteLiteral(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func migrationVersion(name string) (int, error) {
	base := filepath.Base(name)
	prefix, _, ok := strings.Cut(base, "_")
	if !ok {
		return 0, fmt.Errorf("migration %q has no numeric prefix", base)
	}
	version, err := strconv.Atoi(prefix)
	if err != nil || version < 1 {
		return 0, fmt.Errorf("migration %q has an invalid version", base)
	}
	return version, nil
}
