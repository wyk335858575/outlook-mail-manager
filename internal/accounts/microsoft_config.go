package accounts

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

var microsoftClientIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type MicrosoftConfig struct {
	MicrosoftConfigured bool   `json:"microsoft_configured"`
	ClientID            string `json:"client_id"`
	UpdatedAt           string `json:"updated_at,omitempty"`
}

func (s *Service) GetMicrosoftConfig(ctx context.Context) (MicrosoftConfig, error) {
	var clientID string
	var updatedAt sql.NullString
	if err := s.db.QueryRowContext(ctx, `
		SELECT client_id, updated_at_utc FROM microsoft_oauth_config WHERE id = 1
	`).Scan(&clientID, &updatedAt); err != nil {
		return MicrosoftConfig{}, fmt.Errorf("load Microsoft OAuth config: %w", err)
	}
	if clientID != "" {
		var err error
		clientID, err = normalizeMicrosoftClientID(clientID)
		if err != nil {
			return MicrosoftConfig{}, fmt.Errorf("load Microsoft OAuth config: %w", err)
		}
	}
	return MicrosoftConfig{
		MicrosoftConfigured: clientID != "",
		ClientID:            clientID,
		UpdatedAt:           updatedAt.String,
	}, nil
}

func (s *Service) UpdateMicrosoftConfig(ctx context.Context, clientID string) (MicrosoftConfig, error) {
	normalized, err := normalizeMicrosoftClientID(clientID)
	if err != nil {
		return MicrosoftConfig{}, err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MicrosoftConfig{}, fmt.Errorf("begin Microsoft OAuth config update: %w", err)
	}
	defer tx.Rollback()

	var previous string
	var previousUpdatedAt sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT client_id, updated_at_utc FROM microsoft_oauth_config WHERE id = 1
	`).Scan(&previous, &previousUpdatedAt); err != nil {
		return MicrosoftConfig{}, fmt.Errorf("load Microsoft OAuth config for update: %w", err)
	}
	if strings.EqualFold(previous, normalized) {
		return MicrosoftConfig{
			MicrosoftConfigured: true,
			ClientID:            normalized,
			UpdatedAt:           previousUpdatedAt.String,
		}, nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE microsoft_oauth_config SET client_id = ?, updated_at_utc = ? WHERE id = 1
	`, normalized, formatTime(now)); err != nil {
		return MicrosoftConfig{}, fmt.Errorf("update Microsoft OAuth config: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE account_tokens SET oauth_client_id = ? WHERE oauth_client_id = ''
	`, normalized); err != nil {
		return MicrosoftConfig{}, fmt.Errorf("backfill account OAuth client IDs: %w", err)
	}
	if err := insertAudit(ctx, tx, "microsoft_oauth_config_updated", "admin", map[string]any{
		"previous_client_id": previous,
		"client_id":          normalized,
	}, now); err != nil {
		return MicrosoftConfig{}, err
	}
	if err := tx.Commit(); err != nil {
		return MicrosoftConfig{}, fmt.Errorf("commit Microsoft OAuth config update: %w", err)
	}
	return MicrosoftConfig{
		MicrosoftConfigured: true,
		ClientID:            normalized,
		UpdatedAt:           formatTime(now),
	}, nil
}

func (s *Service) bootstrapMicrosoftConfig(ctx context.Context, clientID string) error {
	normalized := ""
	var err error
	if strings.TrimSpace(clientID) != "" {
		normalized, err = normalizeMicrosoftClientID(clientID)
		if err != nil {
			return fmt.Errorf("bootstrap Microsoft OAuth config: %w", err)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Microsoft OAuth config bootstrap: %w", err)
	}
	defer tx.Rollback()

	var current string
	if err := tx.QueryRowContext(ctx, `
		SELECT client_id FROM microsoft_oauth_config WHERE id = 1
	`).Scan(&current); err != nil {
		return fmt.Errorf("load Microsoft OAuth bootstrap config: %w", err)
	}
	if current != "" {
		current, err = normalizeMicrosoftClientID(current)
		if err != nil {
			return fmt.Errorf("validate stored Microsoft OAuth config: %w", err)
		}
	}
	if current == "" && normalized != "" {
		now := s.now().UTC()
		if _, err := tx.ExecContext(ctx, `
			UPDATE microsoft_oauth_config SET client_id = ?, updated_at_utc = ? WHERE id = 1
		`, normalized, formatTime(now)); err != nil {
			return fmt.Errorf("seed Microsoft OAuth config: %w", err)
		}
		if err := insertAudit(ctx, tx, "microsoft_oauth_config_bootstrapped", "system", map[string]any{
			"client_id": normalized,
		}, now); err != nil {
			return err
		}
		current = normalized
	}
	if current != "" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE account_tokens SET oauth_client_id = ? WHERE oauth_client_id = ''
		`, current); err != nil {
			return fmt.Errorf("backfill legacy account OAuth client IDs: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Microsoft OAuth config bootstrap: %w", err)
	}
	return nil
}

func normalizeMicrosoftClientID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !microsoftClientIDPattern.MatchString(value) {
		return "", ErrInvalidMicrosoftClientID
	}
	return strings.ToLower(value), nil
}
