package accounts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"outlook-mail-manager/internal/datakey"
)

const (
	defaultAuthorityBaseURL      = "https://login.microsoftonline.com/consumers/oauth2/v2.0"
	defaultGraphAuthorityBaseURL = "https://login.microsoftonline.com/common/oauth2/v2.0"
	defaultGraphBaseURL          = "https://graph.microsoft.com/v1.0"
	maxImportRows                = 1000
	maxNotesBytes                = 500
)

type AuthMethod string

const (
	AuthMethodWeb   AuthMethod = "web"
	AuthMethodOAuth AuthMethod = "oauth"
)

var (
	ErrAccountNotFound           = errors.New("account not found")
	ErrAccountDisabled           = errors.New("account is disabled")
	ErrAuthorizationNotFound     = errors.New("authorization job not found")
	ErrAuthorizationState        = errors.New("authorization job is not awaiting confirmation")
	ErrMicrosoftNotConfigured    = errors.New("MS_CLIENT_ID is not configured")
	ErrInvalidMicrosoftClientID  = errors.New("Microsoft client ID must be a GUID")
	ErrDuplicateMicrosoftAccount = errors.New("Microsoft account is already connected")
	ErrReauthorizationRequired   = errors.New("account requires reauthorization")
	ErrPOPIMAPCredential         = errors.New("credential only grants POP or IMAP access")
)

var requestedScopes = []string{"openid", "profile", "offline_access", "User.Read", "Mail.ReadWrite"}

var requiredAccessScopes = []string{"Mail.ReadWrite"}

type Options struct {
	Keyring               *datakey.Store
	ClientID              string
	AuthorityBaseURL      string
	GraphAuthorityBaseURL string
	GraphBaseURL          string
	HTTPClient            *http.Client
	Now                   func() time.Time
	Random                io.Reader
}

type Service struct {
	db                 *sql.DB
	keyring            *datakey.Store
	oauthEndpoint      oauth2.Endpoint
	graphOAuthEndpoint oauth2.Endpoint
	httpClient         *http.Client
	graphBaseURL       string
	now                func() time.Time
	random             io.Reader
	manager            *TokenManager
	oauthImportSlots   chan struct{}
	healthCheckQueue   chan accountHealthCheck
	healthChecksMu     sync.Mutex
	healthChecks       map[int64]struct{}
	healthChecksWG     sync.WaitGroup

	jobsMu      sync.Mutex
	jobs        map[string]*authorizationJob
	accountJobs map[int64]string
	closeCtx    context.Context
	close       context.CancelFunc
}

type Account struct {
	PublicID                string     `json:"public_id"`
	ImportedEmail           string     `json:"imported_email"`
	AuthMethod              AuthMethod `json:"auth_method"`
	PrimaryEmail            string     `json:"primary_email,omitempty"`
	DisplayName             string     `json:"display_name,omitempty"`
	Notes                   string     `json:"notes"`
	Status                  string     `json:"status"`
	ReauthReason            string     `json:"reauth_reason,omitempty"`
	LastOAuthError          string     `json:"last_oauth_error,omitempty"`
	ConsecutiveFailures     int        `json:"consecutive_failures"`
	NextRetryAt             *time.Time `json:"next_retry_at,omitempty"`
	LastRefreshSuccessAt    *time.Time `json:"last_refresh_success_at,omitempty"`
	LastGraphSuccessAt      *time.Time `json:"last_graph_success_at,omitempty"`
	LastSyncSuccessAt       *time.Time `json:"last_sync_success_at,omitempty"`
	LastSyncError           string     `json:"last_sync_error,omitempty"`
	SyncFailures            int        `json:"sync_failures"`
	SyncNextRetryAt         *time.Time `json:"sync_next_retry_at,omitempty"`
	SyncBacklog             int        `json:"sync_backlog"`
	Groups                  []string   `json:"groups"`
	Tags                    []string   `json:"tags"`
	CleanupProtected        bool       `json:"cleanup_protected"`
	AuthorizationInProgress bool       `json:"authorization_in_progress"`
}

type ImportResult struct {
	Created  int `json:"created"`
	Existing int `json:"existing"`
}

type ImportValidationError struct {
	Message string
}

func (e *ImportValidationError) Error() string {
	return e.Message
}

type importRow struct {
	email string
	group string
	tags  []string
	notes string
}

func New(db *sql.DB, options Options) (*Service, error) {
	if db == nil {
		return nil, errors.New("accounts database is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	authorityWasConfigured := options.AuthorityBaseURL != ""
	if options.AuthorityBaseURL == "" {
		options.AuthorityBaseURL = defaultAuthorityBaseURL
	}
	if options.GraphAuthorityBaseURL == "" {
		if authorityWasConfigured {
			options.GraphAuthorityBaseURL = options.AuthorityBaseURL
		} else {
			options.GraphAuthorityBaseURL = defaultGraphAuthorityBaseURL
		}
	}
	if options.GraphBaseURL == "" {
		options.GraphBaseURL = defaultGraphBaseURL
	}
	if options.Keyring == nil {
		return nil, errors.New("data key store is required")
	}
	authority := strings.TrimRight(options.AuthorityBaseURL, "/")
	graphAuthority := strings.TrimRight(options.GraphAuthorityBaseURL, "/")
	oauthEndpoint := oauth2.Endpoint{
		AuthURL:       authority + "/authorize",
		DeviceAuthURL: authority + "/devicecode",
		TokenURL:      authority + "/token",
		AuthStyle:     oauth2.AuthStyleInParams,
	}
	graphOAuthEndpoint := oauth2.Endpoint{
		TokenURL:  graphAuthority + "/token",
		AuthStyle: oauth2.AuthStyleInParams,
	}
	closeCtx, cancel := context.WithCancel(context.Background())
	service := &Service{
		db:                 db,
		keyring:            options.Keyring,
		oauthEndpoint:      oauthEndpoint,
		graphOAuthEndpoint: graphOAuthEndpoint,
		httpClient:         options.HTTPClient,
		graphBaseURL:       strings.TrimRight(options.GraphBaseURL, "/"),
		now:                options.Now,
		random:             options.Random,
		oauthImportSlots:   make(chan struct{}, oauthImportConcurrency),
		healthCheckQueue:   make(chan accountHealthCheck, maxImportRows),
		healthChecks:       make(map[int64]struct{}),
		jobs:               make(map[string]*authorizationJob),
		accountJobs:        make(map[int64]string),
		closeCtx:           closeCtx,
		close:              cancel,
	}
	if err := service.bootstrapMicrosoftConfig(context.Background(), options.ClientID); err != nil {
		cancel()
		return nil, err
	}
	service.manager = newTokenManager(db, options.Keyring, graphOAuthEndpoint, options.HTTPClient, options.Now)
	for range oauthImportConcurrency {
		service.healthChecksWG.Add(1)
		go service.healthCheckWorker()
	}
	options.Keyring.OnUnlock(service.ResumeOAuthImports)
	return service, nil
}

func (s *Service) Close() {
	s.close()
	s.jobsMu.Lock()
	for _, job := range s.jobs {
		job.cancel()
	}
	s.jobsMu.Unlock()
	s.healthChecksWG.Wait()
}

func (s *Service) ValidateStartup(ctx context.Context) error {
	return nil
}

func (s *Service) TokenManager() *TokenManager {
	return s.manager
}

func newOAuthConfig(clientID string, endpoint oauth2.Endpoint) *oauth2.Config {
	return &oauth2.Config{
		ClientID: clientID,
		Endpoint: endpoint,
		Scopes:   append([]string(nil), requestedScopes...),
	}
}

func (s *Service) Import(ctx context.Context, value string) (ImportResult, error) {
	rows, err := parseImport(value)
	if err != nil {
		return ImportResult{}, err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ImportResult{}, fmt.Errorf("begin account import: %w", err)
	}
	defer tx.Rollback()

	result := ImportResult{}
	for _, row := range rows {
		publicID, err := randomPublicID(s.random)
		if err != nil {
			return ImportResult{}, err
		}
		insert, err := tx.ExecContext(ctx, `
			INSERT INTO accounts (public_id, imported_email, auth_method, notes, created_at_utc, updated_at_utc)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(imported_email) DO NOTHING
		`, publicID, row.email, AuthMethodWeb, row.notes, formatTime(now), formatTime(now))
		if err != nil {
			return ImportResult{}, fmt.Errorf("import account: %w", err)
		}
		created, err := insert.RowsAffected()
		if err != nil {
			return ImportResult{}, fmt.Errorf("read account import result: %w", err)
		}
		if created == 1 {
			result.Created++
		} else {
			result.Existing++
			if row.notes != "" {
				if _, err := tx.ExecContext(ctx,
					"UPDATE accounts SET notes = ?, updated_at_utc = ? WHERE imported_email = ?",
					row.notes, formatTime(now), row.email,
				); err != nil {
					return ImportResult{}, fmt.Errorf("update imported account: %w", err)
				}
			}
		}

		var accountID int64
		if err := tx.QueryRowContext(ctx, "SELECT id FROM accounts WHERE imported_email = ?", row.email).Scan(&accountID); err != nil {
			return ImportResult{}, fmt.Errorf("load imported account: %w", err)
		}
		if row.group != "" {
			if err := attachName(ctx, tx, accountID, row.group, "account_groups", "account_group_members", "group_id", now); err != nil {
				return ImportResult{}, err
			}
		}
		for _, tag := range row.tags {
			if err := attachName(ctx, tx, accountID, tag, "account_tags", "account_tag_members", "tag_id", now); err != nil {
				return ImportResult{}, err
			}
		}
	}
	if err := insertAudit(ctx, tx, "accounts_imported", "admin", map[string]any{
		"created": result.Created, "existing": result.Existing,
	}, now); err != nil {
		return ImportResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ImportResult{}, fmt.Errorf("commit account import: %w", err)
	}
	return result, nil
}

func (s *Service) SetDisabled(ctx context.Context, publicID string, disabled bool) error {
	result, err := s.SetDisabledBatch(ctx, []string{publicID}, disabled)
	if err != nil {
		return err
	}
	if result.Failed > 0 {
		return ErrAccountNotFound
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, publicID string) error {
	result, err := s.DeleteBatch(ctx, []string{publicID})
	if err != nil {
		return err
	}
	if result.Failed > 0 {
		return ErrAccountNotFound
	}
	return nil
}

func (s *Service) accountIdentity(ctx context.Context, publicID string) (int64, string, string, error) {
	var accountID int64
	var importedEmail, status string
	err := s.db.QueryRowContext(ctx,
		"SELECT id, imported_email, status FROM accounts WHERE public_id = ?", publicID,
	).Scan(&accountID, &importedEmail, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", "", ErrAccountNotFound
	}
	if err != nil {
		return 0, "", "", fmt.Errorf("load account: %w", err)
	}
	return accountID, importedEmail, status, nil
}

func parseImport(value string) ([]importRow, error) {
	if strings.TrimSpace(value) == "" {
		return nil, invalidImport("导入内容为空")
	}
	reader := csv.NewReader(strings.NewReader(value))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	records, err := reader.ReadAll()
	if err != nil {
		return nil, invalidImport("CSV 格式无效")
	}
	if len(records) == 0 {
		return nil, invalidImport("导入内容为空")
	}
	columns := map[string]int{"email": 0, "group": 1, "tags": 2, "notes": 3}
	headerColumns := 0
	if isImportHeader(records[0]) {
		columns, err = parseImportHeader(records[0])
		if err != nil {
			return nil, err
		}
		headerColumns = len(records[0])
		records = records[1:]
	}
	if len(records) == 0 || len(records) > maxImportRows {
		return nil, invalidImport(fmt.Sprintf("每次需要导入 1 到 %d 个账号", maxImportRows))
	}
	rows := make([]importRow, 0, len(records))
	for index, record := range records {
		if len(record) < 1 || len(record) > 4 || (headerColumns > 0 && len(record) > headerColumns) {
			return nil, invalidImport(fmt.Sprintf("第 %d 行只能包含邮箱、分组、标签和备注", index+1))
		}
		valueAt := func(name string) string {
			position, ok := columns[name]
			if !ok || position >= len(record) {
				return ""
			}
			return record[position]
		}
		email, err := normalizeEmail(valueAt("email"))
		if err != nil {
			return nil, invalidImport(fmt.Sprintf("第 %d 行的邮箱地址无效", index+1))
		}
		group := strings.TrimSpace(valueAt("group"))
		if len(group) > 80 {
			return nil, invalidImport(fmt.Sprintf("第 %d 行的分组名称过长", index+1))
		}
		tags, err := parseTags(valueAt("tags"))
		if err != nil {
			return nil, invalidImport(fmt.Sprintf("第 %d 行的标签无效", index+1))
		}
		notes := strings.TrimSpace(valueAt("notes"))
		if len(notes) > maxNotesBytes {
			return nil, invalidImport(fmt.Sprintf("第 %d 行的备注过长", index+1))
		}
		rows = append(rows, importRow{email: email, group: group, tags: tags, notes: notes})
	}
	return rows, nil
}

func isImportHeader(record []string) bool {
	if len(record) == 0 {
		return false
	}
	value := strings.ToLower(strings.TrimSpace(record[0]))
	return value == "email" || value == "邮箱"
}

func parseImportHeader(record []string) (map[string]int, error) {
	allowed := map[string]string{
		"email": "email", "邮箱": "email", "group": "group", "分组": "group",
		"tags": "tags", "标签": "tags", "note": "notes", "notes": "notes", "备注": "notes",
	}
	if len(record) > 4 {
		return nil, invalidImport("导入表头包含不支持的字段")
	}
	columns := make(map[string]int, len(record))
	for index, field := range record {
		name := strings.ToLower(strings.TrimSpace(field))
		if strings.Contains(name, "password") || strings.Contains(name, "密码") {
			return nil, invalidImport("导入内容不能包含邮箱密码")
		}
		column, ok := allowed[name]
		if !ok {
			return nil, invalidImport(fmt.Sprintf("不支持导入字段 %q", field))
		}
		if _, exists := columns[column]; exists {
			return nil, invalidImport(fmt.Sprintf("导入表头重复了字段 %q", field))
		}
		columns[column] = index
	}
	if _, ok := columns["email"]; !ok {
		return nil, invalidImport("导入表头必须包含邮箱字段")
	}
	return columns, nil
}

func normalizeEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 254 || strings.ContainsAny(value, "<>\r\n") {
		return "", errors.New("email address is invalid")
	}
	address, err := mail.ParseAddress(value)
	if err != nil || !strings.EqualFold(address.Address, value) || strings.Count(address.Address, "@") != 1 {
		return "", errors.New("email address is invalid")
	}
	return strings.ToLower(address.Address), nil
}

func parseTags(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return []string{}, nil
	}
	seen := make(map[string]bool)
	tags := make([]string, 0)
	for _, value := range strings.Split(value, "|") {
		tag := strings.TrimSpace(value)
		if tag == "" || len(tag) > 64 {
			return nil, errors.New("tag name is invalid")
		}
		key := strings.ToLower(tag)
		if !seen[key] {
			seen[key] = true
			tags = append(tags, tag)
		}
	}
	if len(tags) > 20 {
		return nil, errors.New("an account may have at most 20 tags")
	}
	return tags, nil
}

func invalidImport(message string) error {
	return &ImportValidationError{Message: message}
}

func attachName(ctx context.Context, tx *sql.Tx, accountID int64, name, table, membership, foreignKey string, now time.Time) error {
	insertEntity := fmt.Sprintf("INSERT INTO %s (name, created_at_utc) VALUES (?, ?) ON CONFLICT(name) DO NOTHING", table)
	if _, err := tx.ExecContext(ctx, insertEntity, name, formatTime(now)); err != nil {
		return fmt.Errorf("create account label: %w", err)
	}
	var entityID int64
	selectEntity := fmt.Sprintf("SELECT id FROM %s WHERE name = ?", table)
	if err := tx.QueryRowContext(ctx, selectEntity, name).Scan(&entityID); err != nil {
		return fmt.Errorf("load account label: %w", err)
	}
	insertMembership := fmt.Sprintf("INSERT INTO %s (account_id, %s) VALUES (?, ?) ON CONFLICT DO NOTHING", membership, foreignKey)
	if _, err := tx.ExecContext(ctx, insertMembership, accountID, entityID); err != nil {
		return fmt.Errorf("attach account label: %w", err)
	}
	return nil
}

func validStatus(status string) bool {
	switch status {
	case "pending", "active", "degraded", "reauth_required", "disabled":
		return true
	default:
		return false
	}
}

func randomPublicID(random io.Reader) (string, error) {
	value := make([]byte, 16)
	if _, err := io.ReadFull(random, value); err != nil {
		return "", fmt.Errorf("generate account public id: %w", err)
	}
	return "acc_" + base64.RawURLEncoding.EncodeToString(value), nil
}

func splitNames(value string) []string {
	if value == "" {
		return []string{}
	}
	return strings.Split(value, string(rune(31)))
}

func parseOptionalTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	return &parsed
}

func tokenAssociatedData(accountID int64, kind string) string {
	return fmt.Sprintf("account:%d:oauth-%s-token", accountID, kind)
}

func insertAudit(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, eventType, actorType string, details map[string]any, now time.Time) error {
	if details == nil {
		details = map[string]any{}
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode audit details: %w", err)
	}
	if _, err := executor.ExecContext(ctx, `
		INSERT INTO audit_events (event_type, actor_type, details_json, created_at_utc)
		VALUES (?, ?, ?, ?)
	`, eventType, actorType, string(encoded), formatTime(now)); err != nil {
		return fmt.Errorf("write audit event: %w", err)
	}
	return nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
