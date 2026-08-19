package database

import (
	"context"
	"database/sql"
	"io/fs"
	"net/url"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestOpenAppliesMigrationsIdempotently(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	store, err := Open(ctx, dataDir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	store, err = Open(ctx, dataDir)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer store.Close()

	version, err := store.CurrentVersion(ctx)
	if err != nil {
		t.Fatalf("CurrentVersion() error = %v", err)
	}
	if version != 14 {
		t.Fatalf("CurrentVersion() = %d, want 14", version)
	}

	var count int
	if err := store.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM app_metadata WHERE key = 'installation_state'").Scan(&count); err != nil {
		t.Fatalf("query app_metadata: %v", err)
	}
	if count != 1 {
		t.Fatalf("installation_state rows = %d, want 1", count)
	}
	var syncInterval, pageSize int
	if err := store.DB.QueryRowContext(ctx,
		"SELECT sync_interval_seconds, message_page_size FROM app_settings WHERE id = 1",
	).Scan(&syncInterval, &pageSize); err != nil {
		t.Fatalf("query app_settings: %v", err)
	}
	if syncInterval != 600 || pageSize != 100 {
		t.Fatalf("app settings defaults = interval:%d page:%d", syncInterval, pageSize)
	}
	var defaultFolder string
	var defaultUnreadOnly, autoSelectFirst, showBodyPreview bool
	if err := store.DB.QueryRowContext(ctx, `
		SELECT default_folder, default_unread_only, auto_select_first_message, show_body_preview
		FROM app_settings WHERE id = 1
	`).Scan(&defaultFolder, &defaultUnreadOnly, &autoSelectFirst, &showBodyPreview); err != nil {
		t.Fatalf("query inbox preferences: %v", err)
	}
	if defaultFolder != "all" || defaultUnreadOnly || !autoSelectFirst || !showBodyPreview {
		t.Fatalf("inbox preference defaults = folder:%q unread:%v auto:%v preview:%v", defaultFolder, defaultUnreadOnly, autoSelectFirst, showBodyPreview)
	}
	var clientID string
	if err := store.DB.QueryRowContext(ctx,
		"SELECT client_id FROM microsoft_oauth_config WHERE id = 1",
	).Scan(&clientID); err != nil {
		t.Fatalf("query Microsoft OAuth config: %v", err)
	}
	if clientID != "" {
		t.Fatalf("default Microsoft client ID = %q, want empty", clientID)
	}
	var oauthClientIDColumn int
	if err := store.DB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM pragma_table_info('account_tokens') WHERE name = 'oauth_client_id'
	`).Scan(&oauthClientIDColumn); err != nil {
		t.Fatalf("query account token columns: %v", err)
	}
	if oauthClientIDColumn != 1 {
		t.Fatalf("oauth_client_id columns = %d, want 1", oauthClientIDColumn)
	}
}

func TestOpenRejectsDatabaseMissingSingletonSettings(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, err := Open(ctx, dataDir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.DB.ExecContext(ctx, `DELETE FROM app_settings`); err != nil {
		t.Fatalf("delete settings: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := Open(ctx, dataDir); err == nil || !strings.Contains(err.Error(), "database invariant failed") {
		t.Fatalf("Open() error = %v, want database invariant failure", err)
	}
}

func TestMailSearchMigrationKeepsFTSInSync(t *testing.T) {
	store, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	now := "2026-08-17T10:00:00Z"
	account, err := store.DB.Exec(`
		INSERT INTO accounts (public_id, imported_email, created_at_utc, updated_at_utc)
		VALUES ('acc_test', 'user@outlook.com', ?, ?)
	`, now, now)
	if err != nil {
		t.Fatalf("insert account: %v", err)
	}
	accountID, _ := account.LastInsertId()
	folder, err := store.DB.Exec(`
		INSERT INTO folders (account_id, graph_id, well_known_name, display_name, created_at_utc, updated_at_utc)
		VALUES (?, 'inbox', 'inbox', '收件箱', ?, ?)
	`, accountID, now, now)
	if err != nil {
		t.Fatalf("insert folder: %v", err)
	}
	folderID, _ := folder.LastInsertId()
	if _, err := store.DB.Exec(`
		INSERT INTO messages (
			public_id, account_id, folder_id, immutable_id, subject, sender_address,
			received_at_utc, body_text, created_at_utc, updated_at_utc
		) VALUES ('msg_test', ?, ?, 'immutable-1', '验证码', 'sender@example.com', ?, '您的验证码是 123456', ?, ?)
	`, accountID, folderID, now, now, now); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	var count int
	if err := store.DB.QueryRow("SELECT COUNT(*) FROM message_fts WHERE message_fts MATCH ?", "\"验证码\"").Scan(&count); err != nil {
		t.Fatalf("search message: %v", err)
	}
	if count != 1 {
		t.Fatalf("search count = %d, want 1", count)
	}
	if _, err := store.DB.Exec("UPDATE messages SET body_text = '更新后的正文' WHERE public_id = 'msg_test'"); err != nil {
		t.Fatalf("update message: %v", err)
	}
	if err := store.DB.QueryRow("SELECT COUNT(*) FROM message_fts WHERE message_fts MATCH ?", "\"123456\"").Scan(&count); err != nil {
		t.Fatalf("search old body: %v", err)
	}
	if count != 0 {
		t.Fatalf("old body search count = %d, want 0", count)
	}
}

func TestOpenEnablesSQLiteSafetyPragmas(t *testing.T) {
	store, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	var journalMode string
	if err := store.DB.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}

	var foreignKeys int
	if err := store.DB.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}
}

func TestOpenMigratesPersonalRulesAndSyncSecondsFromVersion11(t *testing.T) {
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, "outlook-manager.db")
	databaseURIPath := filepath.ToSlash(databasePath)
	if filepath.VolumeName(databasePath) != "" && !strings.HasPrefix(databaseURIPath, "/") {
		databaseURIPath = "/" + databaseURIPath
	}
	dsn := (&url.URL{Scheme: "file", Path: databaseURIPath}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open version 11 database: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at_utc TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create migration table: %v", err)
	}
	entries, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	sort.Strings(entries)
	for _, name := range entries {
		version, err := migrationVersion(name)
		if err != nil {
			t.Fatalf("migration version: %v", err)
		}
		if version > 11 {
			break
		}
		script, err := migrationFiles.ReadFile(name)
		if err != nil {
			t.Fatalf("read migration %d: %v", version, err)
		}
		if _, err := db.Exec(string(script)); err != nil {
			t.Fatalf("apply migration %d: %v", version, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations (version, name, applied_at_utc) VALUES (?, ?, ?)`,
			version, filepath.Base(name), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("record migration %d: %v", version, err)
		}
	}
	now := "2026-08-18T00:00:00Z"
	if _, err := db.Exec(`INSERT INTO notification_channels (
		public_id, name, kind, config_ciphertext, created_at_utc, updated_at_utc
	) VALUES ('channel_migration', '旧通道', 'webhook', 'sealed', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert old notification channel: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO notification_rules (
		public_id, channel_id, name, enabled, personal_inbox, categories_json,
		subject_keywords_json, created_at_utc, updated_at_utc
	) VALUES ('rule_migration', (SELECT id FROM notification_channels WHERE public_id = 'channel_migration'),
		'旧个性化规则', 1, 1, '["important"]', '["payment"]', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert old personal notification rule: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version 11 database: %v", err)
	}

	store, err := Open(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("Open() migration error = %v", err)
	}
	defer store.Close()
	version, err := store.CurrentVersion(context.Background())
	if err != nil || version != 14 {
		t.Fatalf("schema version = %d, %v", version, err)
	}
	var copied, legacyFlag, seconds int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM personal_inbox_rules WHERE public_id = 'rule_migration'`).Scan(&copied); err != nil {
		t.Fatalf("query migrated personal rule: %v", err)
	}
	if err := store.DB.QueryRow(`SELECT personal_inbox FROM notification_rules WHERE public_id = 'rule_migration'`).Scan(&legacyFlag); err != nil {
		t.Fatalf("query legacy personal flag: %v", err)
	}
	if err := store.DB.QueryRow(`SELECT sync_interval_seconds FROM app_settings WHERE id = 1`).Scan(&seconds); err != nil {
		t.Fatalf("query migrated sync interval: %v", err)
	}
	if copied != 1 || legacyFlag != 0 || seconds != 600 {
		t.Fatalf("migration result = copied:%d legacy:%d seconds:%d", copied, legacyFlag, seconds)
	}
	backups, err := filepath.Glob(filepath.Join(dataDir, "backups", "outlook-manager.before-v11-to-v14-*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("migration backups = %v, %v", backups, err)
	}
}

func TestSQLitePathSupportsWindowsDriveLetters(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path regression test")
	}

	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "含 空格"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	var databasePath string
	if err := store.DB.QueryRow("PRAGMA database_list").Scan(new(int), new(string), &databasePath); err != nil {
		t.Fatalf("read database_list: %v", err)
	}
	if !strings.Contains(databasePath, "含 空格") {
		t.Fatalf("database path = %q, want Unicode directory", databasePath)
	}
}
