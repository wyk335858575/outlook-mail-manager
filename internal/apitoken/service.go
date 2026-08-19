package apitoken

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

const tokenPrefixLength = 12

var (
	ErrInvalidTokenInput = errors.New("invalid API token input")
	ErrTokenNotFound     = errors.New("API token not found")
	ErrUnauthorized      = errors.New("API token is unauthorized")
	ErrScopeDenied       = errors.New("API token scope denied")
	ErrAccountDenied     = errors.New("API token account scope denied")
)

type Options struct {
	Now    func() time.Time
	Random io.Reader
}

type Service struct {
	db     *sql.DB
	now    func() time.Time
	random io.Reader
}

type TokenInput struct {
	Name             string     `json:"name"`
	Scopes           []string   `json:"scopes"`
	AccountPublicIDs []string   `json:"account_public_ids"`
	GroupNames       []string   `json:"group_names"`
	IPCIDRs          []string   `json:"ip_cidrs"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}

type Token struct {
	PublicID         string     `json:"public_id"`
	Name             string     `json:"name"`
	Prefix           string     `json:"prefix"`
	Scopes           []string   `json:"scopes"`
	AccountPublicIDs []string   `json:"account_public_ids"`
	GroupNames       []string   `json:"group_names"`
	IPCIDRs          []string   `json:"ip_cidrs"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	LastUsedAt       *time.Time `json:"last_used_at,omitempty"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

type CreatedToken struct {
	Token
	Secret string `json:"secret"`
}

type Grant struct {
	TokenPublicID    string
	Scopes           []string
	AccountPublicIDs []string
	GroupNames       []string
}

func New(db *sql.DB, options Options) (*Service, error) {
	if db == nil {
		return nil, errors.New("API token database is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	return &Service{db: db, now: options.Now, random: options.Random}, nil
}

func (s *Service) Create(ctx context.Context, input TokenInput) (CreatedToken, error) {
	if err := s.validateInput(ctx, input); err != nil {
		return CreatedToken{}, err
	}
	secretBytes := make([]byte, 32)
	if _, err := io.ReadFull(s.random, secretBytes); err != nil {
		return CreatedToken{}, fmt.Errorf("generate API token: %w", err)
	}
	secret := "omm_" + base64.RawURLEncoding.EncodeToString(secretBytes)
	prefix := secret
	if len(prefix) > tokenPrefixLength {
		prefix = prefix[:tokenPrefixLength]
	}
	hash := sha256.Sum256([]byte(secret))
	publicID, err := randomID("api_", s.random)
	if err != nil {
		return CreatedToken{}, err
	}
	scopesJSON, _ := json.Marshal(uniqueStrings(input.Scopes))
	accountsJSON, _ := json.Marshal(uniqueStrings(input.AccountPublicIDs))
	groupsJSON, _ := json.Marshal(uniqueStrings(input.GroupNames))
	ipJSON, _ := json.Marshal(uniqueStrings(input.IPCIDRs))
	var expires any
	if input.ExpiresAt != nil {
		expires = formatTime(input.ExpiresAt.UTC())
	}
	now := s.now().UTC()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO api_tokens (
			public_id, name, token_prefix, token_hash, scopes_json, account_public_ids_json,
			group_names_json, ip_cidrs_json, expires_at_utc, created_at_utc
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, publicID, strings.TrimSpace(input.Name), prefix, hex.EncodeToString(hash[:]), string(scopesJSON),
		string(accountsJSON), string(groupsJSON), string(ipJSON), expires, formatTime(now)); err != nil {
		return CreatedToken{}, fmt.Errorf("create API token: %w", err)
	}
	item, err := s.get(ctx, publicID)
	if err != nil {
		return CreatedToken{}, err
	}
	_ = s.recordAudit(ctx, "api_token.created", publicID, map[string]any{"scopes": item.Scopes})
	return CreatedToken{Token: item, Secret: secret}, nil
}

func (s *Service) List(ctx context.Context) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT public_id, name, token_prefix, scopes_json, account_public_ids_json, group_names_json,
			ip_cidrs_json, expires_at_utc, last_used_at_utc, revoked_at_utc, created_at_utc
		FROM api_tokens ORDER BY created_at_utc DESC, id DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list API tokens: %w", err)
	}
	defer rows.Close()
	items := make([]Token, 0)
	for rows.Next() {
		item, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) Revoke(ctx context.Context, publicID string) error {
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE api_tokens SET revoked_at_utc = ? WHERE public_id = ? AND revoked_at_utc IS NULL
	`, formatTime(now), strings.TrimSpace(publicID))
	if err != nil {
		return fmt.Errorf("revoke API token: %w", err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrTokenNotFound
	}
	_ = s.recordAudit(ctx, "api_token.revoked", publicID, nil)
	return nil
}

func (s *Service) Verify(ctx context.Context, secret, remoteIP, requiredScope string) (Grant, error) {
	secret = strings.TrimSpace(secret)
	if len(secret) < tokenPrefixLength || !strings.HasPrefix(secret, "omm_") {
		return Grant{}, ErrUnauthorized
	}
	prefix := secret[:tokenPrefixLength]
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, public_id, token_hash, scopes_json, account_public_ids_json,
			group_names_json, ip_cidrs_json, expires_at_utc, revoked_at_utc
		FROM api_tokens WHERE token_prefix = ?
	`, prefix)
	if err != nil {
		return Grant{}, fmt.Errorf("verify API token: %w", err)
	}
	defer rows.Close()
	presented := sha256.Sum256([]byte(secret))
	now := s.now().UTC()
	for rows.Next() {
		var id int64
		var publicID, storedHash, scopesJSON, accountsJSON, groupsJSON, cidrsJSON string
		var expires, revoked sql.NullString
		if err := rows.Scan(&id, &publicID, &storedHash, &scopesJSON, &accountsJSON, &groupsJSON,
			&cidrsJSON, &expires, &revoked); err != nil {
			return Grant{}, fmt.Errorf("scan API token: %w", err)
		}
		decodedHash, err := hex.DecodeString(storedHash)
		if err != nil || subtle.ConstantTimeCompare(decodedHash, presented[:]) != 1 {
			continue
		}
		if revoked.Valid {
			return Grant{}, ErrUnauthorized
		}
		if expires.Valid {
			expiry, err := parseTime(expires.String)
			if err != nil || !expiry.After(now) {
				return Grant{}, ErrUnauthorized
			}
		}
		var grant Grant
		grant.TokenPublicID = publicID
		var cidrs []string
		if err := decodeLists([]decodeTarget{
			{scopesJSON, &grant.Scopes}, {accountsJSON, &grant.AccountPublicIDs},
			{groupsJSON, &grant.GroupNames}, {cidrsJSON, &cidrs},
		}); err != nil {
			return Grant{}, err
		}
		if !contains(grant.Scopes, requiredScope) {
			return Grant{}, ErrScopeDenied
		}
		if !ipAllowed(remoteIP, cidrs) {
			return Grant{}, ErrUnauthorized
		}
		_, _ = s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at_utc = ? WHERE id = ?`, formatTime(now), id)
		return grant, nil
	}
	return Grant{}, ErrUnauthorized
}

func (g Grant) AllowsAccount(ctx context.Context, db *sql.DB, accountPublicID string) (bool, error) {
	if containsFold(g.AccountPublicIDs, accountPublicID) {
		return true, nil
	}
	if len(g.GroupNames) == 0 {
		return false, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(g.GroupNames)), ",")
	args := make([]any, 0, len(g.GroupNames)+1)
	args = append(args, accountPublicID)
	for _, group := range g.GroupNames {
		args = append(args, group)
	}
	var allowed bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM accounts a
			JOIN account_group_members gm ON gm.account_id = a.id
			JOIN account_groups g ON g.id = gm.group_id
			WHERE a.public_id = ? AND g.name IN (`+placeholders+`)
		)
	`, args...).Scan(&allowed)
	if err != nil {
		return false, fmt.Errorf("check API token account scope: %w", err)
	}
	return allowed, nil
}

func (s *Service) validateInput(ctx context.Context, input TokenInput) error {
	if strings.TrimSpace(input.Name) == "" || len(input.Scopes) == 0 || len(input.AccountPublicIDs)+len(input.GroupNames) == 0 {
		return ErrInvalidTokenInput
	}
	allowed := map[string]bool{"accounts:read": true, "mail:read": true, "otp:read": true, "system:read": true}
	for _, scope := range input.Scopes {
		if !allowed[scope] {
			return ErrInvalidTokenInput
		}
	}
	for _, cidr := range input.IPCIDRs {
		if _, _, err := net.ParseCIDR(strings.TrimSpace(cidr)); err != nil {
			return ErrInvalidTokenInput
		}
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(s.now().UTC()) {
		return ErrInvalidTokenInput
	}
	for _, account := range uniqueStrings(input.AccountPublicIDs) {
		var exists bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE public_id = ?)`, account).Scan(&exists); err != nil || !exists {
			return ErrInvalidTokenInput
		}
	}
	for _, group := range uniqueStrings(input.GroupNames) {
		var exists bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM account_groups WHERE name = ? COLLATE NOCASE)`, group).Scan(&exists); err != nil || !exists {
			return ErrInvalidTokenInput
		}
	}
	return nil
}

func (s *Service) get(ctx context.Context, publicID string) (Token, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT public_id, name, token_prefix, scopes_json, account_public_ids_json, group_names_json,
			ip_cidrs_json, expires_at_utc, last_used_at_utc, revoked_at_utc, created_at_utc
		FROM api_tokens WHERE public_id = ?
	`, strings.TrimSpace(publicID))
	item, err := scanToken(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrTokenNotFound
	}
	return item, err
}

type scanner interface{ Scan(...any) error }

func scanToken(row scanner) (Token, error) {
	var item Token
	var scopesJSON, accountsJSON, groupsJSON, cidrsJSON, created string
	var expires, lastUsed, revoked sql.NullString
	if err := row.Scan(&item.PublicID, &item.Name, &item.Prefix, &scopesJSON, &accountsJSON,
		&groupsJSON, &cidrsJSON, &expires, &lastUsed, &revoked, &created); err != nil {
		return Token{}, err
	}
	if err := decodeLists([]decodeTarget{
		{scopesJSON, &item.Scopes}, {accountsJSON, &item.AccountPublicIDs},
		{groupsJSON, &item.GroupNames}, {cidrsJSON, &item.IPCIDRs},
	}); err != nil {
		return Token{}, err
	}
	var err error
	item.CreatedAt, err = parseTime(created)
	if err != nil {
		return Token{}, err
	}
	if item.ExpiresAt, err = parseNullableTime(expires); err != nil {
		return Token{}, err
	}
	if item.LastUsedAt, err = parseNullableTime(lastUsed); err != nil {
		return Token{}, err
	}
	if item.RevokedAt, err = parseNullableTime(revoked); err != nil {
		return Token{}, err
	}
	return item, nil
}

type decodeTarget struct {
	data string
	out  *[]string
}

func decodeLists(targets []decodeTarget) error {
	for _, target := range targets {
		if err := json.Unmarshal([]byte(target.data), target.out); err != nil {
			return fmt.Errorf("decode API token scope: %w", err)
		}
	}
	return nil
}

func ipAllowed(remoteIP string, cidrs []string) bool {
	if len(cidrs) == 0 {
		return true
	}
	ip := net.ParseIP(strings.TrimSpace(remoteIP))
	if ip == nil {
		return false
	}
	for _, value := range cidrs {
		_, network, err := net.ParseCIDR(strings.TrimSpace(value))
		if err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !containsFold(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func (s *Service) recordAudit(ctx context.Context, eventType, publicID string, details map[string]any) error {
	if details == nil {
		details = map[string]any{}
	}
	encoded, _ := json.Marshal(details)
	auditID, err := randomID("audit_", s.random)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO audit_events (public_id, event_type, actor_type, entity_type, entity_public_id, details_json, created_at_utc)
		VALUES (?, ?, 'admin', 'api_token', ?, ?, ?)
	`, auditID, eventType, publicID, string(encoded), formatTime(s.now().UTC()))
	return err
}

func randomID(prefix string, source io.Reader) (string, error) {
	value := make([]byte, 18)
	if _, err := io.ReadFull(source, value); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func parseNullableTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
