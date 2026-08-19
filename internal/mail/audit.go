package mail

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

type AuditEvent struct {
	PublicID       string         `json:"public_id"`
	EventType      string         `json:"event_type"`
	Actor          string         `json:"actor"`
	EntityType     string         `json:"entity_type"`
	EntityPublicID string         `json:"entity_public_id"`
	Detail         map[string]any `json:"detail"`
	CreatedAt      time.Time      `json:"created_at"`
}

func (s *Service) ListAuditEvents(ctx context.Context, limit int) ([]AuditEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(public_id, 'audit_legacy_' || id), event_type, actor_type,
			COALESCE(entity_type, 'system'), COALESCE(entity_public_id, ''), details_json, created_at_utc
		FROM audit_events ORDER BY created_at_utc DESC, id DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	items := make([]AuditEvent, 0)
	for rows.Next() {
		var item AuditEvent
		var detailJSON, created string
		if err := rows.Scan(&item.PublicID, &item.EventType, &item.Actor, &item.EntityType,
			&item.EntityPublicID, &detailJSON, &created); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		if err := json.Unmarshal([]byte(detailJSON), &item.Detail); err != nil {
			return nil, fmt.Errorf("decode audit detail: %w", err)
		}
		item.CreatedAt, err = parseStoredTime(created)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) recordAuditTx(
	ctx context.Context,
	tx *sql.Tx,
	eventType, actor, entityType, entityPublicID string,
	detail map[string]any,
) error {
	publicID, err := randomPublicID("audit_", s.random)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode audit detail: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_events (
			public_id, event_type, actor_type, entity_type, entity_public_id, details_json, created_at_utc
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, publicID, eventType, actor, entityType, entityPublicID, string(payload), formatTime(s.now().UTC())); err != nil {
		return fmt.Errorf("record audit event: %w", err)
	}
	return nil
}

func randomPublicID(prefix string, source io.Reader) (string, error) {
	if source == nil {
		source = rand.Reader
	}
	value := make([]byte, 18)
	if _, err := io.ReadFull(source, value); err != nil {
		return "", fmt.Errorf("generate %s identifier: %w", strings.TrimSuffix(prefix, "_"), err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}
