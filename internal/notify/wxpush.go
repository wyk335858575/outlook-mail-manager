package notify

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/url"
	"strings"
	"time"
)

const weChatAPIBaseURL = "https://api.weixin.qq.com"

var (
	ErrWXPushCredentials     = errors.New("wxpush_credentials_failed")
	ErrWXPushUser            = errors.New("wxpush_user_failed")
	ErrWXPushTemplate        = errors.New("wxpush_template_failed")
	ErrWXPushNetwork         = errors.New("wxpush_network_failed")
	ErrWXPushAPI             = errors.New("wxpush_api_failed")
	ErrWXPushReconfiguration = errors.New("wxpush_reconfiguration_required")
)

type wxPushToken struct {
	value     string
	expiresAt time.Time
}

type wxPushAPIResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

func validateWXPushSecret(secret channelSecret) error {
	if secret.WXPushAppID == "" || secret.WXPushAppSecret == "" || secret.WXPushUserID == "" || secret.WXPushTemplateID == "" {
		return ErrInvalidChannel
	}
	if strings.ContainsAny(secret.WXPushUserID, "|,;\t\r\n ") {
		return ErrInvalidChannel
	}
	return nil
}

func isLegacyWXPush(secret channelSecret) bool {
	return (secret.WXPushURL != "" || secret.WXPushToken != "") &&
		(secret.WXPushAppID == "" || secret.WXPushAppSecret == "" || secret.WXPushUserID == "" || secret.WXPushTemplateID == "")
}

func (s *Service) sendWXPush(ctx context.Context, payload deliveryPayload, secret channelSecret) error {
	if isLegacyWXPush(secret) {
		return ErrWXPushReconfiguration
	}
	if err := validateWXPushSecret(secret); err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		token, err := s.wxPushAccessToken(ctx, secret)
		if err != nil {
			return err
		}
		response, err := s.sendWXPushTemplate(ctx, token, payload, secret)
		if err != nil {
			return err
		}
		if response.ErrCode == 0 {
			return nil
		}
		if !isWXPushTokenError(response.ErrCode) || attempt == 1 {
			return wxPushSendError(response.ErrCode)
		}
		s.clearWXPushToken(secret, token)
	}
	return ErrWXPushAPI
}

func (s *Service) wxPushAccessToken(ctx context.Context, secret channelSecret) (string, error) {
	key := wxPushTokenKey(secret)
	now := s.wxPushNow()
	s.wxPushTokenMu.Lock()
	defer s.wxPushTokenMu.Unlock()
	if cached, ok := s.wxPushTokens[key]; ok && now.Before(cached.expiresAt) {
		return cached.value, nil
	}
	var response struct {
		wxPushAPIResponse
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	err := s.doJSON(ctx, s.wxPushAPIBase()+"/cgi-bin/stable_token", map[string]any{
		"grant_type":    "client_credential",
		"appid":         secret.WXPushAppID,
		"secret":        secret.WXPushAppSecret,
		"force_refresh": false,
	}, &response, nil)
	if err != nil {
		return "", ErrWXPushNetwork
	}
	if response.ErrCode != 0 || response.AccessToken == "" || response.ExpiresIn <= 0 {
		return "", ErrWXPushCredentials
	}
	if s.wxPushTokens == nil {
		s.wxPushTokens = make(map[[32]byte]wxPushToken)
	}
	expiresAt := now.Add(time.Duration(response.ExpiresIn)*time.Second - 5*time.Minute)
	s.wxPushTokens[key] = wxPushToken{value: response.AccessToken, expiresAt: expiresAt}
	return response.AccessToken, nil
}

func (s *Service) sendWXPushTemplate(ctx context.Context, token string, payload deliveryPayload, secret channelSecret) (wxPushAPIResponse, error) {
	sender := payload.Sender
	if sender == "" {
		sender = "邮箱管理台"
	}
	subject := payload.Subject
	if subject == "" {
		subject = payload.Title
	}
	body := payload.Body
	if body == "" {
		body = payload.Text
	}
	var response wxPushAPIResponse
	err := s.doJSON(ctx, s.wxPushAPIBase()+"/cgi-bin/message/template/send?access_token="+url.QueryEscape(token), map[string]any{
		"touser":      secret.WXPushUserID,
		"template_id": secret.WXPushTemplateID,
		"data": map[string]any{
			"title":   map[string]string{"value": payload.Title},
			"content": map[string]string{"value": payload.Text},
			"sender":  map[string]string{"value": sender},
			"subject": map[string]string{"value": subject},
			"body":    map[string]string{"value": body},
		},
	}, &response, nil)
	if err != nil {
		return wxPushAPIResponse{}, ErrWXPushNetwork
	}
	return response, nil
}

func (s *Service) clearWXPushToken(secret channelSecret, invalidToken string) {
	s.wxPushTokenMu.Lock()
	key := wxPushTokenKey(secret)
	if cached, ok := s.wxPushTokens[key]; ok && cached.value == invalidToken {
		delete(s.wxPushTokens, key)
	}
	s.wxPushTokenMu.Unlock()
}

func (s *Service) wxPushAPIBase() string {
	if s.wxPushBaseURL == "" {
		return weChatAPIBaseURL
	}
	return strings.TrimRight(s.wxPushBaseURL, "/")
}

func (s *Service) wxPushNow() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

func wxPushTokenKey(secret channelSecret) [32]byte {
	return sha256.Sum256([]byte(secret.WXPushAppID + "\x00" + secret.WXPushAppSecret))
}

func isWXPushTokenError(code int) bool {
	switch code {
	case 40001, 40014, 42001:
		return true
	default:
		return false
	}
}

func wxPushSendError(code int) error {
	switch code {
	case 40003, 43004, 43101:
		return ErrWXPushUser
	case 40037, 47003:
		return ErrWXPushTemplate
	default:
		return ErrWXPushAPI
	}
}

