package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

type microsoftTokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	ExpiresIn        int64  `json:"expires_in"`
	ErrorCode        string `json:"error"`
	ErrorDescription string `json:"error_description"`
	ErrorURI         string `json:"error_uri"`
}

const graphDefaultScope = "https://graph.microsoft.com/.default"

func refreshMicrosoftToken(
	ctx context.Context,
	client *http.Client,
	endpoint oauth2.Endpoint,
	clientID string,
	refreshToken string,
	now func() time.Time,
) (*oauth2.Token, error) {
	return requestMicrosoftToken(ctx, client, endpoint, clientID, refreshToken, graphDefaultScope, now)
}

func requestMicrosoftToken(
	ctx context.Context,
	client *http.Client,
	endpoint oauth2.Endpoint,
	clientID string,
	refreshToken string,
	scope string,
	now func() time.Time,
) (*oauth2.Token, error) {
	form := url.Values{
		"client_id":     {clientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"scope":         {scope},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var payload microsoftTokenResponse
	decodeErr := json.Unmarshal(body, &payload)
	if response.StatusCode < 200 || response.StatusCode > 299 || payload.ErrorCode != "" {
		return nil, &oauth2.RetrieveError{
			Response: response, Body: body, ErrorCode: payload.ErrorCode,
			ErrorDescription: payload.ErrorDescription, ErrorURI: payload.ErrorURI,
		}
	}
	if decodeErr != nil {
		return nil, decodeErr
	}
	if payload.AccessToken == "" || payload.ExpiresIn <= 0 {
		return nil, errors.New("incomplete token")
	}
	if payload.RefreshToken == "" {
		payload.RefreshToken = refreshToken
	}
	if strings.TrimSpace(payload.Scope) == "" {
		payload.Scope = scope
	}
	token := (&oauth2.Token{
		AccessToken: payload.AccessToken, RefreshToken: payload.RefreshToken,
		TokenType: payload.TokenType, Expiry: now().UTC().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}).WithExtra(map[string]any{"scope": payload.Scope})
	return token, nil
}
