package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/hangox/cliproxy-quota-plugin/pkg/quota"
)

type dummyAccountLister struct{}

func (d *dummyAccountLister) ListAccounts(_ context.Context) ([]*quota.AuthAccount, error) {
	return []*quota.AuthAccount{
		{
			ID:       "acc-1",
			Provider: "antigravity",
			Status:   "active",
			Weight:   1,
		},
	}, nil
}

type dummyStrategy struct{}

func (d *dummyStrategy) Provider() string {
	return "antigravity"
}

func (d *dummyStrategy) FetchAccountQuota(_ context.Context, _ *quota.AuthAccount) (quota.AccountQuotaData, error) {
	return quota.AccountQuotaData{
		AuthID: "acc-1",
		Buckets: []quota.RawBucket{
			{Kind: "5h", RemainingPercent: 80.0, ResetAt: time.Date(2026, 9, 23, 15, 0, 0, 0, time.UTC)},
		},
	}, nil
}

func TestPluginRegistration(t *testing.T) {
	engine := quota.NewQuotaEngine(&dummyAccountLister{})
	p := NewPlugin(engine)

	reg := p.Register()
	if reg.Metadata.Name != PluginName {
		t.Errorf("PluginName = %q, want %q", reg.Metadata.Name, PluginName)
	}
	if !reg.Capabilities.ManagementAPI {
		t.Errorf("ManagementAPI = false, want true")
	}

	mReg := p.ManagementRegister()
	if len(mReg.Resources) != 1 {
		t.Fatalf("len(Resources) = %d, want 1", len(mReg.Resources))
	}
	if mReg.Resources[0].Path != ResourcePath {
		t.Errorf("Resource Path = %q, want %q", mReg.Resources[0].Path, ResourcePath)
	}
}

func TestPluginHandleManagement(t *testing.T) {
	engine := quota.NewQuotaEngine(&dummyAccountLister{})
	engine.Register(&dummyStrategy{})
	p := NewPlugin(engine)

	// 1. 请求 /status
	req := ManagementRequest{
		Method: http.MethodGet,
		Path:   ResourcePath,
	}
	rawReq, _ := json.Marshal(req)

	resp, err := p.HandleManagement(context.Background(), rawReq)
	if err != nil {
		t.Fatalf("HandleManagement error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Headers.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json; charset=utf-8", ct)
	}

	var unified quota.UnifiedQuotaResponse
	if errUnmarshal := json.Unmarshal(resp.Body, &unified); errUnmarshal != nil {
		t.Fatalf("Unmarshal unified response: %v", errUnmarshal)
	}
	if _, ok := unified.Providers["antigravity"]; !ok {
		t.Errorf("antigravity provider missing in unified response: %+v", unified)
	}

	// 2. 带 query ?provider=antigravity
	reqWithQuery := ManagementRequest{
		Method: http.MethodGet,
		Path:   ResourcePath,
		Query: url.Values{
			"provider": []string{"antigravity"},
		},
	}
	rawReqWithQuery, _ := json.Marshal(reqWithQuery)
	resp2, err2 := p.HandleManagement(context.Background(), rawReqWithQuery)
	if err2 != nil {
		t.Fatalf("HandleManagement with query error: %v", err2)
	}
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", resp2.StatusCode)
	}

	var providerRes quota.ProviderQuotaResult
	if errUnmarshal := json.Unmarshal(resp2.Body, &providerRes); errUnmarshal != nil {
		t.Fatalf("Unmarshal provider response: %v", errUnmarshal)
	}
	if providerRes.Provider != "antigravity" || providerRes.ActiveAccounts != 1 {
		t.Errorf("unexpected provider response: %+v", providerRes)
	}

	// 3. 错误路径测试
	badReq := ManagementRequest{
		Method: http.MethodGet,
		Path:   "/invalid-path",
	}
	rawBadReq, _ := json.Marshal(badReq)
	resp3, _ := p.HandleManagement(context.Background(), rawBadReq)
	if resp3.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want 404", resp3.StatusCode)
	}
}

type dummyProxyStrategy struct {
	proxyURL string
}

func (d *dummyProxyStrategy) Provider() string {
	return "dummy"
}

func (d *dummyProxyStrategy) FetchAccountQuota(_ context.Context, _ *quota.AuthAccount) (quota.AccountQuotaData, error) {
	return quota.AccountQuotaData{}, nil
}

func (d *dummyProxyStrategy) SetDefaultProxyURL(u string) {
	d.proxyURL = u
}

func TestPluginConfigureAndParseProxyURL(t *testing.T) {
	// 1. 验证各种输入格式解析
	tests := []struct {
		name  string
		raw   string
		want  string
	}{
		{
			name: "rpcLifecycleRequest yaml bytes",
			raw:  `{"config_yaml":"cHJveHlfdXJsOiBodHRwOi8vMTI3LjAuMC4xOjc4OTAK"}` , // base64 of "proxy_url: http://127.0.0.1:7890\n"
			want: "http://127.0.0.1:7890",
		},
		{
			name: "direct JSON object",
			raw:  `{"proxy_url": "http://127.0.0.1:8888"}`,
			want: "http://127.0.0.1:8888",
		},
		{
			name: "raw yaml",
			raw:  "proxy_url: http://127.0.0.1:9999\n",
			want: "http://127.0.0.1:9999",
		},
		{
			name: "empty config",
			raw:  `{}`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseProxyURLFromConfig([]byte(tt.raw))
			if got != tt.want {
				t.Errorf("ParseProxyURLFromConfig() = %q, want %q", got, tt.want)
			}
		})
	}

	// 2. 验证 Plugin.Configure 动态同步更新至引擎策略
	engine := quota.NewQuotaEngine(&dummyAccountLister{})
	strat := &dummyProxyStrategy{}
	engine.Register(strat)
	p := NewPlugin(engine)

	reqRaw := []byte(`{"proxy_url": "http://10.0.0.1:1080"}`)
	if err := p.Configure(reqRaw); err != nil {
		t.Fatalf("Configure failed: %v", err)
	}
	if strat.proxyURL != "http://10.0.0.1:1080" {
		t.Errorf("strat.proxyURL = %q, want http://10.0.0.1:1080", strat.proxyURL)
	}
}

