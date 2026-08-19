package mail

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"outlook-mail-manager/internal/accounts"
)

type tokenCall struct {
	force    bool
	rejected *int64
}

type fakeTokenProvider struct {
	mu     sync.Mutex
	calls  []tokenCall
	leases []accounts.AccessTokenLease
}

func (p *fakeTokenProvider) Acquire(_ context.Context, _ int64, force bool, rejected *int64) (accounts.AccessTokenLease, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var rejectedCopy *int64
	if rejected != nil {
		value := *rejected
		rejectedCopy = &value
	}
	p.calls = append(p.calls, tokenCall{force: force, rejected: rejectedCopy})
	index := len(p.calls) - 1
	if index >= len(p.leases) {
		index = len(p.leases) - 1
	}
	return p.leases[index], nil
}

func TestGraphClientRetriesUnauthorizedOnce(t *testing.T) {
	provider := &fakeTokenProvider{leases: []accounts.AccessTokenLease{
		{AccessToken: "old-token", Version: 7},
		{AccessToken: "new-token", Version: 8},
	}}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.Header.Get("Prefer"); got != `IdType="ImmutableId", outlook.body-content-type="text"` {
			t.Fatalf("Prefer header = %q", got)
		}
		if requests == 1 {
			if got := r.Header.Get("Authorization"); got != "Bearer old-token" {
				t.Fatalf("first Authorization = %q", got)
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer new-token" {
			t.Fatalf("second Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{}})
	}))
	defer server.Close()

	client, err := newGraphClient(server.URL, provider, server.Client(), time.Now)
	if err != nil {
		t.Fatalf("newGraphClient() error = %v", err)
	}
	var response map[string]any
	if err := client.getJSON(context.Background(), 42, "/messages", &response); err != nil {
		t.Fatalf("getJSON() error = %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if len(provider.calls) != 2 || provider.calls[1].rejected == nil || *provider.calls[1].rejected != 7 {
		t.Fatalf("token calls = %#v", provider.calls)
	}
}

func TestGraphClientDoesNotReplaySecondUnauthorized(t *testing.T) {
	provider := &fakeTokenProvider{leases: []accounts.AccessTokenLease{
		{AccessToken: "old-token", Version: 1},
		{AccessToken: "new-token", Version: 2},
	}}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client, err := newGraphClient(server.URL, provider, server.Client(), time.Now)
	if err != nil {
		t.Fatalf("newGraphClient() error = %v", err)
	}
	err = client.getJSON(context.Background(), 1, "/messages", &map[string]any{})
	var graphErr *GraphError
	if !errorsAs(err, &graphErr) || graphErr.Status != http.StatusUnauthorized {
		t.Fatalf("getJSON() error = %v, want HTTP 401", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestGraphClientPreservesRetryAfter(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	provider := &fakeTokenProvider{leases: []accounts.AccessTokenLease{{AccessToken: "token", Version: 1}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"TooManyRequests"}}`))
	}))
	defer server.Close()

	client, err := newGraphClient(server.URL, provider, server.Client(), func() time.Time { return now })
	if err != nil {
		t.Fatalf("newGraphClient() error = %v", err)
	}
	err = client.getJSON(context.Background(), 1, "/messages", &map[string]any{})
	var graphErr *GraphError
	if !errorsAs(err, &graphErr) {
		t.Fatalf("getJSON() error = %v, want GraphError", err)
	}
	if graphErr.RetryAt == nil || !graphErr.RetryAt.Equal(now.Add(17*time.Second)) {
		t.Fatalf("RetryAt = %v", graphErr.RetryAt)
	}
}

func TestGraphClientRequestsHTMLMessageWithImmutableID(t *testing.T) {
	provider := &fakeTokenProvider{leases: []accounts.AccessTokenLease{{AccessToken: "token", Version: 1}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.EscapedPath(); got != "/me/messages/AAMk%2Fimmutable+id" {
			t.Fatalf("escaped path = %q", got)
		}
		if got := r.URL.Query().Get("$select"); got != "body" {
			t.Fatalf("$select = %q", got)
		}
		if got := r.Header.Get("Prefer"); got != `IdType="ImmutableId", outlook.body-content-type="html"` {
			t.Fatalf("Prefer header = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"body": map[string]string{"contentType": "html", "content": "<p>Hello</p>"},
		})
	}))
	defer server.Close()

	client, err := newGraphClient(server.URL, provider, server.Client(), time.Now)
	if err != nil {
		t.Fatalf("newGraphClient() error = %v", err)
	}
	body, err := client.getMessageHTML(context.Background(), 42, "AAMk/immutable+id")
	if err != nil {
		t.Fatalf("getMessageHTML() error = %v", err)
	}
	if body != "<p>Hello</p>" {
		t.Fatalf("body = %q", body)
	}
}

func TestGraphClientMarksMessageReadWithImmutableID(t *testing.T) {
	provider := &fakeTokenProvider{leases: []accounts.AccessTokenLease{{AccessToken: "token", Version: 1}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Fatalf("method = %q, want PATCH", r.Method)
		}
		if got := r.URL.EscapedPath(); got != "/me/messages/AAMk%2Fimmutable+id" {
			t.Fatalf("escaped path = %q", got)
		}
		if got := r.Header.Get("Prefer"); got != `IdType="ImmutableId"` {
			t.Fatalf("Prefer header = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q", got)
		}
		var body map[string]bool
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if !body["isRead"] {
			t.Fatalf("request body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "AAMk/immutable+id", "isRead": true})
	}))
	defer server.Close()

	client, err := newGraphClient(server.URL, provider, server.Client(), time.Now)
	if err != nil {
		t.Fatalf("newGraphClient() error = %v", err)
	}
	if err := client.markMessageRead(context.Background(), 42, "AAMk/immutable+id"); err != nil {
		t.Fatalf("markMessageRead() error = %v", err)
	}
}

func TestGraphClientBatchReturnsPerRequestResults(t *testing.T) {
	now := time.Date(2026, 8, 19, 1, 0, 0, 0, time.UTC)
	provider := &fakeTokenProvider{leases: []accounts.AccessTokenLease{{AccessToken: "token", Version: 1}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/$batch" {
			t.Fatalf("request = %s %s", r.Method, r.URL.EscapedPath())
		}
		var body struct {
			Requests []graphBatchRequest `json:"requests"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode batch request: %v", err)
		}
		if len(body.Requests) != 2 || body.Requests[0].Method != http.MethodPatch || body.Requests[0].URL != "/me/messages/one" {
			t.Fatalf("batch requests = %#v", body.Requests)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"responses": []map[string]any{
			{"id": "0", "status": 200, "headers": map[string]string{}, "body": map[string]any{"isRead": true}},
			{"id": "1", "status": 429, "headers": map[string]string{"Retry-After": "9"}, "body": map[string]any{"error": map[string]string{"code": "TooManyRequests"}}},
		}})
	}))
	defer server.Close()

	client, err := newGraphClient(server.URL, provider, server.Client(), func() time.Time { return now })
	if err != nil {
		t.Fatalf("newGraphClient() error = %v", err)
	}
	responses, err := client.batch(context.Background(), 42, []graphBatchRequest{
		{ID: "0", Method: http.MethodPatch, URL: "/me/messages/one", Body: map[string]bool{"isRead": true}},
		{ID: "1", Method: http.MethodPatch, URL: "/me/messages/two", Body: map[string]bool{"isRead": true}},
	})
	if err != nil {
		t.Fatalf("batch() error = %v", err)
	}
	if err := client.batchResponseError(responses["0"]); err != nil {
		t.Fatalf("successful batch response error = %v", err)
	}
	var graphErr *GraphError
	if err := client.batchResponseError(responses["1"]); !errorsAs(err, &graphErr) || graphErr.Code != "TooManyRequests" || graphErr.RetryAt == nil || !graphErr.RetryAt.Equal(now.Add(9*time.Second)) {
		t.Fatalf("throttled batch response error = %#v", err)
	}
}

func TestGraphClientMovesMessageWithoutPermanentDelete(t *testing.T) {
	provider := &fakeTokenProvider{leases: []accounts.AccessTokenLease{{AccessToken: "token", Version: 1}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/me/messages/AAMk%2Fid/move" {
			t.Fatalf("request = %s %s", r.Method, r.URL.EscapedPath())
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode move request: %v", err)
		}
		if body["destinationId"] != "deleteditems" {
			t.Fatalf("destinationId = %q", body["destinationId"])
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "AAMk/id"})
	}))
	defer server.Close()

	client, err := newGraphClient(server.URL, provider, server.Client(), time.Now)
	if err != nil {
		t.Fatalf("newGraphClient() error = %v", err)
	}
	if _, err := client.moveMessage(context.Background(), 42, "AAMk/id", "deleteditems"); err != nil {
		t.Fatalf("moveMessage() error = %v", err)
	}
}

func TestGraphClientListsAndStreamsAttachments(t *testing.T) {
	provider := &fakeTokenProvider{leases: []accounts.AccessTokenLease{{AccessToken: "token", Version: 1}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/me/messages/AAMk%2Fid/attachments":
			if got := r.URL.Query().Get("$select"); got != "id,name,contentType,size,isInline" {
				t.Fatalf("$select = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []map[string]any{{
				"@odata.type": "#microsoft.graph.fileAttachment", "id": "att/id", "name": "report.pdf",
				"contentType": "application/pdf", "size": 7, "isInline": false,
			}}})
		case "/me/messages/AAMk%2Fid/attachments/att%2Fid/$value":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("payload"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := newGraphClient(server.URL, provider, server.Client(), time.Now)
	if err != nil {
		t.Fatalf("newGraphClient() error = %v", err)
	}
	items, err := client.listMessageAttachments(context.Background(), 42, "AAMk/id")
	if err != nil || len(items) != 1 || items[0].ID != "att/id" || items[0].Name != "report.pdf" {
		t.Fatalf("listMessageAttachments() = %#v, %v", items, err)
	}
	response, err := client.openMessageAttachment(context.Background(), 42, "AAMk/id", "att/id")
	if err != nil {
		t.Fatalf("openMessageAttachment() error = %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if string(body) != "payload" || response.Header.Get("Content-Type") != "application/pdf" {
		t.Fatalf("attachment response = %q, %q", body, response.Header.Get("Content-Type"))
	}
}

func errorsAs(err error, target any) bool {
	return errors.As(err, target)
}
