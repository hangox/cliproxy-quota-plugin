package strategies

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hangox/cliproxy-quota-plugin/pkg/quota"
)

const (
	kimiQuotaURL   = "https://api.kimi.com/coding/v1/usages"
	kimiAIQuotaURL = "https://api.kimi.ai/coding/v1/usages"
)

// KimiStrategy 实现 Kimi Coding 配额采集。
type KimiStrategy struct {
	httpClient      *http.Client
	defaultProxyURL string
	proxyClients    sync.Map
}

var _ quota.ProviderStrategy = (*KimiStrategy)(nil)

// NewKimiStrategy 创建 Kimi 配额策略实例。
func NewKimiStrategy(client *http.Client, defaultProxyURL ...string) *KimiStrategy {
	var proxyURL string
	if len(defaultProxyURL) > 0 {
		proxyURL = strings.TrimSpace(defaultProxyURL[0])
	}
	if proxyURL == "" {
		proxyURL = DefaultEnvProxyURL()
	}
	if client == nil {
		transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
		if proxyURL != "" {
			if parsedURL, err := url.Parse(proxyURL); err == nil {
				transport.Proxy = http.ProxyURL(parsedURL)
			}
		}
		client = &http.Client{Transport: transport, Timeout: 10 * time.Second}
	}
	return &KimiStrategy{httpClient: client, defaultProxyURL: proxyURL}
}

// SetDefaultProxyURL 设置默认代理地址，空值时回退检查环境变量。
func (s *KimiStrategy) SetDefaultProxyURL(proxyURL string) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		proxyURL = DefaultEnvProxyURL()
	}
	s.defaultProxyURL = proxyURL
}

// SetHTTPClient 设置 HTTP 客户端。
func (s *KimiStrategy) SetHTTPClient(client *http.Client) {
	if client != nil {
		s.httpClient = client
	}
}

// Provider 返回策略标识。
func (s *KimiStrategy) Provider() string { return "kimi" }

func (s *KimiStrategy) getHTTPClient(account *quota.AuthAccount) (*http.Client, error) {
	proxyURL := kimiAccountProxy(account)
	if proxyURL == "" {
		proxyURL = s.defaultProxyURL
	}
	if proxyURL == "" {
		return s.httpClient, nil
	}
	if cached, ok := s.proxyClients.Load(proxyURL); ok {
		return cached.(*http.Client), nil
	}
	parsedURL, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL %q: %w", proxyURL, err)
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(parsedURL)},
		Timeout:   10 * time.Second,
	}
	actual, _ := s.proxyClients.LoadOrStore(proxyURL, client)
	return actual.(*http.Client), nil
}

func kimiAccountProxy(account *quota.AuthAccount) string {
	if account == nil {
		return ""
	}
	if account.Attributes != nil {
		if value := strings.TrimSpace(account.Attributes["proxy_url"]); value != "" {
			return value
		}
		if value := strings.TrimSpace(account.Attributes["proxy"]); value != "" {
			return value
		}
	}
	if account.Metadata != nil {
		if value, ok := account.Metadata["proxy_url"].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
		if value, ok := account.Metadata["proxy"].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// FetchAccountQuota 请求 Kimi Coding 配额并转换为统一窗口数据。
func (s *KimiStrategy) FetchAccountQuota(ctx context.Context, account *quota.AuthAccount) (quota.AccountQuotaData, error) {
	data := quota.AccountQuotaData{}
	if account == nil {
		return data, fmt.Errorf("kimi auth is nil")
	}
	data.AuthID = account.ID
	data.Weight = account.Weight
	token := kimiAccountToken(account)
	if token == "" {
		return data, fmt.Errorf("kimi auth %q missing access_token, api_key or token", account.ID)
	}

	client, err := s.getHTTPClient(account)
	if err != nil {
		return data, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, kimiEndpointForAccount(account), nil)
	if err != nil {
		return data, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return data, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return data, fmt.Errorf("read Kimi usage response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		sample := body
		if len(sample) > 256 {
			sample = sample[:256]
		}
		return data, fmt.Errorf("Kimi usage returned status %d: %s", response.StatusCode, string(sample))
	}
	data.Buckets, err = parseKimiBuckets(body)
	if err != nil {
		return data, err
	}
	return data, nil
}

func kimiAccountToken(account *quota.AuthAccount) string {
	if account == nil {
		return ""
	}
	for _, key := range []string{"access_token", "api_key", "token"} {
		if value := strings.TrimSpace(account.Attributes[key]); value != "" {
			return value
		}
	}
	for _, key := range []string{"access_token", "api_key", "token"} {
		if value, ok := account.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func kimiEndpointForAccount(account *quota.AuthAccount) string {
	if account == nil {
		return kimiQuotaURL
	}
	domain := ""
	if account.Attributes != nil {
		domain = firstKimiString(account.Attributes["domain"], account.Attributes["api_domain"], account.Attributes["base_url"], account.Attributes["url"])
	}
	if domain == "" && account.Metadata != nil {
		domain = firstKimiString(
			stringMetadata(account.Metadata["domain"]), stringMetadata(account.Metadata["api_domain"]),
			stringMetadata(account.Metadata["base_url"]), stringMetadata(account.Metadata["url"]),
		)
	}
	identity := domain
	if identity == "" {
		name := ""
		if account.Attributes != nil {
			name = account.Attributes["name"]
		}
		if account.Metadata != nil && name == "" {
			name = stringMetadata(account.Metadata["name"])
		}
		identity = strings.Join([]string{name, account.Name, account.Provider}, " ")
	}
	identity = strings.ToLower(identity)
	if strings.Contains(identity, "kimi.ai") || strings.Contains(identity, "kimi-ai") {
		return kimiAIQuotaURL
	}
	return kimiQuotaURL
}

func firstKimiString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func stringMetadata(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func parseKimiBuckets(body []byte) ([]quota.RawBucket, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode Kimi usage response: %w", err)
	}
	return parseKimiUsagePayload(payload), nil
}

func parseKimiUsagePayload(payload map[string]any) []quota.RawBucket {
	candidates := []map[string]any{payload}
	for _, key := range []string{"data", "result"} {
		if nested, ok := payload[key].(map[string]any); ok {
			candidates = append(candidates, nested)
		}
	}

	var fiveHour *quota.RawBucket
	for _, candidate := range candidates {
		limits, _ := candidate["limits"].([]any)
		for _, raw := range limits {
			limit, _ := raw.(map[string]any)
			if limit == nil || !isKimiFiveHourLimit(limit) {
				continue
			}
			if bucket, ok := kimiLimitBucket("5h", limit); ok {
				fiveHour = &bucket
				break
			}
		}
		if fiveHour != nil {
			break
		}
	}
	if fiveHour == nil {
		for _, candidate := range candidates {
			if raw, ok := candidate["limit_5h"]; ok {
				if bucket, ok := kimiFallbackBucket(raw, candidate); ok {
					fiveHour = &bucket
					break
				}
			}
		}
	}

	buckets := make([]quota.RawBucket, 0, 2)
	if fiveHour != nil {
		buckets = append(buckets, *fiveHour)
	}
	for _, candidate := range candidates {
		usages, _ := candidate["usages"].(map[string]any)
		if usages == nil || usages["limit_month_total"] == nil {
			continue
		}
		monthlyUsage, _ := usages["limit_month_total"].(map[string]any)
		if monthlyUsage == nil {
			monthlyUsage = usages
		}
		used, ok := kimiRatioPercent(firstKimiValue(monthlyUsage["used_ratio"], usages["used_ratio"]))
		if !ok {
			continue
		}
		buckets = append(buckets, quota.RawBucket{
			Kind:             "monthly",
			RemainingPercent: clampKimiPercent(100 - used),
			ResetAt:          kimiResetTime(firstKimiValue(monthlyUsage["reset_time"], monthlyUsage["resetTime"], usages["reset_time"], usages["resetTime"])),
		})
		break
	}
	return buckets
}

func isKimiFiveHourLimit(limit map[string]any) bool {
	for _, key := range []string{"window_minutes", "duration_minutes", "limit_window_minutes"} {
		if value, ok := kimiNumber(limit[key]); ok && value == 300 {
			return true
		}
	}
	unit := strings.ToLower(kimiString(firstKimiValue(limit["unit"], limit["time_unit"], limit["timeUnit"])))
	for _, key := range []string{"window", "duration", "period"} {
		value := limit[key]
		text := strings.ToLower(kimiString(value))
		if text == "5h" || text == "pt5h" || text == "300m" || text == "300min" || text == "300 minutes" {
			return true
		}
		if number, ok := kimiNumber(value); ok && number == 300 && !strings.Contains(unit, "second") && !strings.Contains(unit, "hour") && !strings.Contains(unit, "hr") {
			return true
		}
		if number, ok := kimiNumber(value); ok && number == 5 && (strings.Contains(unit, "hour") || strings.Contains(unit, "hr")) {
			return true
		}
	}
	for _, key := range []string{"window_seconds", "duration_seconds", "limit_window_seconds"} {
		if value, ok := kimiNumber(limit[key]); ok && value == 18000 {
			return true
		}
	}
	return false
}

func kimiLimitBucket(kind string, values map[string]any) (quota.RawBucket, bool) {
	limit := values["limit"]
	used, hasUsed := kimiAmountPercent(values["used"], limit)
	remaining, hasRemaining := kimiAmountPercent(values["remaining"], limit)
	if !hasUsed {
		used, hasUsed = kimiPercentage(firstKimiValue(values["used_percent"], values["usedPercent"]))
	}
	if !hasRemaining {
		remaining, hasRemaining = kimiPercentage(firstKimiValue(values["remaining_percent"], values["remainingPercent"]))
	}
	if !hasUsed {
		used, hasUsed = kimiRatioPercent(values["used_ratio"])
	}
	if !hasRemaining {
		remaining, hasRemaining = kimiRatioPercent(values["remaining_ratio"])
	}
	if !hasUsed && hasRemaining {
		used, hasUsed = clampKimiPercent(100-remaining), true
	}
	if !hasRemaining && hasUsed {
		remaining, hasRemaining = clampKimiPercent(100-used), true
	}
	if !hasUsed || !hasRemaining {
		return quota.RawBucket{}, false
	}
	return quota.RawBucket{
		Kind: kind, RemainingPercent: remaining,
		ResetAt: kimiResetTime(firstKimiValue(values["resetTime"], values["reset_time"], values["reset_at"])),
	}, true
}

func kimiFallbackBucket(raw any, parent map[string]any) (quota.RawBucket, bool) {
	if values, ok := raw.(map[string]any); ok {
		bucket, valid := kimiLimitBucket("5h", values)
		if valid && bucket.ResetAt.IsZero() {
			bucket.ResetAt = kimiResetTime(firstKimiValue(parent["resetTime"], parent["reset_time"]))
		}
		return bucket, valid
	}
	used, ok := kimiRatioPercent(raw)
	if !ok {
		return quota.RawBucket{}, false
	}
	return quota.RawBucket{
		Kind: "5h", RemainingPercent: clampKimiPercent(100 - used),
		ResetAt: kimiResetTime(firstKimiValue(parent["resetTime"], parent["reset_time"])),
	}, true
}

func kimiRatioPercent(value any) (float64, bool) {
	ratio, ok := kimiNumber(value)
	if !ok {
		return 0, false
	}
	return clampKimiPercent(ratio * 100), true
}

func kimiPercentage(value any) (float64, bool) {
	percentage, ok := kimiNumber(value)
	if !ok {
		return 0, false
	}
	return clampKimiPercent(percentage), true
}

func kimiAmountPercent(amountValue, limitValue any) (float64, bool) {
	amount, okAmount := kimiNumber(amountValue)
	limit, okLimit := kimiNumber(limitValue)
	if !okAmount || !okLimit || limit <= 0 {
		return 0, false
	}
	return clampKimiPercent(amount / limit * 100), true
}

func kimiNumber(value any) (float64, bool) {
	var number float64
	switch value := value.(type) {
	case float64:
		number = value
	case float32:
		number = float64(value)
	case int:
		number = float64(value)
	case int64:
		number = float64(value)
	case json.Number:
		parsed, err := value.Float64()
		if err != nil {
			return 0, false
		}
		number = parsed
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		return 0, false
	}
	return number, !math.IsNaN(number) && !math.IsInf(number, 0)
}

func kimiResetTime(value any) time.Time {
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return parsed
		}
	}
	seconds, ok := kimiNumber(value)
	if !ok || seconds <= 0 {
		return time.Time{}
	}
	if seconds > 1e12 {
		return time.UnixMilli(int64(seconds))
	}
	return time.Unix(int64(seconds), 0)
}

func clampKimiPercent(value float64) float64 {
	value = math.Max(0, math.Min(100, value))
	return math.Round(value*100) / 100
}

func firstKimiValue(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func kimiString(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}
