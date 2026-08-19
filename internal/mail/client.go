package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"outlook-mail-manager/internal/accounts"
)

const (
	maxGraphResponseBytes = 20 << 20
	maxGraphBatchRequests = 20
	graphTextPreference   = `IdType="ImmutableId", outlook.body-content-type="text"`
	graphHTMLPreference   = `IdType="ImmutableId", outlook.body-content-type="html"`
	graphIDPreference     = `IdType="ImmutableId"`
)

type tokenProvider interface {
	Acquire(context.Context, int64, bool, *int64) (accounts.AccessTokenLease, error)
}

type graphClient struct {
	baseURL    *url.URL
	tokens     tokenProvider
	httpClient *http.Client
	now        func() time.Time
	sleep      func(context.Context, time.Duration) error
}

type GraphError struct {
	Status  int
	Code    string
	RetryAt *time.Time
}

type graphBatchRequest struct {
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    any               `json:"body,omitempty"`
}

type graphBatchResponse struct {
	ID      string            `json:"id"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

func (e *GraphError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("Microsoft Graph returned HTTP %d", e.Status)
	}
	return "Microsoft Graph request failed"
}

func newGraphClient(baseURL string, tokens tokenProvider, httpClient *http.Client, now func() time.Time) (*graphClient, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/") + "/")
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("Graph base URL must be absolute")
	}
	return &graphClient{
		baseURL: parsed, tokens: tokens, httpClient: httpClient, now: now,
		sleep: sleepContext,
	}, nil
}

func (c *graphClient) getJSON(ctx context.Context, accountID int64, target string, destination any) error {
	return c.getJSONWithPreference(ctx, accountID, target, graphTextPreference, destination)
}

func (c *graphClient) getJSONWithPreference(ctx context.Context, accountID int64, target, preference string, destination any) error {
	return c.requestJSON(ctx, accountID, http.MethodGet, target, preference, nil, destination)
}

func (c *graphClient) patchJSON(ctx context.Context, accountID int64, target string, value, destination any) error {
	return c.writeJSON(ctx, accountID, http.MethodPatch, target, value, destination)
}

func (c *graphClient) postJSON(ctx context.Context, accountID int64, target string, value, destination any) error {
	return c.writeJSON(ctx, accountID, http.MethodPost, target, value, destination)
}

func (c *graphClient) writeJSON(ctx context.Context, accountID int64, method, target string, value, destination any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode Microsoft Graph request: %w", err)
	}
	return c.requestJSON(ctx, accountID, method, target, graphIDPreference, body, destination)
}

func (c *graphClient) requestJSON(
	ctx context.Context,
	accountID int64,
	method string,
	target string,
	preference string,
	body []byte,
	destination any,
) error {
	requestURL, err := c.resolve(target)
	if err != nil {
		return err
	}
	lease, err := c.tokens.Acquire(ctx, accountID, false, nil)
	if err != nil {
		return err
	}
	authRetried := false
	for attempt := 0; attempt < 3; attempt++ {
		var requestBody io.Reader
		if body != nil {
			requestBody = bytes.NewReader(body)
		}
		request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), requestBody)
		if err != nil {
			return fmt.Errorf("create Graph request: %w", err)
		}
		request.Header.Set("Authorization", "Bearer "+lease.AccessToken)
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Prefer", preference)
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}

		response, requestErr := c.httpClient.Do(request)
		if requestErr != nil {
			if attempt < 2 {
				if err := c.sleep(ctx, retryDelay(attempt)); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("request Microsoft Graph: %w", requestErr)
		}
		if response.StatusCode == http.StatusUnauthorized && !authRetried {
			response.Body.Close()
			rejectedVersion := lease.Version
			lease, err = c.tokens.Acquire(ctx, accountID, true, &rejectedVersion)
			if err != nil {
				return err
			}
			authRetried = true
			attempt--
			continue
		}
		if response.StatusCode >= 500 && attempt < 2 {
			response.Body.Close()
			if err := c.sleep(ctx, retryDelay(attempt)); err != nil {
				return err
			}
			continue
		}
		if response.StatusCode < 200 || response.StatusCode > 299 {
			graphErr := decodeGraphError(response, c.now().UTC())
			response.Body.Close()
			return graphErr
		}
		if destination == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxGraphResponseBytes))
			response.Body.Close()
			return nil
		}
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, maxGraphResponseBytes)).Decode(destination)
		response.Body.Close()
		if decodeErr != nil {
			return fmt.Errorf("decode Microsoft Graph response: %w", decodeErr)
		}
		return nil
	}
	return errors.New("Microsoft Graph retry limit exceeded")
}

func (c *graphClient) batch(ctx context.Context, accountID int64, requests []graphBatchRequest) (map[string]graphBatchResponse, error) {
	if len(requests) == 0 || len(requests) > maxGraphBatchRequests {
		return nil, fmt.Errorf("Microsoft Graph batch must contain 1 to %d requests", maxGraphBatchRequests)
	}
	var response struct {
		Responses []graphBatchResponse `json:"responses"`
	}
	if err := c.postJSON(ctx, accountID, "$batch", map[string]any{"requests": requests}, &response); err != nil {
		return nil, err
	}
	items := make(map[string]graphBatchResponse, len(response.Responses))
	for _, item := range response.Responses {
		items[item.ID] = item
	}
	return items, nil
}

func (c *graphClient) batchResponseError(response graphBatchResponse) error {
	if response.Status >= 200 && response.Status <= 299 {
		return nil
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(response.Body, &body)
	result := &GraphError{Status: response.Status, Code: body.Error.Code}
	if response.Status == http.StatusTooManyRequests {
		for key, value := range response.Headers {
			if strings.EqualFold(key, "Retry-After") {
				if retryAt, ok := parseRetryAfter(value, c.now().UTC()); ok {
					result.RetryAt = &retryAt
				}
				break
			}
		}
	}
	return result
}

func (c *graphClient) markMessageRead(ctx context.Context, accountID int64, immutableID string) error {
	target := fmt.Sprintf("me/messages/%s", url.PathEscape(immutableID))
	return c.patchJSON(ctx, accountID, target, map[string]bool{"isRead": true}, nil)
}

func (c *graphClient) setMessageFlagged(ctx context.Context, accountID int64, immutableID string, flagged bool) error {
	status := "notFlagged"
	if flagged {
		status = "flagged"
	}
	target := fmt.Sprintf("me/messages/%s", url.PathEscape(immutableID))
	return c.patchJSON(ctx, accountID, target, map[string]any{"flag": map[string]string{"flagStatus": status}}, nil)
}

type graphMailFolder struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

func (c *graphClient) listMailFolders(ctx context.Context, accountID int64) ([]graphMailFolder, error) {
	query := url.Values{}
	query.Set("$select", "id,displayName")
	query.Set("$top", "100")
	var response struct {
		Value []graphMailFolder `json:"value"`
	}
	if err := c.getJSONWithPreference(ctx, accountID, "me/mailFolders?"+query.Encode(), graphIDPreference, &response); err != nil {
		return nil, err
	}
	return response.Value, nil
}

func (c *graphClient) createMailFolder(ctx context.Context, accountID int64, displayName string) (graphMailFolder, error) {
	var response graphMailFolder
	if err := c.postJSON(ctx, accountID, "me/mailFolders", map[string]string{"displayName": displayName}, &response); err != nil {
		return graphMailFolder{}, err
	}
	if response.ID == "" {
		return graphMailFolder{}, errors.New("Microsoft Graph created a folder without an ID")
	}
	return response, nil
}

func (c *graphClient) getMailFolder(ctx context.Context, accountID int64, folderID string) (graphMailFolder, error) {
	query := url.Values{}
	query.Set("$select", "id,displayName")
	var response graphMailFolder
	target := fmt.Sprintf("me/mailFolders/%s?%s", url.PathEscape(folderID), query.Encode())
	if err := c.getJSONWithPreference(ctx, accountID, target, graphIDPreference, &response); err != nil {
		return graphMailFolder{}, err
	}
	if response.ID == "" {
		return graphMailFolder{}, errors.New("Microsoft Graph returned a folder without an ID")
	}
	return response, nil
}

func (c *graphClient) moveMessage(ctx context.Context, accountID int64, immutableID, destinationID string) (string, error) {
	target := fmt.Sprintf("me/messages/%s/move", url.PathEscape(immutableID))
	var response struct {
		ID string `json:"id"`
	}
	if err := c.postJSON(ctx, accountID, target, map[string]string{"destinationId": destinationID}, &response); err != nil {
		return "", err
	}
	if response.ID == "" {
		return "", errors.New("Microsoft Graph moved a message without returning its ID")
	}
	return response.ID, nil
}

func (c *graphClient) getMessageForSync(ctx context.Context, accountID int64, immutableID string) (graphMessage, error) {
	query := url.Values{}
	query.Set("$select", syncMessageSelect)
	target := fmt.Sprintf("me/messages/%s?%s", url.PathEscape(immutableID), query.Encode())
	var response graphMessage
	if err := c.getJSON(ctx, accountID, target, &response); err != nil {
		return graphMessage{}, err
	}
	if response.ID == "" {
		return graphMessage{}, errors.New("Microsoft Graph returned a message without an immutable ID")
	}
	return response, nil
}

func (c *graphClient) getMessageHTML(ctx context.Context, accountID int64, immutableID string) (string, error) {
	query := url.Values{}
	query.Set("$select", "body")
	target := fmt.Sprintf("me/messages/%s?%s", url.PathEscape(immutableID), query.Encode())
	var response struct {
		Body struct {
			Content string `json:"content"`
		} `json:"body"`
	}
	if err := c.getJSONWithPreference(ctx, accountID, target, graphHTMLPreference, &response); err != nil {
		return "", err
	}
	return response.Body.Content, nil
}

type graphAttachment struct {
	ODataType   string `json:"@odata.type"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	Inline      bool   `json:"isInline"`
}

func (c *graphClient) listMessageAttachments(ctx context.Context, accountID int64, immutableID string) ([]graphAttachment, error) {
	query := url.Values{}
	query.Set("$select", "id,name,contentType,size,isInline")
	target := fmt.Sprintf("me/messages/%s/attachments?%s", url.PathEscape(immutableID), query.Encode())
	var response struct {
		Value []graphAttachment `json:"value"`
	}
	if err := c.getJSONWithPreference(ctx, accountID, target, graphIDPreference, &response); err != nil {
		return nil, err
	}
	return response.Value, nil
}

func (c *graphClient) openMessageAttachment(ctx context.Context, accountID int64, immutableID, attachmentID string) (*http.Response, error) {
	target := fmt.Sprintf("me/messages/%s/attachments/%s/$value", url.PathEscape(immutableID), url.PathEscape(attachmentID))
	requestURL, err := c.resolve(target)
	if err != nil {
		return nil, err
	}
	lease, err := c.tokens.Acquire(ctx, accountID, false, nil)
	if err != nil {
		return nil, err
	}
	authRetried := false
	for attempt := 0; attempt < 3; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("create Graph attachment request: %w", err)
		}
		request.Header.Set("Authorization", "Bearer "+lease.AccessToken)
		request.Header.Set("Prefer", graphIDPreference)
		response, requestErr := c.httpClient.Do(request)
		if requestErr != nil {
			if attempt < 2 {
				if err := c.sleep(ctx, retryDelay(attempt)); err != nil {
					return nil, err
				}
				continue
			}
			return nil, fmt.Errorf("download Microsoft Graph attachment: %w", requestErr)
		}
		if response.StatusCode == http.StatusUnauthorized && !authRetried {
			response.Body.Close()
			rejectedVersion := lease.Version
			lease, err = c.tokens.Acquire(ctx, accountID, true, &rejectedVersion)
			if err != nil {
				return nil, err
			}
			authRetried = true
			attempt--
			continue
		}
		if response.StatusCode >= 500 && attempt < 2 {
			response.Body.Close()
			if err := c.sleep(ctx, retryDelay(attempt)); err != nil {
				return nil, err
			}
			continue
		}
		if response.StatusCode < 200 || response.StatusCode > 299 {
			graphErr := decodeGraphError(response, c.now().UTC())
			response.Body.Close()
			return nil, graphErr
		}
		return response, nil
	}
	return nil, errors.New("Microsoft Graph attachment retry limit exceeded")
}

func (c *graphClient) resolve(target string) (*url.URL, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("parse Graph URL: %w", err)
	}
	if !parsed.IsAbs() {
		parsed = c.baseURL.ResolveReference(parsed)
	}
	if !strings.EqualFold(parsed.Scheme, c.baseURL.Scheme) || !strings.EqualFold(parsed.Host, c.baseURL.Host) {
		return nil, errors.New("Graph pagination URL changed origin")
	}
	return parsed, nil
}

func decodeGraphError(response *http.Response, now time.Time) *GraphError {
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&body)
	result := &GraphError{Status: response.StatusCode, Code: body.Error.Code}
	if response.StatusCode == http.StatusTooManyRequests {
		if retryAt, ok := parseRetryAfter(response.Header.Get("Retry-After"), now); ok {
			result.RetryAt = &retryAt
		}
	}
	return result
}

func parseRetryAfter(value string, now time.Time) (time.Time, bool) {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		return now.Add(time.Duration(seconds) * time.Second), true
	}
	parsed, err := http.ParseTime(value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func retryDelay(attempt int) time.Duration {
	return time.Duration(1<<attempt) * 250 * time.Millisecond
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
