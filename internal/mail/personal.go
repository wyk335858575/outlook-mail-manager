package mail

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type PersonalInboxRule struct {
	PublicID         string    `json:"public_id"`
	Name             string    `json:"name"`
	Enabled          bool      `json:"enabled"`
	AccountPublicIDs []string  `json:"account_public_ids"`
	GroupNames       []string  `json:"group_names"`
	TagNames         []string  `json:"tag_names"`
	Categories       []string  `json:"categories"`
	SenderAddress    string    `json:"sender_address"`
	SenderDomain     string    `json:"sender_domain"`
	SubjectKeywords  []string  `json:"subject_keywords"`
	RequireOTP       bool      `json:"require_otp"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (s *Service) ListPersonalInboxRules(ctx context.Context) ([]PersonalInboxRule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT public_id, name, enabled, account_public_ids_json, group_names_json,
			 tag_names_json, categories_json, sender_address, sender_domain,
			 subject_keywords_json, require_otp, created_at_utc, updated_at_utc
		FROM personal_inbox_rules ORDER BY name COLLATE NOCASE, id
	`)
	if err != nil {
		return nil, fmt.Errorf("list personal inbox rules: %w", err)
	}
	defer rows.Close()
	items := make([]PersonalInboxRule, 0)
	for rows.Next() {
		item, err := scanPersonalInboxRule(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) CreatePersonalInboxRule(ctx context.Context, rule PersonalInboxRule) (PersonalInboxRule, error) {
	rule = normalizePersonalInboxRule(rule)
	if err := validatePersonalInboxRule(rule); err != nil {
		return PersonalInboxRule{}, err
	}
	publicID, err := randomPublicID("personal_rule_", s.random)
	if err != nil {
		return PersonalInboxRule{}, err
	}
	values, err := encodePersonalRuleLists(rule)
	if err != nil {
		return PersonalInboxRule{}, err
	}
	now := formatTime(s.now().UTC())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PersonalInboxRule{}, fmt.Errorf("begin personal rule create: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO personal_inbox_rules (
			public_id, name, enabled, account_public_ids_json, group_names_json,
			tag_names_json, categories_json, sender_address, sender_domain,
			subject_keywords_json, require_otp, created_at_utc, updated_at_utc
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, publicID, rule.Name, rule.Enabled, values.accounts, values.groups, values.tags,
		values.categories, rule.SenderAddress, rule.SenderDomain, values.keywords,
		rule.RequireOTP, now, now); err != nil {
		return PersonalInboxRule{}, fmt.Errorf("create personal inbox rule: %w", err)
	}
	if err := s.recordAuditTx(ctx, tx, "personal_inbox.rule_created", "admin", "personal_inbox_rule", publicID, map[string]any{}); err != nil {
		return PersonalInboxRule{}, err
	}
	if err := tx.Commit(); err != nil {
		return PersonalInboxRule{}, fmt.Errorf("commit personal rule create: %w", err)
	}
	return s.getPersonalInboxRule(ctx, publicID)
}

func (s *Service) UpdatePersonalInboxRule(ctx context.Context, publicID string, rule PersonalInboxRule) (PersonalInboxRule, error) {
	publicID = strings.TrimSpace(publicID)
	rule = normalizePersonalInboxRule(rule)
	if err := validatePersonalInboxRule(rule); err != nil {
		return PersonalInboxRule{}, err
	}
	values, err := encodePersonalRuleLists(rule)
	if err != nil {
		return PersonalInboxRule{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PersonalInboxRule{}, fmt.Errorf("begin personal rule update: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE personal_inbox_rules SET name = ?, enabled = ?, account_public_ids_json = ?,
			group_names_json = ?, tag_names_json = ?, categories_json = ?, sender_address = ?,
			sender_domain = ?, subject_keywords_json = ?, require_otp = ?, updated_at_utc = ?
		WHERE public_id = ?
	`, rule.Name, rule.Enabled, values.accounts, values.groups, values.tags, values.categories,
		rule.SenderAddress, rule.SenderDomain, values.keywords, rule.RequireOTP,
		formatTime(s.now().UTC()), publicID)
	if err != nil {
		return PersonalInboxRule{}, fmt.Errorf("update personal inbox rule: %w", err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return PersonalInboxRule{}, ErrPersonalRuleNotFound
	}
	if err := s.recordAuditTx(ctx, tx, "personal_inbox.rule_updated", "admin", "personal_inbox_rule", publicID, map[string]any{}); err != nil {
		return PersonalInboxRule{}, err
	}
	if err := tx.Commit(); err != nil {
		return PersonalInboxRule{}, fmt.Errorf("commit personal rule update: %w", err)
	}
	return s.getPersonalInboxRule(ctx, publicID)
}

func (s *Service) DeletePersonalInboxRule(ctx context.Context, publicID string) error {
	publicID = strings.TrimSpace(publicID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin personal rule delete: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM personal_inbox_rules WHERE public_id = ?`, publicID)
	if err != nil {
		return fmt.Errorf("delete personal inbox rule: %w", err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrPersonalRuleNotFound
	}
	if err := s.recordAuditTx(ctx, tx, "personal_inbox.rule_deleted", "admin", "personal_inbox_rule", publicID, map[string]any{}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit personal rule delete: %w", err)
	}
	return nil
}

func (s *Service) getPersonalInboxRule(ctx context.Context, publicID string) (PersonalInboxRule, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT public_id, name, enabled, account_public_ids_json, group_names_json,
			tag_names_json, categories_json, sender_address, sender_domain,
			subject_keywords_json, require_otp, created_at_utc, updated_at_utc
		FROM personal_inbox_rules WHERE public_id = ?
	`, strings.TrimSpace(publicID))
	item, err := scanPersonalInboxRule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PersonalInboxRule{}, ErrPersonalRuleNotFound
	}
	return item, err
}

type personalRuleScanner interface {
	Scan(...any) error
}

func scanPersonalInboxRule(scanner personalRuleScanner) (PersonalInboxRule, error) {
	var item PersonalInboxRule
	var accountsJSON, groupsJSON, tagsJSON, categoriesJSON, keywordsJSON, created, updated string
	if err := scanner.Scan(&item.PublicID, &item.Name, &item.Enabled, &accountsJSON, &groupsJSON,
		&tagsJSON, &categoriesJSON, &item.SenderAddress, &item.SenderDomain, &keywordsJSON,
		&item.RequireOTP, &created, &updated); err != nil {
		return PersonalInboxRule{}, err
	}
	for _, value := range []struct {
		data string
		dest *[]string
	}{
		{accountsJSON, &item.AccountPublicIDs}, {groupsJSON, &item.GroupNames},
		{tagsJSON, &item.TagNames}, {categoriesJSON, &item.Categories},
		{keywordsJSON, &item.SubjectKeywords},
	} {
		if err := json.Unmarshal([]byte(value.data), value.dest); err != nil {
			return PersonalInboxRule{}, fmt.Errorf("decode personal rule conditions: %w", err)
		}
	}
	var err error
	item.CreatedAt, err = parseStoredTime(created)
	if err != nil {
		return PersonalInboxRule{}, err
	}
	item.UpdatedAt, err = parseStoredTime(updated)
	return item, err
}

type encodedPersonalRuleLists struct {
	accounts, groups, tags, categories, keywords string
}

func encodePersonalRuleLists(rule PersonalInboxRule) (encodedPersonalRuleLists, error) {
	encode := func(values []string) (string, error) {
		if values == nil {
			values = []string{}
		}
		data, err := json.Marshal(values)
		return string(data), err
	}
	var values encodedPersonalRuleLists
	var err error
	if values.accounts, err = encode(rule.AccountPublicIDs); err != nil {
		return values, err
	}
	if values.groups, err = encode(rule.GroupNames); err != nil {
		return values, err
	}
	if values.tags, err = encode(rule.TagNames); err != nil {
		return values, err
	}
	if values.categories, err = encode(rule.Categories); err != nil {
		return values, err
	}
	if values.keywords, err = encode(rule.SubjectKeywords); err != nil {
		return values, err
	}
	return values, nil
}

func normalizePersonalInboxRule(rule PersonalInboxRule) PersonalInboxRule {
	rule.Name = strings.TrimSpace(rule.Name)
	rule.SenderAddress = strings.TrimSpace(rule.SenderAddress)
	rule.SenderDomain = strings.TrimSpace(rule.SenderDomain)
	rule.AccountPublicIDs = normalizePersonalValues(rule.AccountPublicIDs)
	rule.GroupNames = normalizePersonalValues(rule.GroupNames)
	rule.TagNames = normalizePersonalValues(rule.TagNames)
	rule.Categories = normalizePersonalValues(rule.Categories)
	rule.SubjectKeywords = normalizePersonalValues(rule.SubjectKeywords)
	return rule
}

func normalizePersonalValues(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func validatePersonalInboxRule(rule PersonalInboxRule) error {
	if rule.Name == "" {
		return ErrInvalidPersonalRule
	}
	for _, category := range rule.Categories {
		if !validCategory(Category(category)) {
			return ErrInvalidPersonalRule
		}
	}
	if len(rule.AccountPublicIDs) == 0 && len(rule.GroupNames) == 0 && len(rule.TagNames) == 0 &&
		len(rule.Categories) == 0 && rule.SenderAddress == "" && rule.SenderDomain == "" &&
		len(rule.SubjectKeywords) == 0 && !rule.RequireOTP {
		return ErrInvalidPersonalRule
	}
	return nil
}
