package strategies

import (
	"context"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hangox/cliproxy-quota-plugin/pkg/quota"
)

const kimiUsageResponse = `{
  "data": {
    "limits": [{
      "window": 300,
      "limit": 1000,
      "used": 250,
      "remaining": 750,
      "resetTime": "2026-10-02T15:00:00Z"
    }],
    "usages": {
      "limit_month_total": {
        "used_ratio": 0.0011,
        "reset_time": "2026-11-01T00:00:00Z"
      }
    }
  }
}`

func TestKimiStrategyProvider(t *testing.T) {
	if got := NewKimiStrategy(nil).Provider(); got != "kimi" {
		t.Fatalf("Provider() = %q, want kimi", got)
	}
}

func TestKimiStrategyEndpointsAndQuotaWindows(t *testing.T) {
	clearStrategyProxyEnvironment(t)
	tests := []struct {
		name     string
		account  *quota.AuthAccount
		wantURL  string
		wantAuth string
	}{
		{
			name: "domestic access token",
			account: &quota.AuthAccount{ID: "kimi-cn", Provider: "kimi", Attributes: map[string]string{
				"access_token": "cn-access", "domain": "api.kimi.com",
			}},
			wantURL: kimiQuotaURL, wantAuth: "Bearer cn-access",
		},
		{
			name: "international api key",
			account: &quota.AuthAccount{ID: "kimi-ai", Provider: "kimi", Attributes: map[string]string{
				"api_key": "ai-key", "domain": "api.kimi.ai",
			}},
			wantURL: kimiAIQuotaURL, wantAuth: "Bearer ai-key",
		},
		{
			name: "international name and metadata token",
			account: &quota.AuthAccount{ID: "kimi-name", Provider: "kimi", Name: "account.json", Metadata: map[string]any{
				"name": "kimi-ai-account", "token": "metadata-token",
			}},
			wantURL: kimiAIQuotaURL, wantAuth: "Bearer metadata-token",
		},
		{
			name: "explicit domestic domain overrides alias",
			account: &quota.AuthAccount{ID: "kimi-domain", Provider: "kimi-ai", Attributes: map[string]string{
				"token": "domain-token", "domain": "api.kimi.com",
			}},
			wantURL: kimiQuotaURL, wantAuth: "Bearer domain-token",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &kimiTestTransport{statusCode: http.StatusOK, body: kimiUsageResponse}
			strategy := NewKimiStrategy(&http.Client{Transport: transport})
			data, err := strategy.FetchAccountQuota(context.Background(), test.account)
			if err != nil {
				t.Fatalf("FetchAccountQuota() error: %v", err)
			}
			if len(transport.requests) != 1 {
				t.Fatalf("requests = %d, want 1", len(transport.requests))
			}
			request := transport.requests[0]
			if got := request.URL.String(); got != test.wantURL {
				t.Errorf("request URL = %q, want %q", got, test.wantURL)
			}
			if got := request.Header.Get("Authorization"); got != test.wantAuth {
				t.Errorf("Authorization = %q, want %q", got, test.wantAuth)
			}
			if request.Header.Get("Accept") != "application/json" {
				t.Errorf("Accept = %q, want application/json", request.Header.Get("Accept"))
			}
			if data.AuthID != test.account.ID || len(data.Buckets) != 2 {
				t.Fatalf("quota data = %+v, want two buckets", data)
			}
			fiveHour := data.Buckets[0]
			if fiveHour.Kind != "5h" || fiveHour.RemainingPercent != 75 {
				t.Errorf("5h bucket = %+v, want 75%% remaining", fiveHour)
			}
			if want := time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC); !fiveHour.ResetAt.Equal(want) {
				t.Errorf("5h reset = %v, want %v", fiveHour.ResetAt, want)
			}
			monthly := data.Buckets[1]
			if monthly.Kind != "monthly" || math.Abs(monthly.RemainingPercent-99.89) > 0.0001 {
				t.Errorf("monthly bucket = %+v, want 99.89%% remaining", monthly)
			}
			if want := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC); !monthly.ResetAt.Equal(want) {
				t.Errorf("monthly reset = %v, want %v", monthly.ResetAt, want)
			}
		})
	}
}

func TestKimiStrategyFiveHourFallback(t *testing.T) {
	body := `{"usages":{"limit_month_total":{"used_ratio":0.25,"reset_time":"2026-11-01T00:00:00Z"}},"limit_5h":{"used_ratio":0.4,"reset_time":"2026-10-02T20:00:00Z"}}`
	buckets, err := parseKimiBuckets([]byte(body))
	if err != nil {
		t.Fatalf("parseKimiBuckets() error: %v", err)
	}
	if len(buckets) != 2 || buckets[0].Kind != "5h" || buckets[0].RemainingPercent != 60 {
		t.Fatalf("buckets = %+v, want 5h fallback and monthly", buckets)
	}
	if buckets[1].Kind != "monthly" || buckets[1].RemainingPercent != 75 {
		t.Fatalf("monthly bucket = %+v, want 75%% remaining", buckets[1])
	}
}

func TestKimiStrategyErrors(t *testing.T) {
	clearStrategyProxyEnvironment(t)

	t.Run("missing credential", func(t *testing.T) {
		transport := &kimiTestTransport{statusCode: http.StatusOK, body: kimiUsageResponse}
		strategy := NewKimiStrategy(&http.Client{Transport: transport})
		_, err := strategy.FetchAccountQuota(context.Background(), &quota.AuthAccount{ID: "empty"})
		if err == nil || len(transport.requests) != 0 {
			t.Fatalf("error = %v, requests = %d; want missing-token error without a request", err, len(transport.requests))
		}
	})

	t.Run("upstream status", func(t *testing.T) {
		transport := &kimiTestTransport{statusCode: http.StatusTooManyRequests, body: "rate limited"}
		strategy := NewKimiStrategy(&http.Client{Transport: transport})
		account := &quota.AuthAccount{ID: "limited", Attributes: map[string]string{"token": "test-token"}}
		_, err := strategy.FetchAccountQuota(context.Background(), account)
		if err == nil || !strings.Contains(err.Error(), "status 429") {
			t.Fatalf("error = %v, want status 429", err)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		transport := &kimiTestTransport{statusCode: http.StatusOK, body: "not-json"}
		strategy := NewKimiStrategy(&http.Client{Transport: transport})
		account := &quota.AuthAccount{ID: "bad-json", Attributes: map[string]string{"token": "test-token"}}
		_, err := strategy.FetchAccountQuota(context.Background(), account)
		if err == nil || !strings.Contains(err.Error(), "decode Kimi usage response") {
			t.Fatalf("error = %v, want JSON decode error", err)
		}
	})

	t.Run("nil account", func(t *testing.T) {
		strategy := NewKimiStrategy(nil)
		if _, err := strategy.FetchAccountQuota(context.Background(), nil); err == nil {
			t.Fatal("expected error for nil account")
		}
	})
}

func TestKimiStrategyProxySupport(t *testing.T) {
	clearStrategyProxyEnvironment(t)
	strategy := NewKimiStrategy(nil)
	baseClient, err := strategy.getHTTPClient(nil)
	if err != nil || baseClient != strategy.httpClient {
		t.Fatalf("no-proxy client = %p, err = %v; want base client", baseClient, err)
	}

	strategy.SetDefaultProxyURL("http://127.0.0.1:1080")
	defaultClient, err := strategy.getHTTPClient(nil)
	if err != nil || defaultClient == strategy.httpClient {
		t.Fatalf("default-proxy client = %p, err = %v; want proxy client", defaultClient, err)
	}

	accountClient, err := strategy.getHTTPClient(&quota.AuthAccount{Attributes: map[string]string{"proxy_url": "http://127.0.0.1:1081"}})
	if err != nil || accountClient == defaultClient {
		t.Fatalf("account-proxy client = %p, err = %v; want account override", accountClient, err)
	}
	metadataClient, err := strategy.getHTTPClient(&quota.AuthAccount{Metadata: map[string]any{"proxy": "http://127.0.0.1:1081"}})
	if err != nil || metadataClient != accountClient {
		t.Fatalf("metadata-proxy client = %p, err = %v; want cached account proxy client %p", metadataClient, err, accountClient)
	}

	strategy.SetDefaultProxyURL("")
	fallbackClient, err := strategy.getHTTPClient(nil)
	if err != nil || fallbackClient != strategy.httpClient {
		t.Fatalf("cleared default proxy client = %p, err = %v; want base client", fallbackClient, err)
	}
}

func clearStrategyProxyEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"PROXY_URL", "HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"} {
		t.Setenv(key, "")
	}
}

type kimiTestTransport struct {
	statusCode int
	body       string
	requests   []*http.Request
}

func (transport *kimiTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.requests = append(transport.requests, request)
	statusCode := transport.statusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	return &http.Response{
		StatusCode: statusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(transport.body)),
		Request:    request,
	}, nil
}
