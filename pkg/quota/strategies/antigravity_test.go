package strategies

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hangox/cliproxy-quota-plugin/pkg/quota"
)

func TestAntigravityStrategy(t *testing.T) {
	expectedReset := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:retrieveUserQuotaSummary" {
			t.Errorf("unexpected path: %q", r.URL.Path)
		}
		if r.Header.Get("User-Agent") != antigravityQuotaSummaryUserAgent {
			t.Errorf("User-Agent = %q, want %q", r.Header.Get("User-Agent"), antigravityQuotaSummaryUserAgent)
		}
		if r.Header.Get("Authorization") != "Bearer test-ag-token" {
			t.Errorf("Authorization = %q, want %q", r.Header.Get("Authorization"), "Bearer test-ag-token")
		}

		var reqBody map[string]any
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		if reqBody["project"] != "test-proj-456" {
			t.Errorf("project = %v, want test-proj-456", reqBody["project"])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"groups": [
				{
					"name": "Gemini Models",
					"label": "Gemini Models",
					"buckets": [
						{
							"name": "gemini-5h",
							"label": "5 hour",
							"remainingFraction": 0.85,
							"resetTime": "2026-09-23T12:00:00Z"
						},
						{
							"name": "gemini-7d",
							"label": "weekly",
							"remainingFraction": "0.65"
						}
					]
				},
				{
					"name": "Claude and GPT models",
					"label": "Claude and GPT models",
					"buckets": [
						{
							"name": "claude-5h",
							"label": "5h",
							"remainingFraction": 0.90
						},
						{
							"name": "claude-7d",
							"label": "7 day",
							"remainingFraction": 0.45
						}
					]
				}
			]
		}`))
	}))
	defer server.Close()

	strat := NewAntigravityStrategy(nil, server.URL+"/v1internal:retrieveUserQuotaSummary")

	account := &quota.AuthAccount{
		ID:       "ag-acc-1",
		Provider: "antigravity",
		Weight:   2,
		Attributes: map[string]string{
			"access_token": "test-ag-token",
			"project_id":   "test-proj-456",
		},
	}

	data, err := strat.FetchAccountQuota(context.Background(), account)
	if err != nil {
		t.Fatalf("FetchAccountQuota error: %v", err)
	}

	if data.AuthID != "ag-acc-1" {
		t.Errorf("AuthID = %q, want ag-acc-1", data.AuthID)
	}
	if data.Weight != 2 {
		t.Errorf("Weight = %d, want 2", data.Weight)
	}

	// 验证 gemini 分组
	geminiBuckets, ok := data.Groups["gemini"]
	if !ok || len(geminiBuckets) != 2 {
		t.Fatalf("gemini buckets = %+v, want 2", geminiBuckets)
	}
	if geminiBuckets[0].Kind != "5h" || geminiBuckets[0].RemainingPercent != 85.0 {
		t.Errorf("gemini 5h = %+v, want 5h 85%%", geminiBuckets[0])
	}
	if !geminiBuckets[0].ResetAt.Equal(expectedReset) {
		t.Errorf("gemini 5h resetAt = %v, want %v", geminiBuckets[0].ResetAt, expectedReset)
	}
	if geminiBuckets[1].Kind != "7d" || geminiBuckets[1].RemainingPercent != 65.0 {
		t.Errorf("gemini 7d = %+v, want 7d 65%%", geminiBuckets[1])
	}

	// 验证 claude_gpt 分组
	claudeBuckets, ok := data.Groups["claude_gpt"]
	if !ok || len(claudeBuckets) != 2 {
		t.Fatalf("claude_gpt buckets = %+v, want 2", claudeBuckets)
	}
	if claudeBuckets[0].Kind != "5h" || claudeBuckets[0].RemainingPercent != 90.0 {
		t.Errorf("claude 5h = %+v, want 5h 90%%", claudeBuckets[0])
	}
	if claudeBuckets[1].Kind != "7d" || claudeBuckets[1].RemainingPercent != 45.0 {
		t.Errorf("claude 7d = %+v, want 7d 45%%", claudeBuckets[1])
	}
}

func TestAntigravityStrategyMissingToken(t *testing.T) {
	strat := NewAntigravityStrategy(nil)
	account := &quota.AuthAccount{
		ID:       "ag-acc-empty",
		Provider: "antigravity",
	}
	_, err := strat.FetchAccountQuota(context.Background(), account)
	if err == nil {
		t.Fatal("expected error for missing token, got nil")
	}
}

func TestAntigravityStrategyProxyClientCaching(t *testing.T) {
	strat := NewAntigravityStrategy(nil)
	strat.SetDefaultProxyURL("")

	// 1. 无代理时返回默认 httpClient
	cEmpty, errEmpty := strat.getHTTPClient(nil)
	if errEmpty != nil {
		t.Fatalf("getHTTPClient empty error: %v", errEmpty)
	}
	if cEmpty != strat.httpClient {
		t.Errorf("expected default httpClient")
	}

	// 2. 账号指定 proxy_url 并且被缓存复用
	acc1 := &quota.AuthAccount{
		ID:         "ag-proxy-1",
		Attributes: map[string]string{"proxy_url": "http://127.0.0.1:9090"},
	}
	acc2 := &quota.AuthAccount{
		ID:         "ag-proxy-2",
		Attributes: map[string]string{"proxy": "http://127.0.0.1:9090"},
	}
	c1, err1 := strat.getHTTPClient(acc1)
	if err1 != nil {
		t.Fatalf("getHTTPClient 1 error: %v", err1)
	}
	c2, err2 := strat.getHTTPClient(acc2)
	if err2 != nil {
		t.Fatalf("getHTTPClient 2 error: %v", err2)
	}
	if c1 != c2 {
		t.Errorf("expected cached client to be reused, got %p vs %p", c1, c2)
	}

	// 3. 全局默认代理兜底
	strat.SetDefaultProxyURL("http://127.0.0.1:9091")
	cDef, errDef := strat.getHTTPClient(&quota.AuthAccount{ID: "no-proxy"})
	if errDef != nil {
		t.Fatalf("getHTTPClient def error: %v", errDef)
	}
	if cDef == strat.httpClient {
		t.Errorf("expected custom client from default proxy")
	}
}

