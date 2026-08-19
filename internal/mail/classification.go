package mail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Category string

const (
	CategoryImportant    Category = "important"
	CategoryVerification Category = "verification"
	CategoryMarketing    Category = "marketing"
	CategorySpam         Category = "spam"
	CategoryNormal       Category = "normal"
	CategoryUncertain    Category = "uncertain"
)

var (
	verificationAfterKeyword  = regexp.MustCompile(`(?i)(?:verification|security|login|one[- ]time|otp|passcode|code|验证码|校验码|动态码)[^0-9]{0,24}([0-9]{4,8})`)
	verificationBeforeKeyword = regexp.MustCompile(`(?i)([0-9]{4,8})[^a-z0-9\p{Han}]{0,16}(?:is your|verification|security|login|otp|passcode|code|验证码|校验码|动态码)`)
	verificationSubject       = regexp.MustCompile(`(?i)(?:\b(?:verification|security|login|one[- ]time|otp|passcode)\s+(?:code|password)\b|验证码|校验码|动态码|一次性密码)`)
)

type ClassificationRule struct {
	PublicID        string    `json:"public_id"`
	Name            string    `json:"name"`
	MatchField      string    `json:"match_field"`
	MatchOperator   string    `json:"match_operator"`
	MatchValue      string    `json:"match_value"`
	TargetCategory  *Category `json:"target_category,omitempty"`
	ProtectsCleanup bool      `json:"protects_cleanup"`
	Priority        int       `json:"priority"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type ClassificationInput struct {
	Subject       string
	SenderAddress string
	Body          string
	Folder        string
	Flagged       bool
	AccountLocked bool
}

type ClassificationResult struct {
	Category         Category
	Reason           string
	Source           string
	VerificationCode string
	Protected        bool
	ProtectionReason string
}

type CleanupEligibility struct {
	Eligible bool
	Reason   string
}

func classifyMessage(input ClassificationInput, rules []ClassificationRule) ClassificationResult {
	result := ClassificationResult{Category: CategoryNormal, Reason: "未命中分类规则", Source: "builtin"}
	sorted := append([]ClassificationRule(nil), rules...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Priority > sorted[j].Priority })
	for _, rule := range sorted {
		if !rule.Enabled || !ruleMatches(rule, input) {
			continue
		}
		if rule.ProtectsCleanup && !result.Protected {
			result.Protected = true
			result.ProtectionReason = "命中保护规则：" + rule.Name
		}
		if rule.TargetCategory != nil && result.Source != "rule" {
			result.Category = *rule.TargetCategory
			result.Reason = "命中规则：" + rule.Name
			result.Source = "rule"
		}
	}

	if code := extractVerificationCode(input.Subject + "\n" + input.Body); code != "" {
		result.VerificationCode = code
		if !result.Protected {
			result.Protected = true
			result.ProtectionReason = "检测到验证码格式"
		}
		if result.Source != "rule" {
			result.Category = CategoryVerification
			result.Reason = "检测到验证码格式"
		}
	} else if verificationSubject.MatchString(input.Subject) {
		if !result.Protected {
			result.Protected = true
			result.ProtectionReason = "检测到验证码主题"
		}
		if result.Source != "rule" {
			result.Category = CategoryVerification
			result.Reason = "检测到验证码主题"
		}
	}
	if result.Source == "rule" {
		return result
	}
	if result.Category == CategoryVerification {
		return result
	}

	text := strings.ToLower(input.Subject + "\n" + input.Body)
	switch {
	case strings.EqualFold(input.Folder, "junkemail"):
		result.Category = CategorySpam
		result.Reason = "来自 Microsoft 垃圾邮件文件夹"
	case containsAny(text, "unsubscribe", "manage preferences", "newsletter", "% off", "sale ends", "退订", "促销", "折扣", "优惠券"):
		result.Category = CategoryMarketing
		result.Reason = "检测到营销邮件特征"
	case containsAny(text, "security alert", "unusual sign-in", "password changed", "invoice", "receipt", "账单", "发票", "安全警报"):
		result.Category = CategoryImportant
		result.Reason = "检测到安全或交易信息"
	}
	return result
}

func cleanupEligibilityFor(input ClassificationInput, result ClassificationResult) CleanupEligibility {
	if input.AccountLocked {
		return CleanupEligibility{Reason: "账号已开启清理保护"}
	}
	if input.Flagged {
		return CleanupEligibility{Reason: "星标邮件受保护"}
	}
	if result.Protected {
		return CleanupEligibility{Reason: result.ProtectionReason}
	}
	if result.Category != CategoryMarketing && result.Category != CategorySpam {
		return CleanupEligibility{Reason: "只有营销邮件和垃圾邮件可进入待清理"}
	}
	return CleanupEligibility{Eligible: true, Reason: result.Reason}
}

func ruleMatches(rule ClassificationRule, input ClassificationInput) bool {
	needle := strings.ToLower(strings.TrimSpace(rule.MatchValue))
	if needle == "" {
		return false
	}
	var value string
	switch rule.MatchField {
	case "sender":
		value = input.SenderAddress
	case "domain":
		_, value, _ = strings.Cut(input.SenderAddress, "@")
	case "subject":
		value = input.Subject
	case "body":
		value = input.Body
	default:
		return false
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if rule.MatchOperator == "equals" {
		return value == needle
	}
	return rule.MatchOperator == "contains" && strings.Contains(value, needle)
}

func extractVerificationCode(value string) string {
	value = truncateForClassification(value, 4096)
	if match := verificationAfterKeyword.FindStringSubmatch(value); len(match) == 2 {
		return match[1]
	}
	if match := verificationBeforeKeyword.FindStringSubmatch(value); len(match) == 2 {
		return match[1]
	}
	return ""
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func truncateForClassification(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}

func validCategory(value Category) bool {
	switch value {
	case CategoryImportant, CategoryVerification, CategoryMarketing, CategorySpam, CategoryNormal, CategoryUncertain:
		return true
	default:
		return false
	}
}

func (s *Service) ListClassificationRules(ctx context.Context) ([]ClassificationRule, error) {
	return loadClassificationRules(ctx, s.db)
}

func (s *Service) CreateClassificationRule(ctx context.Context, rule ClassificationRule) (ClassificationRule, error) {
	if err := validateClassificationRule(rule); err != nil {
		return ClassificationRule{}, err
	}
	now := s.now().UTC()
	publicID, err := randomPublicID("rule_", s.random)
	if err != nil {
		return ClassificationRule{}, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO classification_rules (
			public_id, name, match_field, match_operator, match_value, target_category,
			protects_cleanup, priority, enabled, created_at_utc, updated_at_utc
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, publicID, strings.TrimSpace(rule.Name), rule.MatchField, rule.MatchOperator,
		strings.TrimSpace(rule.MatchValue), nullableCategory(rule.TargetCategory), rule.ProtectsCleanup,
		rule.Priority, rule.Enabled, formatTime(now), formatTime(now))
	if err != nil {
		return ClassificationRule{}, fmt.Errorf("create classification rule: %w", err)
	}
	return s.getClassificationRule(ctx, publicID)
}

func (s *Service) DeleteClassificationRule(ctx context.Context, publicID string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM classification_rules WHERE public_id = ?", strings.TrimSpace(publicID))
	if err != nil {
		return fmt.Errorf("delete classification rule: %w", err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrRuleNotFound
	}
	return nil
}

func (s *Service) CorrectMessageCategory(ctx context.Context, publicID string, category Category) error {
	if !validCategory(category) {
		return ErrInvalidCategory
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin message correction: %w", err)
	}
	defer tx.Rollback()
	var messageID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM messages WHERE public_id = ? AND remote_deleted = 0`, strings.TrimSpace(publicID)).Scan(&messageID); errors.Is(err, sql.ErrNoRows) {
		return ErrMessageNotFound
	} else if err != nil {
		return fmt.Errorf("load corrected message: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE messages SET category = ?, classification_reason = '管理员人工纠正',
			classification_source = 'manual', updated_at_utc = ? WHERE id = ?
	`, category, formatTime(s.now().UTC()), messageID); err != nil {
		return fmt.Errorf("save message correction: %w", err)
	}
	if err := s.refreshCleanupCandidateTx(ctx, tx, messageID); err != nil {
		return err
	}
	if err := s.recordAuditTx(ctx, tx, "classification.corrected", "admin", "message", publicID,
		map[string]any{"category": category}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) ReclassifyAll(ctx context.Context) error {
	rules, err := s.ListClassificationRules(ctx)
	if err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.subject, m.sender_address, COALESCE(m.body_text, ''), f.well_known_name,
			m.is_flagged, a.cleanup_protected
		FROM messages m
		JOIN folders f ON f.id = m.folder_id
		JOIN accounts a ON a.id = m.account_id
		WHERE m.remote_deleted = 0 AND m.classification_source != 'manual'
		ORDER BY m.id
	`)
	if err != nil {
		return fmt.Errorf("list messages for reclassification: %w", err)
	}
	type row struct {
		id    int64
		input ClassificationInput
	}
	items := make([]row, 0)
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.id, &item.input.Subject, &item.input.SenderAddress, &item.input.Body,
			&item.input.Folder, &item.input.Flagged, &item.input.AccountLocked); err != nil {
			rows.Close()
			return fmt.Errorf("scan message for reclassification: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range items {
		if err := s.applyClassification(ctx, item.id, item.input, rules); err != nil {
			return err
		}
	}
	return nil
}

type classificationExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadClassificationRules(ctx context.Context, db classificationExecer) ([]ClassificationRule, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT public_id, name, match_field, match_operator, match_value, target_category,
			protects_cleanup, priority, enabled, created_at_utc, updated_at_utc
		FROM classification_rules ORDER BY priority DESC, id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list classification rules: %w", err)
	}
	defer rows.Close()
	result := make([]ClassificationRule, 0)
	for rows.Next() {
		var rule ClassificationRule
		var category, created, updated sql.NullString
		if err := rows.Scan(&rule.PublicID, &rule.Name, &rule.MatchField, &rule.MatchOperator, &rule.MatchValue,
			&category, &rule.ProtectsCleanup, &rule.Priority, &rule.Enabled, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan classification rule: %w", err)
		}
		if category.Valid {
			value := Category(category.String)
			rule.TargetCategory = &value
		}
		var parseErr error
		rule.CreatedAt, parseErr = parseStoredTime(created.String)
		if parseErr != nil {
			return nil, parseErr
		}
		rule.UpdatedAt, parseErr = parseStoredTime(updated.String)
		if parseErr != nil {
			return nil, parseErr
		}
		result = append(result, rule)
	}
	return result, rows.Err()
}

func (s *Service) getClassificationRule(ctx context.Context, publicID string) (ClassificationRule, error) {
	rules, err := s.ListClassificationRules(ctx)
	if err != nil {
		return ClassificationRule{}, err
	}
	for _, rule := range rules {
		if rule.PublicID == publicID {
			return rule, nil
		}
	}
	return ClassificationRule{}, ErrRuleNotFound
}

func validateClassificationRule(rule ClassificationRule) error {
	if strings.TrimSpace(rule.Name) == "" || strings.TrimSpace(rule.MatchValue) == "" {
		return ErrInvalidRule
	}
	if rule.MatchField != "sender" && rule.MatchField != "domain" && rule.MatchField != "subject" && rule.MatchField != "body" {
		return ErrInvalidRule
	}
	if rule.MatchOperator != "equals" && rule.MatchOperator != "contains" {
		return ErrInvalidRule
	}
	if rule.TargetCategory != nil && !validCategory(*rule.TargetCategory) {
		return ErrInvalidRule
	}
	if rule.TargetCategory == nil && !rule.ProtectsCleanup {
		return ErrInvalidRule
	}
	if rule.Priority < 0 || rule.Priority > 1000 {
		return ErrInvalidRule
	}
	return nil
}

func nullableCategory(category *Category) any {
	if category == nil {
		return nil
	}
	return string(*category)
}

func (s *Service) applyClassification(ctx context.Context, messageID int64, input ClassificationInput, rules []ClassificationRule) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.applyClassificationTx(ctx, tx, messageID, input, rules); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) applyClassificationTx(ctx context.Context, tx *sql.Tx, messageID int64, input ClassificationInput, rules []ClassificationRule) error {
	result := classifyMessage(input, rules)
	protectionReason := any(nil)
	if result.ProtectionReason != "" {
		protectionReason = result.ProtectionReason
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE messages SET category = ?, classification_reason = ?, classification_source = ?,
			verification_code = ?, cleanup_protected = ?, cleanup_protection_reason = ?, updated_at_utc = ?
		WHERE id = ? AND classification_source != 'manual'
	`, result.Category, result.Reason, result.Source, nullableString(result.VerificationCode), result.Protected,
		protectionReason, formatTime(s.now().UTC()), messageID); err != nil {
		return fmt.Errorf("save message classification: %w", err)
	}
	return s.refreshCleanupCandidateTx(ctx, tx, messageID)
}

func (s *Service) refreshCleanupCandidateTx(ctx context.Context, tx *sql.Tx, messageID int64) error {
	var input ClassificationInput
	var category Category
	var reason string
	var messageProtected bool
	var protectionReason sql.NullString
	var publicID string
	if err := tx.QueryRowContext(ctx, `
		SELECT m.public_id, m.subject, m.sender_address, COALESCE(m.body_text, ''), f.well_known_name,
			m.is_flagged, a.cleanup_protected, m.category, m.classification_reason,
			m.cleanup_protected, m.cleanup_protection_reason
		FROM messages m JOIN folders f ON f.id = m.folder_id JOIN accounts a ON a.id = m.account_id
		WHERE m.id = ?
	`, messageID).Scan(&publicID, &input.Subject, &input.SenderAddress, &input.Body, &input.Folder,
		&input.Flagged, &input.AccountLocked, &category, &reason, &messageProtected, &protectionReason); err != nil {
		return fmt.Errorf("load cleanup eligibility: %w", err)
	}
	result := ClassificationResult{Category: category, Reason: reason, Protected: messageProtected, ProtectionReason: protectionReason.String}
	eligibility := cleanupEligibilityFor(input, result)
	now := s.now().UTC()
	if eligibility.Eligible {
		actionID, err := randomPublicID("clean_", s.random)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO cleanup_actions (
				public_id, message_id, state, candidate_reason, created_at_utc, updated_at_utc
			) VALUES (?, ?, 'candidate', ?, ?, ?)
			ON CONFLICT(message_id) DO UPDATE SET
				state = CASE WHEN cleanup_actions.state IN ('candidate', 'dismissed', 'restored', 'failed') THEN 'candidate' ELSE cleanup_actions.state END,
				candidate_reason = excluded.candidate_reason,
				last_error = CASE WHEN cleanup_actions.state IN ('candidate', 'dismissed', 'restored', 'failed') THEN NULL ELSE cleanup_actions.last_error END,
				updated_at_utc = excluded.updated_at_utc
		`, actionID, messageID, eligibility.Reason, formatTime(now), formatTime(now)); err != nil {
			return fmt.Errorf("save cleanup candidate: %w", err)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE cleanup_actions SET state = 'dismissed', last_error = NULL, updated_at_utc = ?
		WHERE message_id = ? AND state IN ('candidate', 'restored', 'failed')
	`, formatTime(now), messageID); err != nil {
		return fmt.Errorf("dismiss ineligible cleanup candidate: %w", err)
	}
	return nil
}
