package strategies

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hangox/cliproxy-quota-plugin/pkg/quota"
)

func TestCodexStrategy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			t.Errorf("path = %q, want /backend-api/wham/usage", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-codex-key" {
			t.Errorf("auth = %q, want Bearer test-codex-key", r.Header.Get("Authorization"))
		}
		if r.Header.Get("ChatGPT-Account-Id") != "acct-123" {
			t.Errorf("acct = %q, want acct-123", r.Header.Get("ChatGPT-Account-Id"))
		}
		if r.Header.Get("User-Agent") != "codex-cli" {
			t.Errorf("User-Agent = %q, want codex-cli", r.Header.Get("User-Agent"))
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"rate_limit": {
				"primary_window": {
					"used_percent": 24.5,
					"limit_window_seconds": 604800,
					"reset_at": 1727000000
				},
				"secondary_window": {
					"used_percent": 10.0,
					"limit_window_seconds": 18000,
					"resets_at": "2026-09-23T14:30:00Z"
				}
			},
			"rate_limit_reset_credits": {
				"available_count": 4
			}
		}`))
	}))
	defer server.Close()

	strat := NewCodexStrategy(nil)
	strat.SetBaseURL(server.URL)

	account := &quota.AuthAccount{
		ID:       "codex-1",
		Provider: "codex",
		Weight:   3,
		Attributes: map[string]string{
			"api_key":    "test-codex-key",
			"account_id": "acct-123",
		},
	}

	data, err := strat.FetchAccountQuota(context.Background(), account)
	if err != nil {
		t.Fatalf("FetchAccountQuota error: %v", err)
	}

	if data.ResetCards != 4 {
		t.Errorf("ResetCards = %d, want 4", data.ResetCards)
	}
	if len(data.Buckets) != 2 {
		t.Fatalf("len(Buckets) = %d, want 2", len(data.Buckets))
	}

	// primary_window: 7d, remaining = 100 - 24.5 = 75.5
	b0 := data.Buckets[0]
	if b0.Kind != "7d" || b0.RemainingPercent != 75.5 {
		t.Errorf("bucket[0] = %+v, want 7d 75.5%%", b0)
	}
	if b0.ResetAt.Unix() != 1727000000 {
		t.Errorf("bucket[0] resetAt unix = %d, want 1727000000", b0.ResetAt.Unix())
	}

	// secondary_window: 5h, remaining = 100 - 10.0 = 90.0
	b1 := data.Buckets[1]
	if b1.Kind != "5h" || b1.RemainingPercent != 90.0 {
		t.Errorf("bucket[1] = %+v, want 5h 90%%", b1)
	}
	expectedB1Reset, _ := time.Parse(time.RFC3339, "2026-09-23T14:30:00Z")
	if !b1.ResetAt.Equal(expectedB1Reset) {
		t.Errorf("bucket[1] resetAt = %v, want %v", b1.ResetAt, expectedB1Reset)
	}
}

func TestCodexStrategyMissingToken(t *testing.T) {
	strat := NewCodexStrategy(nil)
	account := &quota.AuthAccount{
		ID:       "codex-empty",
		Provider: "codex",
	}
	_, err := strat.FetchAccountQuota(context.Background(), account)
	if err == nil {
		t.Fatal("expected error for missing token, got nil")
	}
}

func TestCodexStrategyProxyCaching(t *testing.T) {
	strat := NewCodexStrategy(nil)
	client1, err1 := strat.getHTTPClient("http://127.0.0.1:8080")
	if err1 != nil {
		t.Fatalf("getHTTPClient 1 error: %v", err1)
	}
	client2, err2 := strat.getHTTPClient("http://127.0.0.1:8080")
	if err2 != nil {
		t.Fatalf("getHTTPClient 2 error: %v", err2)
	}
	if client1 != client2 {
		t.Errorf("expected cached client instance to be reused, got %p vs %p", client1, client2)
	}

	emptyClient, errEmpty := strat.getHTTPClient("")
	if errEmpty != nil {
		t.Fatalf("getHTTPClient empty error: %v", errEmpty)
	}
	if emptyClient != strat.httpClient {
		t.Errorf("expected default httpClient for empty proxy")
	}
}

