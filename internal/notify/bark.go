package notify

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

var (
	ErrBarkNetwork = errors.New("bark_network_failed")
	ErrBarkAPI     = errors.New("bark_api_failed")
)

func validateBarkSecret(secret channelSecret) error {
	if secret.BarkServerURL == "" || secret.BarkDeviceKey == "" {
		return ErrInvalidChannel
	}
	parsed, err := url.Parse(secret.BarkServerURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ErrInvalidChannel
	}
	if strings.ContainsAny(secret.BarkDeviceKey, "\t\r\n ") {
		return ErrInvalidChannel
	}
	return nil
}

func (s *Service) sendBark(ctx context.Context, payload deliveryPayload, secret channelSecret) error {
	if err := validateBarkSecret(secret); err != nil {
		return err
	}
	target := strings.TrimRight(secret.BarkServerURL, "/") + "/push"
	body := map[string]string{
		"device_key": secret.BarkDeviceKey,
		"title":      payload.Title,
		"body":       payload.Text,
	}
	if secret.BarkGroup != "" {
		body["group"] = secret.BarkGroup
	}
	if secret.BarkSound != "" {
		body["sound"] = secret.BarkSound
	}
	var response struct {
		Code int `json:"code"`
	}
	if err := s.doJSON(ctx, target, body, &response, nil); err != nil {
		var httpErr notificationHTTPError
		if errors.As(err, &httpErr) {
			return ErrBarkAPI
		}
		return ErrBarkNetwork
	}
	if response.Code != 200 {
		return ErrBarkAPI
	}
	return nil
}
