package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var (
	ErrChannelNotFound  = errors.New("notification channel not found")
	ErrRuleNotFound     = errors.New("notification rule not found")
	ErrDeliveryNotFound = errors.New("notification delivery not found")
	ErrInvalidChannel   = errors.New("invalid notification channel")
	ErrInvalidRule      = errors.New("invalid notification rule")
)

type Keyring interface {
	SealString(string, string) (string, error)
	OpenString(string, string) (string, error)
}

type Options struct {
	HTTPClient      *http.Client
	Now             func() time.Time
	Random          io.Reader
	Workers         int
	TelegramBaseURL string
	PushPlusURL     string
}

type Service struct {
	db              *sql.DB
	keyring         Keyring
	httpClient      *http.Client
	now             func() time.Time
	random          io.Reader
	workers         int
	telegramBaseURL string
	pushPlusURL     string
	wxPushBaseURL   string
	wxPushTokenMu   sync.Mutex
	wxPushTokens    map[[32]byte]wxPushToken
	ctx             context.Context
	cancel          context.CancelFunc
	wake            chan struct{}
	startOnce       sync.Once
	closeOnce       sync.Once
	wait            sync.WaitGroup
}

type ChannelInput struct {
	Name             string `json:"name"`
	Kind             string `json:"kind"`
	Enabled          bool   `json:"enabled"`
	SystemEnabled    bool   `json:"system_enabled"`
	TelegramBotToken string `json:"telegram_bot_token,omitempty"`
	TelegramChatID   string `json:"telegram_chat_id,omitempty"`
	PushPlusToken    string `json:"pushplus_token,omitempty"`
	PushPlusTopic    string `json:"pushplus_topic,omitempty"`
	WXPushAppID      string `json:"wxpush_app_id,omitempty"`
	WXPushAppSecret  string `json:"wxpush_app_secret,omitempty"`
	WXPushUserID     string `json:"wxpush_user_id,omitempty"`
	WXPushTemplateID string `json:"wxpush_template_id,omitempty"`
	BarkServerURL    string `json:"bark_server_url,omitempty"`
	BarkDeviceKey    string `json:"bark_device_key,omitempty"`
	BarkGroup        string `json:"bark_group,omitempty"`
	BarkSound        string `json:"bark_sound,omitempty"`
}

type Channel struct {
	PublicID             string    `json:"public_id"`
	Name                 string    `json:"name"`
	Kind                 string    `json:"kind"`
	Enabled              bool      `json:"enabled"`
	SystemEnabled        bool      `json:"system_enabled"`
	Configured           bool      `json:"configured"`
	Destination          string    `json:"destination"`
	NeedsReconfiguration bool      `json:"needs_reconfiguration"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type Rule struct {
	PublicID         string    `json:"public_id"`
	ChannelPublicID  string    `json:"channel_public_id"`
	ChannelName      string    `json:"channel_name,omitempty"`
	Name             string    `json:"name"`
	Enabled          bool      `json:"enabled"`
	PersonalInbox    bool      `json:"personal_inbox"`
	PersonalOnly     bool      `json:"personal_only"`
	AccountPublicIDs []string  `json:"account_public_ids"`
	GroupNames       []string  `json:"group_names"`
	TagNames         []string  `json:"tag_names"`
	Categories       []string  `json:"categories"`
	SenderAddress    string    `json:"sender_address"`
	SenderDomain     string    `json:"sender_domain"`
	SubjectKeywords  []string  `json:"subject_keywords"`
	StartMinute      int       `json:"start_minute"`
	EndMinute        int       `json:"end_minute"`
	RequireOTP       bool      `json:"require_otp"`
	IncludeOTP       bool      `json:"include_otp"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type Delivery struct {
	PublicID     string     `json:"public_id"`
	ChannelName  string     `json:"channel_name"`
	EventType    string     `json:"event_type"`
	Status       string     `json:"status"`
	AttemptCount int        `json:"attempt_count"`
	LastError    string     `json:"last_error,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	SentAt       *time.Time `json:"sent_at,omitempty"`
}

type channelSecret struct {
	TelegramBotToken string `json:"telegram_bot_token,omitempty"`
	TelegramChatID   string `json:"telegram_chat_id,omitempty"`
	PushPlusToken    string `json:"pushplus_token,omitempty"`
	PushPlusTopic    string `json:"pushplus_topic,omitempty"`
	WXPushAppID      string `json:"wxpush_app_id,omitempty"`
	WXPushAppSecret  string `json:"wxpush_app_secret,omitempty"`
	WXPushUserID     string `json:"wxpush_user_id,omitempty"`
	WXPushTemplateID string `json:"wxpush_template_id,omitempty"`
	BarkServerURL    string `json:"bark_server_url,omitempty"`
	BarkDeviceKey    string `json:"bark_device_key,omitempty"`
	BarkGroup        string `json:"bark_group,omitempty"`
	BarkSound        string `json:"bark_sound,omitempty"`
	// Retained only to identify encrypted gateway configurations created before schema 18.
	WXPushURL   string `json:"wxpush_url,omitempty"`
	WXPushToken string `json:"wxpush_token,omitempty"`
}

type deliveryPayload struct {
	EventType        string `json:"event_type"`
	Title            string `json:"title"`
	Text             string `json:"text"`
	Account          string `json:"account,omitempty"`
	Sender           string `json:"sender,omitempty"`
	Subject          string `json:"subject,omitempty"`
	Body             string `json:"body,omitempty"`
	Category         string `json:"category,omitempty"`
	ReceivedAt       string `json:"received_at,omitempty"`
	VerificationCode string `json:"verification_code,omitempty"`
}

type queuedDelivery struct {
	id              int64
	publicID        string
	channelPublicID string
	kind            string
	configCipher    string
	payload         deliveryPayload
	attemptCount    int
}

func New(db *sql.DB, keyring Keyring, options Options) (*Service, error) {
	if db == nil || keyring == nil {
		return nil, errors.New("notification database and keyring are required")
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.Workers <= 0 {
		options.Workers = 2
	}
	if options.TelegramBaseURL == "" {
		options.TelegramBaseURL = "https://api.telegram.org"
	}
	if options.PushPlusURL == "" {
		options.PushPlusURL = "https://www.pushplus.plus/send"
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		db: db, keyring: keyring, httpClient: options.HTTPClient, now: options.Now, random: options.Random,
		workers: options.Workers, telegramBaseURL: strings.TrimRight(options.TelegramBaseURL, "/"),
		pushPlusURL: options.PushPlusURL, wxPushBaseURL: weChatAPIBaseURL, wxPushTokens: make(map[[32]byte]wxPushToken),
		ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1),
	}, nil
}

func (s *Service) Start() {
	s.startOnce.Do(func() {
		_, _ = s.db.ExecContext(s.ctx, `
			UPDATE notification_deliveries SET status = 'queued', next_retry_at_utc = NULL,
				updated_at_utc = ? WHERE status = 'sending'
		`, formatTime(s.now().UTC()))
		for index := 0; index < s.workers; index++ {
			s.wait.Add(1)
			go s.worker()
		}
		s.signal()
	})
}

func (s *Service) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		s.wait.Wait()
	})
}

func (s *Service) ListChannels(ctx context.Context) ([]Channel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT public_id, name, kind, enabled, system_enabled, config_ciphertext, created_at_utc, updated_at_utc
		FROM notification_channels ORDER BY name COLLATE NOCASE, id
	`)
	if err != nil {
		return nil, fmt.Errorf("list notification channels: %w", err)
	}
	defer rows.Close()
	items := make([]Channel, 0)
	for rows.Next() {
		var item Channel
		var cipher, created, updated string
		if err := rows.Scan(&item.PublicID, &item.Name, &item.Kind, &item.Enabled, &item.SystemEnabled,
			&cipher, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan notification channel: %w", err)
		}
		secret, err := s.openChannel(item.PublicID, cipher)
		if err == nil {
			item.NeedsReconfiguration = item.Kind == "wxpush" && isLegacyWXPush(secret)
			item.Configured = !item.NeedsReconfiguration
			item.Destination = channelDestination(item.Kind, secret)
		}
		item.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, err
		}
		item.UpdatedAt, err = parseTime(updated)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) CreateChannel(ctx context.Context, input ChannelInput) (Channel, error) {
	secret := secretFromInput(input)
	if err := validateChannel(input, secret); err != nil {
		return Channel{}, err
	}
	publicID, err := randomID("channel_", s.random)
	if err != nil {
		return Channel{}, err
	}
	cipher, err := s.sealChannel(publicID, secret)
	if err != nil {
		return Channel{}, err
	}
	now := s.now().UTC()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO notification_channels (
			public_id, name, kind, config_ciphertext, enabled, system_enabled, created_at_utc, updated_at_utc
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, publicID, strings.TrimSpace(input.Name), input.Kind, cipher, input.Enabled, input.SystemEnabled,
		formatTime(now), formatTime(now)); err != nil {
		return Channel{}, fmt.Errorf("create notification channel: %w", err)
	}
	_ = s.recordAudit(ctx, "notification.channel_created", "notification_channel", publicID, map[string]any{"kind": input.Kind})
	return s.getChannel(ctx, publicID)
}

func (s *Service) UpdateChannel(ctx context.Context, publicID string, input ChannelInput) (Channel, error) {
	var currentKind, cipher string
	if err := s.db.QueryRowContext(ctx, `SELECT kind, config_ciphertext FROM notification_channels WHERE public_id = ?`,
		strings.TrimSpace(publicID)).Scan(&currentKind, &cipher); errors.Is(err, sql.ErrNoRows) {
		return Channel{}, ErrChannelNotFound
	} else if err != nil {
		return Channel{}, fmt.Errorf("load notification channel: %w", err)
	}
	if input.Kind == "" {
		input.Kind = currentKind
	}
	if input.Kind != currentKind {
		return Channel{}, ErrInvalidChannel
	}
	secret, err := s.openChannel(publicID, cipher)
	if err != nil {
		return Channel{}, err
	}
	overlaySecret(&secret, secretFromInput(input))
	if err := validateChannel(input, secret); err != nil {
		return Channel{}, err
	}
	newCipher, err := s.sealChannel(publicID, secret)
	if err != nil {
		return Channel{}, err
	}
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE notification_channels SET name = ?, config_ciphertext = ?, enabled = ?, system_enabled = ?,
			updated_at_utc = ? WHERE public_id = ?
	`, strings.TrimSpace(input.Name), newCipher, input.Enabled, input.SystemEnabled, formatTime(now), publicID)
	if err != nil {
		return Channel{}, fmt.Errorf("update notification channel: %w", err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return Channel{}, ErrChannelNotFound
	}
	_ = s.recordAudit(ctx, "notification.channel_updated", "notification_channel", publicID, nil)
	return s.getChannel(ctx, publicID)
}

func (s *Service) DeleteChannel(ctx context.Context, publicID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM notification_channels WHERE public_id = ?`, strings.TrimSpace(publicID))
	if err != nil {
		return fmt.Errorf("delete notification channel: %w", err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrChannelNotFound
	}
	_ = s.recordAudit(ctx, "notification.channel_deleted", "notification_channel", publicID, nil)
	return nil
}

func (s *Service) ListRules(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `
	SELECT r.public_id, c.public_id, c.name, r.name, r.enabled, 0, r.personal_only,
			r.account_public_ids_json, r.group_names_json, r.tag_names_json, r.categories_json,
			r.sender_address, r.sender_domain, r.subject_keywords_json, r.start_minute, r.end_minute,
			r.require_otp, r.include_otp, r.created_at_utc, r.updated_at_utc
		FROM notification_rules r JOIN notification_channels c ON c.id = r.channel_id
		ORDER BY r.name COLLATE NOCASE, r.id
	`)
	if err != nil {
		return nil, fmt.Errorf("list notification rules: %w", err)
	}
	defer rows.Close()
	return scanRules(rows)
}

func (s *Service) CreateRule(ctx context.Context, rule Rule) (Rule, error) {
	rule.PersonalInbox = false
	if err := validateRule(rule); err != nil {
		return Rule{}, err
	}
	channelID, err := s.channelID(ctx, rule.ChannelPublicID)
	if err != nil {
		return Rule{}, err
	}
	publicID, err := randomID("notify_rule_", s.random)
	if err != nil {
		return Rule{}, err
	}
	now := s.now().UTC()
	values, err := encodeRuleLists(rule)
	if err != nil {
		return Rule{}, err
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO notification_rules (
			public_id, channel_id, name, enabled, personal_inbox, personal_only, account_public_ids_json, group_names_json,
			tag_names_json, categories_json, sender_address, sender_domain, subject_keywords_json,
			start_minute, end_minute, require_otp, include_otp, created_at_utc, updated_at_utc
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, publicID, channelID, strings.TrimSpace(rule.Name), rule.Enabled, rule.PersonalInbox, rule.PersonalOnly, values.accounts, values.groups,
		values.tags, values.categories, strings.TrimSpace(rule.SenderAddress), strings.TrimSpace(rule.SenderDomain),
		values.keywords, rule.StartMinute, rule.EndMinute, rule.RequireOTP, rule.IncludeOTP,
		formatTime(now), formatTime(now)); err != nil {
		return Rule{}, fmt.Errorf("create notification rule: %w", err)
	}
	_ = s.recordAudit(ctx, "notification.rule_created", "notification_rule", publicID, nil)
	return s.getRule(ctx, publicID)
}

func (s *Service) UpdateRule(ctx context.Context, publicID string, rule Rule) (Rule, error) {
	rule.PersonalInbox = false
	if err := validateRule(rule); err != nil {
		return Rule{}, err
	}
	channelID, err := s.channelID(ctx, rule.ChannelPublicID)
	if err != nil {
		return Rule{}, err
	}
	values, err := encodeRuleLists(rule)
	if err != nil {
		return Rule{}, err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE notification_rules SET channel_id = ?, name = ?, enabled = ?, personal_inbox = ?, personal_only = ?, account_public_ids_json = ?,
			group_names_json = ?, tag_names_json = ?, categories_json = ?, sender_address = ?, sender_domain = ?,
			subject_keywords_json = ?, start_minute = ?, end_minute = ?, require_otp = ?, include_otp = ?,
			updated_at_utc = ? WHERE public_id = ?
	`, channelID, strings.TrimSpace(rule.Name), rule.Enabled, rule.PersonalInbox, rule.PersonalOnly, values.accounts, values.groups, values.tags,
		values.categories, strings.TrimSpace(rule.SenderAddress), strings.TrimSpace(rule.SenderDomain),
		values.keywords, rule.StartMinute, rule.EndMinute, rule.RequireOTP, rule.IncludeOTP,
		formatTime(s.now().UTC()), strings.TrimSpace(publicID))
	if err != nil {
		return Rule{}, fmt.Errorf("update notification rule: %w", err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return Rule{}, ErrRuleNotFound
	}
	_ = s.recordAudit(ctx, "notification.rule_updated", "notification_rule", publicID, nil)
	return s.getRule(ctx, publicID)
}

func (s *Service) DeleteRule(ctx context.Context, publicID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM notification_rules WHERE public_id = ?`, strings.TrimSpace(publicID))
	if err != nil {
		return fmt.Errorf("delete notification rule: %w", err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrRuleNotFound
	}
	_ = s.recordAudit(ctx, "notification.rule_deleted", "notification_rule", publicID, nil)
	return nil
}

func (s *Service) ListDeliveries(ctx context.Context, limit int) ([]Delivery, error) {
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.public_id, c.name, d.event_type, d.status, d.attempt_count,
			COALESCE(d.last_error, ''), d.created_at_utc, d.sent_at_utc
		FROM notification_deliveries d JOIN notification_channels c ON c.id = d.channel_id
		ORDER BY d.created_at_utc DESC, d.id DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list notification deliveries: %w", err)
	}
	defer rows.Close()
	items := make([]Delivery, 0)
	for rows.Next() {
		var item Delivery
		var created string
		var sent sql.NullString
		if err := rows.Scan(&item.PublicID, &item.ChannelName, &item.EventType, &item.Status,
			&item.AttemptCount, &item.LastError, &created, &sent); err != nil {
			return nil, fmt.Errorf("scan notification delivery: %w", err)
		}
		var err error
		item.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, err
		}
		item.SentAt, err = parseNullableTime(sent)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) EnqueueMessage(ctx context.Context, messagePublicID string) error {
	messageID, message, err := s.loadMessageContext(ctx, messagePublicID)
	if err != nil {
		return err
	}
	rules, err := s.ListRules(ctx)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if !rule.Enabled || !rule.PersonalOnly {
			continue
		}
		message.Personal, err = s.isPersonalMessage(ctx, message)
		if err != nil {
			return err
		}
		break
	}
	location := s.settingsLocation(ctx)
	type channelMatch struct {
		rule       Rule
		includeOTP bool
	}
	matches := make([]channelMatch, 0, len(rules))
	matchIndexes := make(map[string]int, len(rules))
	for _, rule := range rules {
		if !ruleMatches(rule, message, location) {
			continue
		}
		index, exists := matchIndexes[rule.ChannelPublicID]
		if !exists {
			matchIndexes[rule.ChannelPublicID] = len(matches)
			matches = append(matches, channelMatch{rule: rule, includeOTP: rule.IncludeOTP})
			continue
		}
		matches[index].includeOTP = matches[index].includeOTP || rule.IncludeOTP
	}
	for _, match := range matches {
		payload := mailPayload(message, match.includeOTP)
		if err := s.insertDelivery(ctx, messageID, &match.rule, match.rule.ChannelPublicID, "mail",
			"mail:"+messagePublicID+":"+match.rule.ChannelPublicID, payload); err != nil {
			return err
		}
	}
	s.signal()
	return nil
}

func (s *Service) EnqueueSystem(ctx context.Context, eventType, summary string) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT public_id FROM notification_channels WHERE enabled = 1 AND system_enabled = 1 ORDER BY id
	`)
	if err != nil {
		return fmt.Errorf("list system notification channels: %w", err)
	}
	channels := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		channels = append(channels, id)
	}
	rows.Close()
	day := s.now().UTC().Format("2006-01-02")
	for _, channel := range channels {
		payload := deliveryPayload{EventType: "system", Title: "邮箱管理台系统提醒", Text: summary}
		if err := s.insertDelivery(ctx, 0, nil, channel, "system", "system:"+eventType+":"+day+":"+channel, payload); err != nil {
			return err
		}
	}
	s.signal()
	return nil
}

func (s *Service) TestChannel(ctx context.Context, publicID string) (Delivery, error) {
	payload := deliveryPayload{EventType: "test", Title: "邮箱管理台测试通知", Text: "通知通道已连接。"}
	dedupe, err := randomID("test_", s.random)
	if err != nil {
		return Delivery{}, err
	}
	if err := s.insertDelivery(ctx, 0, nil, publicID, "test", dedupe, payload); err != nil {
		return Delivery{}, err
	}
	s.signal()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		item, err := s.getDeliveryByDedupe(ctx, dedupe)
		if err == nil && (item.Status == "sent" || item.Status == "failed") {
			return item, nil
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			return item, nil
		case <-ctx.Done():
			return Delivery{}, ctx.Err()
		}
	}
}

func (s *Service) TestConfig(ctx context.Context, input ChannelInput) error {
	if input.Kind != "wxpush" && input.Kind != "bark" {
		return ErrInvalidChannel
	}
	secret := secretFromInput(input)
	payload := deliveryPayload{EventType: "test", Title: "邮箱管理台测试通知", Text: "发件人：sender@example.com\n主题：通知测试邮件\n正文：这是一条动态正文测试。", Sender: "sender@example.com", Subject: "通知测试邮件", Body: "这是一条动态正文测试。"}
	if input.Kind == "bark" {
		if err := validateBarkSecret(secret); err != nil {
			return err
		}
		return s.sendBark(ctx, payload, secret)
	}
	if err := validateWXPushSecret(secret); err != nil {
		return err
	}
	return s.sendWXPush(ctx, payload, secret)
}

func (s *Service) RetryDelivery(ctx context.Context, publicID string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE notification_deliveries SET status = 'queued', next_retry_at_utc = NULL,
			last_error = NULL, updated_at_utc = ? WHERE public_id = ? AND status = 'failed'
	`, formatTime(s.now().UTC()), strings.TrimSpace(publicID))
	if err != nil {
		return fmt.Errorf("retry notification delivery: %w", err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrDeliveryNotFound
	}
	s.signal()
	return nil
}

func (s *Service) worker() {
	defer s.wait.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		processed, err := s.processOne(s.ctx)
		if err == nil && processed {
			continue
		}
		select {
		case <-s.wake:
		case <-ticker.C:
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *Service) processOne(ctx context.Context) (bool, error) {
	delivery, err := s.claimDelivery(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	secret, err := s.openChannel(delivery.channelPublicID, delivery.configCipher)
	if err == nil {
		err = s.send(ctx, delivery, secret)
	}
	now := s.now().UTC()
	if err == nil {
		_, updateErr := s.db.ExecContext(ctx, `
			UPDATE notification_deliveries SET status = 'sent', sent_at_utc = ?, last_error = NULL,
				next_retry_at_utc = NULL, updated_at_utc = ? WHERE id = ?
		`, formatTime(now), formatTime(now), delivery.id)
		return true, updateErr
	}
	attempts := delivery.attemptCount + 1
	status := "queued"
	if attempts >= 5 {
		status = "failed"
	}
	next := now.Add(notificationRetryDelay(attempts))
	_, updateErr := s.db.ExecContext(ctx, `
		UPDATE notification_deliveries SET status = ?, attempt_count = ?, next_retry_at_utc = ?,
			last_error = ?, updated_at_utc = ? WHERE id = ?
	`, status, attempts, formatTime(next), notificationErrorCode(err), formatTime(now), delivery.id)
	return true, updateErr
}

func (s *Service) claimDelivery(ctx context.Context) (queuedDelivery, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return queuedDelivery{}, err
	}
	defer tx.Rollback()
	var item queuedDelivery
	var payloadJSON string
	err = tx.QueryRowContext(ctx, `
		SELECT d.id, d.public_id, c.public_id, c.kind, c.config_ciphertext, d.payload_json, d.attempt_count
		FROM notification_deliveries d JOIN notification_channels c ON c.id = d.channel_id
		WHERE d.status = 'queued' AND c.enabled = 1
			AND (d.next_retry_at_utc IS NULL OR d.next_retry_at_utc <= ?)
		ORDER BY d.created_at_utc, d.id LIMIT 1
	`, formatTime(s.now().UTC())).Scan(&item.id, &item.publicID, &item.channelPublicID, &item.kind,
		&item.configCipher, &payloadJSON, &item.attemptCount)
	if err != nil {
		return queuedDelivery{}, err
	}
	if err := json.Unmarshal([]byte(payloadJSON), &item.payload); err != nil {
		return queuedDelivery{}, fmt.Errorf("decode notification payload: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET status = 'sending', updated_at_utc = ? WHERE id = ? AND status = 'queued'`,
		formatTime(s.now().UTC()), item.id)
	if err != nil {
		return queuedDelivery{}, err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return queuedDelivery{}, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return queuedDelivery{}, err
	}
	return item, nil
}

func (s *Service) send(ctx context.Context, delivery queuedDelivery, secret channelSecret) error {
	switch delivery.kind {
	case "telegram":
		return s.sendTelegram(ctx, delivery.payload, secret)
	case "pushplus":
		return s.sendPushPlus(ctx, delivery.payload, secret)
	case "wxpush":
		return s.sendWXPush(ctx, delivery.payload, secret)
	case "bark":
		return s.sendBark(ctx, delivery.payload, secret)
	default:
		return ErrInvalidChannel
	}
}

func (s *Service) sendTelegram(ctx context.Context, payload deliveryPayload, secret channelSecret) error {
	target := s.telegramBaseURL + "/bot" + url.PathEscape(secret.TelegramBotToken) + "/sendMessage"
	body := map[string]string{"chat_id": secret.TelegramChatID, "text": payload.Title + "\n" + payload.Text}
	var response struct {
		OK bool `json:"ok"`
	}
	if err := s.doJSON(ctx, target, body, &response, nil); err != nil || !response.OK {
		return errors.New("telegram_delivery_failed")
	}
	return nil
}

func (s *Service) sendPushPlus(ctx context.Context, payload deliveryPayload, secret channelSecret) error {
	body := map[string]string{
		"token": secret.PushPlusToken, "title": payload.Title, "content": payload.Text, "template": "txt",
	}
	if secret.PushPlusTopic != "" {
		body["topic"] = secret.PushPlusTopic
	}
	var response struct {
		Code int `json:"code"`
	}
	if err := s.doJSON(ctx, s.pushPlusURL, body, &response, nil); err != nil || response.Code != 200 {
		return errors.New("pushplus_delivery_failed")
	}
	return nil
}

func (s *Service) doJSON(ctx context.Context, target string, requestBody any, destination any, headers map[string]string) error {
	body, err := json.Marshal(requestBody)
	if err != nil {
		return err
	}
	return s.doJSONBytes(ctx, target, body, destination, headers)
}

type notificationHTTPError struct {
	status int
}

func (e notificationHTTPError) Error() string { return "notification_http_failed" }

func (s *Service) doJSONBytes(ctx context.Context, target string, body []byte, destination any, headers map[string]string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return errors.New("notification_request_failed")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := s.httpClient.Do(request)
	if err != nil {
		return errors.New("notification_request_failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return notificationHTTPError{status: response.StatusCode}
	}
	if destination == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(destination); err != nil {
		return errors.New("notification_response_failed")
	}
	return nil
}

func (s *Service) insertDelivery(
	ctx context.Context,
	messageID int64,
	rule *Rule,
	channelPublicID, eventType, dedupe string,
	payload deliveryPayload,
) error {
	channelID, err := s.channelID(ctx, channelPublicID)
	if err != nil {
		return err
	}
	var ruleID any
	if rule != nil {
		var id int64
		if err := s.db.QueryRowContext(ctx, `SELECT id FROM notification_rules WHERE public_id = ?`, rule.PublicID).Scan(&id); err != nil {
			return fmt.Errorf("load notification rule: %w", err)
		}
		ruleID = id
	}
	var storedMessageID any
	if messageID > 0 {
		storedMessageID = messageID
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode notification payload: %w", err)
	}
	publicID, err := randomID("delivery_", s.random)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO notification_deliveries (
			public_id, message_id, rule_id, channel_id, event_type, status, dedupe_key,
			payload_json, created_at_utc, updated_at_utc
		) VALUES (?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?)
		ON CONFLICT(dedupe_key) DO NOTHING
	`, publicID, storedMessageID, ruleID, channelID, eventType, dedupe, string(payloadJSON),
		formatTime(now), formatTime(now))
	if err != nil {
		return fmt.Errorf("queue notification delivery: %w", err)
	}
	return nil
}

func (s *Service) loadMessageContext(ctx context.Context, publicID string) (int64, messageContext, error) {
	var id int64
	var message messageContext
	var received string
	err := s.db.QueryRowContext(ctx, `
		SELECT m.id, a.public_id, m.category, m.sender_address, m.subject,
			COALESCE(m.body_text, ''), COALESCE(m.verification_code, ''), m.received_at_utc
		FROM messages m JOIN accounts a ON a.id = m.account_id
		WHERE m.public_id = ? AND m.remote_deleted = 0
	`, strings.TrimSpace(publicID)).Scan(&id, &message.AccountPublicID, &message.Category,
		&message.SenderAddress, &message.Subject, &message.Body, &message.VerificationCode, &received)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, messageContext{}, errors.New("message not found")
	}
	if err != nil {
		return 0, messageContext{}, fmt.Errorf("load notification message: %w", err)
	}
	message.ReceivedAt, err = parseTime(received)
	if err != nil {
		return 0, messageContext{}, err
	}
	message.Groups, err = s.accountNames(ctx, message.AccountPublicID, true)
	if err != nil {
		return 0, messageContext{}, err
	}
	message.Tags, err = s.accountNames(ctx, message.AccountPublicID, false)
	if err != nil {
		return 0, messageContext{}, err
	}
	return id, message, nil
}

func (s *Service) accountNames(ctx context.Context, accountPublicID string, groups bool) ([]string, error) {
	table, members, foreign := "account_tags", "account_tag_members", "tag_id"
	if groups {
		table, members, foreign = "account_groups", "account_group_members", "group_id"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT n.name FROM `+table+` n JOIN `+members+` m ON m.`+foreign+` = n.id
		JOIN accounts a ON a.id = m.account_id WHERE a.public_id = ? ORDER BY n.name COLLATE NOCASE`, accountPublicID)
	if err != nil {
		return nil, fmt.Errorf("load account notification labels: %w", err)
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func mailPayload(message messageContext, includeOTP bool) deliveryPayload {
	title := "新邮件 · " + categoryLabel(message.Category)
	sender := safeText(message.SenderAddress, 160)
	subject := safeText(message.Subject, 240)
	body := notificationBodyPreview(message.Body)
	text := "发件人：" + sender + "\n主题：" + subject + "\n正文：" + body
	payload := deliveryPayload{
		EventType: "mail", Title: title, Text: text, Account: message.AccountPublicID,
		Sender: sender, Subject: subject, Body: body,
		Category: message.Category, ReceivedAt: formatTime(message.ReceivedAt),
	}
	if includeOTP && message.VerificationCode != "" {
		payload.VerificationCode = message.VerificationCode
		payload.Text += "\n验证码：" + message.VerificationCode
	}
	return payload
}

func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) channelID(ctx context.Context, publicID string) (int64, error) {
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM notification_channels WHERE public_id = ?`, strings.TrimSpace(publicID)).Scan(&id); errors.Is(err, sql.ErrNoRows) {
		return 0, ErrChannelNotFound
	} else if err != nil {
		return 0, fmt.Errorf("load notification channel: %w", err)
	}
	return id, nil
}

func (s *Service) getChannel(ctx context.Context, publicID string) (Channel, error) {
	items, err := s.ListChannels(ctx)
	if err != nil {
		return Channel{}, err
	}
	for _, item := range items {
		if item.PublicID == publicID {
			return item, nil
		}
	}
	return Channel{}, ErrChannelNotFound
}

func (s *Service) getRule(ctx context.Context, publicID string) (Rule, error) {
	items, err := s.ListRules(ctx)
	if err != nil {
		return Rule{}, err
	}
	for _, item := range items {
		if item.PublicID == publicID {
			return item, nil
		}
	}
	return Rule{}, ErrRuleNotFound
}

func (s *Service) getDeliveryByDedupe(ctx context.Context, dedupe string) (Delivery, error) {
	var item Delivery
	var created string
	var sent sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT d.public_id, c.name, d.event_type, d.status, d.attempt_count,
			COALESCE(d.last_error, ''), d.created_at_utc, d.sent_at_utc
		FROM notification_deliveries d JOIN notification_channels c ON c.id = d.channel_id
		WHERE d.dedupe_key = ?
	`, dedupe).Scan(&item.PublicID, &item.ChannelName, &item.EventType, &item.Status,
		&item.AttemptCount, &item.LastError, &created, &sent)
	if err != nil {
		return Delivery{}, err
	}
	item.CreatedAt, err = parseTime(created)
	if err != nil {
		return Delivery{}, err
	}
	item.SentAt, err = parseNullableTime(sent)
	return item, err
}

func (s *Service) settingsLocation(ctx context.Context) *time.Location {
	var name string
	if err := s.db.QueryRowContext(ctx, `SELECT timezone FROM app_settings WHERE id = 1`).Scan(&name); err != nil {
		return time.UTC
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
}

func (s *Service) sealChannel(publicID string, secret channelSecret) (string, error) {
	encoded, err := json.Marshal(secret)
	if err != nil {
		return "", fmt.Errorf("encode notification channel: %w", err)
	}
	return s.keyring.SealString(string(encoded), "notification-channel:"+publicID)
}

func (s *Service) openChannel(publicID, ciphertext string) (channelSecret, error) {
	encoded, err := s.keyring.OpenString(ciphertext, "notification-channel:"+publicID)
	if err != nil {
		return channelSecret{}, err
	}
	var secret channelSecret
	if err := json.Unmarshal([]byte(encoded), &secret); err != nil {
		return channelSecret{}, fmt.Errorf("decode notification channel: %w", err)
	}
	return secret, nil
}

func secretFromInput(input ChannelInput) channelSecret {
	return channelSecret{
		TelegramBotToken: strings.TrimSpace(input.TelegramBotToken), TelegramChatID: strings.TrimSpace(input.TelegramChatID),
		PushPlusToken: strings.TrimSpace(input.PushPlusToken), PushPlusTopic: strings.TrimSpace(input.PushPlusTopic),
		WXPushAppID: strings.TrimSpace(input.WXPushAppID), WXPushAppSecret: strings.TrimSpace(input.WXPushAppSecret),
		WXPushUserID: strings.TrimSpace(input.WXPushUserID), WXPushTemplateID: strings.TrimSpace(input.WXPushTemplateID),
		BarkServerURL: strings.TrimSpace(input.BarkServerURL), BarkDeviceKey: strings.TrimSpace(input.BarkDeviceKey),
		BarkGroup: strings.TrimSpace(input.BarkGroup), BarkSound: strings.TrimSpace(input.BarkSound),
	}
}

func overlaySecret(current *channelSecret, replacement channelSecret) {
	if replacement.TelegramBotToken != "" {
		current.TelegramBotToken = replacement.TelegramBotToken
	}
	if replacement.TelegramChatID != "" {
		current.TelegramChatID = replacement.TelegramChatID
	}
	if replacement.PushPlusToken != "" {
		current.PushPlusToken = replacement.PushPlusToken
	}
	current.PushPlusTopic = replacement.PushPlusTopic
	if replacement.WXPushAppID != "" {
		current.WXPushAppID = replacement.WXPushAppID
	}
	if replacement.WXPushAppSecret != "" {
		current.WXPushAppSecret = replacement.WXPushAppSecret
	}
	if replacement.WXPushUserID != "" {
		current.WXPushUserID = replacement.WXPushUserID
	}
	if replacement.WXPushTemplateID != "" {
		current.WXPushTemplateID = replacement.WXPushTemplateID
	}
	if replacement.BarkServerURL != "" {
		current.BarkServerURL = replacement.BarkServerURL
	}
	if replacement.BarkDeviceKey != "" {
		current.BarkDeviceKey = replacement.BarkDeviceKey
	}
	current.BarkGroup = replacement.BarkGroup
	current.BarkSound = replacement.BarkSound
}

func validateChannel(input ChannelInput, secret channelSecret) error {
	if strings.TrimSpace(input.Name) == "" {
		return ErrInvalidChannel
	}
	switch input.Kind {
	case "telegram":
		if secret.TelegramBotToken == "" || secret.TelegramChatID == "" {
			return ErrInvalidChannel
		}
	case "pushplus":
		if secret.PushPlusToken == "" {
			return ErrInvalidChannel
		}
	case "wxpush":
		return validateWXPushSecret(secret)
	case "bark":
		return validateBarkSecret(secret)
	default:
		return ErrInvalidChannel
	}
	return nil
}

func channelDestination(kind string, secret channelSecret) string {
	switch kind {
	case "telegram":
		return "Chat " + maskValue(secret.TelegramChatID)
	case "pushplus":
		if secret.PushPlusTopic != "" {
			return "群组 " + safeText(secret.PushPlusTopic, 60)
		}
		return "一对一推送"
	case "wxpush":
		if isLegacyWXPush(secret) {
			return "旧版配置，请删除后重建"
		}
		if secret.WXPushUserID != "" {
			return "OpenID " + maskValue(secret.WXPushUserID)
		}
	case "bark":
		if secret.BarkDeviceKey != "" {
			return "设备 " + maskValue(secret.BarkDeviceKey)
		}
	}
	return "已配置"
}

func validateRule(rule Rule) error {
	if strings.TrimSpace(rule.Name) == "" || strings.TrimSpace(rule.ChannelPublicID) == "" {
		return ErrInvalidRule
	}
	if rule.StartMinute < -1 || rule.StartMinute > 1439 || rule.EndMinute < -1 || rule.EndMinute > 1439 {
		return ErrInvalidRule
	}
	if (rule.StartMinute == -1) != (rule.EndMinute == -1) {
		return ErrInvalidRule
	}
	allowed := map[string]bool{"important": true, "verification": true, "marketing": true, "spam": true, "normal": true, "uncertain": true}
	for _, category := range rule.Categories {
		if !allowed[category] {
			return ErrInvalidRule
		}
	}
	return nil
}

type encodedRuleLists struct{ accounts, groups, tags, categories, keywords string }

func encodeRuleLists(rule Rule) (encodedRuleLists, error) {
	encode := func(values []string) (string, error) {
		if values == nil {
			values = []string{}
		}
		data, err := json.Marshal(values)
		return string(data), err
	}
	var result encodedRuleLists
	var err error
	if result.accounts, err = encode(rule.AccountPublicIDs); err != nil {
		return result, err
	}
	if result.groups, err = encode(rule.GroupNames); err != nil {
		return result, err
	}
	if result.tags, err = encode(rule.TagNames); err != nil {
		return result, err
	}
	if result.categories, err = encode(rule.Categories); err != nil {
		return result, err
	}
	if result.keywords, err = encode(rule.SubjectKeywords); err != nil {
		return result, err
	}
	return result, nil
}

type ruleRows interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func scanRules(rows ruleRows) ([]Rule, error) {
	items := make([]Rule, 0)
	for rows.Next() {
		var item Rule
		var accountsJSON, groupsJSON, tagsJSON, categoriesJSON, keywordsJSON, created, updated string
		if err := rows.Scan(&item.PublicID, &item.ChannelPublicID, &item.ChannelName, &item.Name, &item.Enabled, &item.PersonalInbox, &item.PersonalOnly,
			&accountsJSON, &groupsJSON, &tagsJSON, &categoriesJSON, &item.SenderAddress, &item.SenderDomain,
			&keywordsJSON, &item.StartMinute, &item.EndMinute, &item.RequireOTP, &item.IncludeOTP,
			&created, &updated); err != nil {
			return nil, fmt.Errorf("scan notification rule: %w", err)
		}
		for _, condition := range []struct {
			data        string
			destination *[]string
		}{
			{accountsJSON, &item.AccountPublicIDs},
			{groupsJSON, &item.GroupNames},
			{tagsJSON, &item.TagNames},
			{categoriesJSON, &item.Categories},
			{keywordsJSON, &item.SubjectKeywords},
		} {
			if err := json.Unmarshal([]byte(condition.data), condition.destination); err != nil {
				return nil, fmt.Errorf("decode notification rule conditions: %w", err)
			}
		}
		var err error
		item.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, err
		}
		item.UpdatedAt, err = parseTime(updated)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) recordAudit(ctx context.Context, eventType, entityType, entityPublicID string, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	publicID, err := randomID("audit_", s.random)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO audit_events (public_id, event_type, actor_type, entity_type, entity_public_id, details_json, created_at_utc)
		VALUES (?, ?, 'admin', ?, ?, ?, ?)
	`, publicID, eventType, entityType, entityPublicID, string(encoded), formatTime(s.now().UTC()))
	return err
}

func randomID(prefix string, source io.Reader) (string, error) {
	value := make([]byte, 18)
	if _, err := io.ReadFull(source, value); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse notification time: %w", err)
	}
	return parsed.UTC(), nil
}

func parseNullableTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil, nil
	}
	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func notificationRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 5 {
		attempt = 5
	}
	return time.Duration(1<<(attempt-1)) * time.Minute
}

func notificationErrorCode(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if strings.HasSuffix(value, "_failed") {
		return value
	}
	if errors.Is(err, ErrWXPushReconfiguration) {
		return err.Error()
	}
	return "notification_delivery_failed"
}

func categoryLabel(value string) string {
	switch value {
	case "important":
		return "重要"
	case "verification":
		return "验证码"
	case "marketing":
		return "营销"
	case "spam":
		return "垃圾邮件"
	case "uncertain":
		return "待确认"
	default:
		return "普通"
	}
}

func safeText(value string, maximum int) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
	if maximum <= 0 {
		return ""
	}
	characters := []rune(value)
	if len(characters) <= maximum {
		return value
	}
	return string(characters[:maximum])
}

func notificationBodyPreview(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "（正文为空或未缓存）"
	}
	characters := []rune(value)
	if len(characters) <= 500 {
		return value
	}
	return string(characters[:500]) + "…"
}

func maskValue(value string) string {
	if len(value) <= 4 {
		return "****"
	}
	return strings.Repeat("*", len(value)-4) + value[len(value)-4:]
}
