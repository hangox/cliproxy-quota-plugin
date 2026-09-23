package strategies

import (
	"bytes"
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
	antigravityQuotaSummaryUserAgent = "antigravity/cli/1.0.13 (aidev_client; os_type=darwin; arch=arm64)"
	antigravityOAuthTokenURL         = "https://oauth2.googleapis.com/token"
)

func defaultAntigravityClientID() string {
	bytes := []byte{107, 106, 109, 107, 106, 106, 108, 106, 108, 106, 111, 99, 107, 119, 46, 55, 50, 41, 41, 51, 52, 104, 50, 104, 107, 54, 57, 40, 63, 104, 105, 111, 44, 46, 53, 54, 53, 48, 50, 110, 61, 110, 106, 105, 63, 42, 116, 59, 42, 42, 41, 116, 61, 53, 53, 61, 54, 63, 47, 41, 63, 40, 57, 53, 52, 46, 63, 52, 46, 116, 57, 53, 55}
	for i := range bytes {
		bytes[i] ^= 0x5A
	}
	return string(bytes)
}

func defaultAntigravityClientSecret() string {
	bytes := []byte{29, 21, 25, 9, 10, 2, 119, 17, 111, 98, 28, 13, 8, 110, 98, 108, 22, 62, 22, 16, 107, 55, 22, 24, 98, 41, 2, 25, 110, 32, 108, 43, 30, 27, 60}
	for i := range bytes {
		bytes[i] ^= 0x5A
	}
	return string(bytes)
}

var defaultAntigravityEndpoints = []string{
	"https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
	"https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:retrieveUserQuotaSummary",
	"https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
}

// AntigravityStrategy 实现 Google Antigravity 配额策略。
type AntigravityStrategy struct {
	httpClient      *http.Client
	endpoints       []string
	userAgent       string
	defaultProxyURL string
	proxyClients    sync.Map // 缓存代理客户端，防止频繁创建 Transport 导致连接池泄漏
}

// NewAntigravityStrategy 创建 Antigravity 配额策略。
func NewAntigravityStrategy(client *http.Client, endpoints ...string) *AntigravityStrategy {
	if client == nil {
		client = &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
			},
			Timeout: 10 * time.Second,
		}
	}
	ep := defaultAntigravityEndpoints
	if len(endpoints) > 0 {
		ep = endpoints
	}
	return &AntigravityStrategy{
		httpClient:      client,
		endpoints:       ep,
		userAgent:       antigravityQuotaSummaryUserAgent,
		defaultProxyURL: DefaultEnvProxyURL(),
	}
}

// SetDefaultProxyURL 设置默认代理地址，若传空则回退检查环境变量。
func (s *AntigravityStrategy) SetDefaultProxyURL(proxyURL string) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		proxyURL = DefaultEnvProxyURL()
	}
	s.defaultProxyURL = proxyURL
}

func (s *AntigravityStrategy) getHTTPClient(account *quota.AuthAccount) (*http.Client, error) {
	proxyURL := ""
	if account != nil {
		if account.Attributes != nil {
			if v := strings.TrimSpace(account.Attributes["proxy_url"]); v != "" {
				proxyURL = v
			} else if v := strings.TrimSpace(account.Attributes["proxy"]); v != "" {
				proxyURL = v
			}
		}
		if proxyURL == "" && account.Metadata != nil {
			if v, ok := account.Metadata["proxy_url"].(string); ok && strings.TrimSpace(v) != "" {
				proxyURL = strings.TrimSpace(v)
			} else if v, ok := account.Metadata["proxy"].(string); ok && strings.TrimSpace(v) != "" {
				proxyURL = strings.TrimSpace(v)
			}
		}
	}
	if proxyURL == "" {
		proxyURL = s.defaultProxyURL
	}
	if proxyURL == "" {
		return s.httpClient, nil
	}
	if v, ok := s.proxyClients.Load(proxyURL); ok {
		return v.(*http.Client), nil
	}
	parsedURL, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL %q: %w", proxyURL, err)
	}
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(parsedURL),
		},
		Timeout: 10 * time.Second,
	}
	actual, _ := s.proxyClients.LoadOrStore(proxyURL, client)
	return actual.(*http.Client), nil
}

// SetEndpoints 设置请求地址（用于测试或定制）。
func (s *AntigravityStrategy) SetEndpoints(endpoints []string) {
	if len(endpoints) > 0 {
		s.endpoints = endpoints
	}
}

// SetHTTPClient 设置 HTTP 客户端。
func (s *AntigravityStrategy) SetHTTPClient(client *http.Client) {
	if client != nil {
		s.httpClient = client
	}
}

// Provider 返回策略对应的 provider 标识。
func (s *AntigravityStrategy) Provider() string {
	return "antigravity"
}

type antigravityQuotaSummaryJSON struct {
	Groups json.RawMessage `json:"groups"`
}

type antigravityQuotaGroupJSON struct {
	Name           string          `json:"name"`
	Label          string          `json:"label"`
	DisplayName    string          `json:"displayName"`
	DisplayNameAlt string          `json:"display_name"`
	Buckets        json.RawMessage `json:"buckets"`
}

type antigravityQuotaBucketJSON struct {
	Name                 string          `json:"name"`
	Label                string          `json:"label"`
	BucketID             string          `json:"bucketId"`
	BucketIDAlt          string          `json:"bucket_id"`
	Window               string          `json:"window"`
	DisplayName          string          `json:"displayName"`
	DisplayNameAlt       string          `json:"display_name"`
	RemainingFraction    json.RawMessage `json:"remainingFraction"`
	RemainingFractionAlt json.RawMessage `json:"remaining_fraction"`
	ResetTime            string          `json:"resetTime"`
	ResetTimeAlt         string          `json:"reset_time"`
}

// FetchAccountQuota 采集单个 Antigravity 账号的配额数据。
func (s *AntigravityStrategy) FetchAccountQuota(ctx context.Context, account *quota.AuthAccount) (quota.AccountQuotaData, error) {
	data := quota.AccountQuotaData{
		AuthID: account.ID,
		Weight: account.Weight,
		Groups: make(map[string][]quota.RawBucket),
	}

	client, errClient := s.getHTTPClient(account)
	if errClient != nil {
		return data, errClient
	}

	accessToken, projectID := s.extractCredentials(ctx, account, client)
	if accessToken == "" {
		return data, fmt.Errorf("antigravity auth %q missing access token", account.ID)
	}

	reqPayload := map[string]any{}
	if projectID != "" {
		reqPayload["project"] = projectID
	}
	reqBytes, errMarshal := json.Marshal(reqPayload)
	if errMarshal != nil {
		return data, fmt.Errorf("marshal request payload: %w", errMarshal)
	}

	var lastErr error
	for _, endpoint := range s.endpoints {
		req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBytes))
		if errReq != nil {
			lastErr = errReq
			continue
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", s.userAgent)

		resp, errDo := client.Do(req)
		if errDo != nil {
			lastErr = errDo
			continue
		}
		respBytes, errRead := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if errRead != nil {
			lastErr = errRead
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("retrieveUserQuotaSummary returned status %d: %s", resp.StatusCode, string(respBytes))
			continue
		}

		groups, errParse := parseAntigravityGroups(respBytes)
		if errParse != nil {
			lastErr = errParse
			continue
		}

		for _, grp := range groups {
			grpName := classifyAntigravityGroup(grp.Name, grp.Label)
			if grpName == "" {
				continue
			}
			for _, b := range grp.Buckets {
				kind := classifyAntigravityBucket(b.Name, b.Label)
				if kind == "" {
					continue
				}
				pct := b.RemainingFraction * 100
				pct = math.Max(0, math.Min(100, pct))
				pct = math.Round(pct*100) / 100
				data.Groups[grpName] = append(data.Groups[grpName], quota.RawBucket{
					Kind:             kind,
					RemainingPercent: pct,
					ResetAt:          b.ResetAt,
				})
			}
		}

		return data, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("retrieveUserQuotaSummary: no endpoint succeeded")
	}
	return data, lastErr
}

func (s *AntigravityStrategy) extractCredentials(ctx context.Context, account *quota.AuthAccount, client *http.Client) (string, string) {
	var accessToken, refreshToken, projectID string

	// 1. 读取 attributes
	if account.Attributes != nil {
		if v := strings.TrimSpace(account.Attributes["access_token"]); v != "" {
			accessToken = v
		} else if v := strings.TrimSpace(account.Attributes["api_key"]); v != "" {
			accessToken = v
		}
		if v := strings.TrimSpace(account.Attributes["refresh_token"]); v != "" {
			refreshToken = v
		}
		if v := strings.TrimSpace(account.Attributes["project_id"]); v != "" {
			projectID = v
		}
	}

	// 2. 读取 metadata 补充
	if account.Metadata != nil {
		if accessToken == "" {
			if v, ok := account.Metadata["access_token"].(string); ok && strings.TrimSpace(v) != "" {
				accessToken = strings.TrimSpace(v)
			} else if v, ok := account.Metadata["api_key"].(string); ok && strings.TrimSpace(v) != "" {
				accessToken = strings.TrimSpace(v)
			}
		}
		if refreshToken == "" {
			if v, ok := account.Metadata["refresh_token"].(string); ok && strings.TrimSpace(v) != "" {
				refreshToken = strings.TrimSpace(v)
			}
		}
		if projectID == "" {
			if v, ok := account.Metadata["project_id"].(string); ok && strings.TrimSpace(v) != "" {
				projectID = strings.TrimSpace(v)
			}
		}
	}

	// 如果没有 access_token 但有 refresh_token，尝试刷新
	if accessToken == "" && refreshToken != "" {
		refreshedToken, errRefresh := s.refreshToken(ctx, refreshToken, client)
		if errRefresh == nil && refreshedToken != "" {
			accessToken = refreshedToken
		}
	}

	return accessToken, projectID
}

func (s *AntigravityStrategy) refreshToken(ctx context.Context, refreshToken string, client *http.Client) (string, error) {
	if client == nil {
		client = s.httpClient
	}

	form := url.Values{}
	form.Set("client_id", defaultAntigravityClientID())
	form.Set("client_secret", defaultAntigravityClientSecret())
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, antigravityOAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Go-http-client/2.0")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("token refresh returned status %d", resp.StatusCode)
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", err
	}
	return tokenResp.AccessToken, nil
}

type parsedAntigravityGroup struct {
	Name    string
	Label   string
	Buckets []parsedAntigravityBucket
}

type parsedAntigravityBucket struct {
	Name              string
	Label             string
	RemainingFraction float64
	ResetAt           time.Time
}

func parseAntigravityGroups(body []byte) ([]parsedAntigravityGroup, error) {
	var payload antigravityQuotaSummaryJSON
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if len(payload.Groups) == 0 || string(payload.Groups) == "null" {
		return nil, nil
	}

	var groups []antigravityQuotaGroupJSON
	if err := json.Unmarshal(payload.Groups, &groups); err != nil {
		var groupMap map[string]antigravityQuotaGroupJSON
		if mapErr := json.Unmarshal(payload.Groups, &groupMap); mapErr != nil {
			return nil, err
		}
		for name, group := range groupMap {
			if strings.TrimSpace(group.Name) == "" {
				group.Name = name
			}
			groups = append(groups, group)
		}
	}

	out := make([]parsedAntigravityGroup, 0, len(groups))
	for _, group := range groups {
		label := strings.TrimSpace(group.Label)
		if label == "" {
			label = strings.TrimSpace(group.DisplayName)
		}
		if label == "" {
			label = strings.TrimSpace(group.DisplayNameAlt)
		}
		buckets, errBuckets := parseAntigravityBuckets(group.Buckets)
		if errBuckets != nil {
			return nil, errBuckets
		}
		name := strings.TrimSpace(group.Name)
		if name == "" {
			name = label
		}
		out = append(out, parsedAntigravityGroup{
			Name:    name,
			Label:   label,
			Buckets: buckets,
		})
	}
	return out, nil
}

func parseAntigravityBuckets(raw json.RawMessage) ([]parsedAntigravityBucket, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var items []antigravityQuotaBucketJSON
	if err := json.Unmarshal(raw, &items); err != nil {
		var itemMap map[string]antigravityQuotaBucketJSON
		if mapErr := json.Unmarshal(raw, &itemMap); mapErr != nil {
			return nil, err
		}
		for name, item := range itemMap {
			if strings.TrimSpace(item.Name) == "" {
				item.Name = name
			}
			items = append(items, item)
		}
	}

	out := make([]parsedAntigravityBucket, 0, len(items))
	for _, item := range items {
		remainingRaw := item.RemainingFraction
		if len(remainingRaw) == 0 {
			remainingRaw = item.RemainingFractionAlt
		}
		remainingFraction, okFraction := parseAntigravityQuotaFraction(remainingRaw)
		if !okFraction {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = strings.TrimSpace(item.BucketID)
		}
		if name == "" {
			name = strings.TrimSpace(item.BucketIDAlt)
		}
		label := strings.TrimSpace(item.Label)
		if label == "" {
			label = strings.TrimSpace(item.DisplayName)
		}
		if label == "" {
			label = strings.TrimSpace(item.DisplayNameAlt)
		}
		if window := strings.TrimSpace(item.Window); window != "" {
			label = strings.TrimSpace(label + " " + window)
		}
		resetTime := strings.TrimSpace(item.ResetTime)
		if resetTime == "" {
			resetTime = strings.TrimSpace(item.ResetTimeAlt)
		}
		resetAt := time.Time{}
		if resetTime != "" {
			if parsed, errParse := time.Parse(time.RFC3339, resetTime); errParse == nil {
				resetAt = parsed
			}
		}
		out = append(out, parsedAntigravityBucket{
			Name:              name,
			Label:             label,
			RemainingFraction: remainingFraction,
			ResetAt:           resetAt,
		})
	}
	return out, nil
}

func parseAntigravityQuotaFraction(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var numeric float64
	if err := json.Unmarshal(raw, &numeric); err == nil && !math.IsNaN(numeric) && !math.IsInf(numeric, 0) {
		return numeric, true
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, false
	}
	numeric, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	return numeric, err == nil && !math.IsNaN(numeric) && !math.IsInf(numeric, 0)
}

func classifyAntigravityGroup(name, label string) string {
	value := strings.ToLower(strings.TrimSpace(name + " " + label))
	switch {
	case strings.Contains(value, "gemini"):
		return "gemini"
	case strings.Contains(value, "claude"), strings.Contains(value, "gpt"):
		return "claude_gpt"
	default:
		return ""
	}
}

func classifyAntigravityBucket(name, label string) string {
	value := strings.ToLower(strings.TrimSpace(name + " " + label))
	value = strings.NewReplacer("_", " ", "-", " ").Replace(value)
	switch {
	case strings.Contains(value, "hour"), strings.Contains(value, "5h"):
		return "5h"
	case strings.Contains(value, "week"), strings.Contains(value, "7 day"), strings.Contains(value, "7d"):
		return "7d"
	default:
		return ""
	}
}
