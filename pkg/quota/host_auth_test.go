package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type mockHostCaller struct {
	handlers map[string]func(payload any) (json.RawMessage, error)
}

func (m *mockHostCaller) CallHost(method string, payload any) (json.RawMessage, error) {
	if h, ok := m.handlers[method]; ok {
		return h(payload)
	}
	return nil, fmt.Errorf("unhandled method %s", method)
}

func TestHostAuthManagerListAccounts(t *testing.T) {
	caller := &mockHostCaller{
		handlers: map[string]func(payload any) (json.RawMessage, error){
			pluginabi.MethodHostAuthList: func(_ any) (json.RawMessage, error) {
				resp := authListResponse{
					Files: []pluginapi.HostAuthFileEntry{
						{
							ID:        "file-1",
							AuthIndex: "idx-1",
							Name:      "antigravity-1.json",
							Type:      "antigravity",
							Provider:  "antigravity",
							Status:    "active",
							Email:     "user1@gmail.com",
						},
						{
							ID:        "file-2",
							AuthIndex: "idx-2",
							Name:      "codex-1.json",
							Type:      "codex",
							Provider:  "codex",
							Status:    "active",
						},
						{
							ID:          "file-runtime",
							AuthIndex:   "idx-runtime",
							Name:        "runtime-gemini",
							Type:        "gemini",
							RuntimeOnly: true,
							Status:      "active",
						},
					},
				}
				raw, err := json.Marshal(resp)
				return json.RawMessage(raw), err
			},
			pluginabi.MethodHostAuthGet: func(payload any) (json.RawMessage, error) {
				req, ok := payload.(pluginapi.HostAuthGetRequest)
				if !ok {
					raw, _ := json.Marshal(payload)
					_ = json.Unmarshal(raw, &req)
				}
				if req.AuthIndex == "idx-runtime" {
					t.Errorf("host.auth.get should NEVER be called for RuntimeOnly credential")
				}
				if req.AuthIndex == "idx-1" {
					resp := pluginapi.HostAuthGetResponse{
						AuthIndex: "idx-1",
						Name:      "antigravity-1.json",
						JSON: json.RawMessage(`{
							"type": "antigravity",
							"email": "user1@gmail.com",
							"project_id": "proj-123",
							"access_token": "token-123",
							"weight": 3
						}`),
					}
					raw, err := json.Marshal(resp)
					return json.RawMessage(raw), err
				}
				if req.AuthIndex == "idx-2" {
					resp := pluginapi.HostAuthGetResponse{
						AuthIndex: "idx-2",
						Name:      "codex-1.json",
						JSON: json.RawMessage(`{
							"type": "codex",
							"api_key": "codex-key",
							"account_id": "acct-999",
							"weight": "2"
						}`),
					}
					raw, err := json.Marshal(resp)
					return json.RawMessage(raw), err
				}
				return nil, fmt.Errorf("not found")
			},
			pluginabi.MethodHostAuthGetRuntime: func(payload any) (json.RawMessage, error) {
				req, ok := payload.(pluginapi.HostAuthGetRequest)
				if !ok {
					raw, _ := json.Marshal(payload)
					_ = json.Unmarshal(raw, &req)
				}
				if req.AuthIndex == "idx-runtime" {
					resp := pluginapi.HostAuthGetRuntimeResponse{
						Auth: pluginapi.HostAuthFileEntry{
							ID:        "file-runtime",
							AuthIndex: "idx-runtime",
							Email:     "runtime@example.com",
							ProjectID: "runtime-proj",
						},
					}
					raw, err := json.Marshal(resp)
					return json.RawMessage(raw), err
				}
				return nil, fmt.Errorf("not found")
			},
		},
	}

	mgr := NewHostAuthManager(caller)
	accounts, err := mgr.ListAccounts(context.Background())
	if err != nil {
		t.Fatalf("ListAccounts error: %v", err)
	}

	if len(accounts) != 3 {
		t.Fatalf("len(accounts) = %d, want 3", len(accounts))
	}

	// 验证 account 1
	a1 := accounts[0]
	if a1.ID != "file-1" || a1.Provider != "antigravity" || a1.Weight != 3 {
		t.Errorf("a1 = %+v, want weight 3", a1)
	}
	if a1.Attributes["project_id"] != "proj-123" || a1.Attributes["access_token"] != "token-123" {
		t.Errorf("a1 attributes = %+v", a1.Attributes)
	}

	// 验证 account 2 (weight 从 string "2" 解析)
	a2 := accounts[1]
	if a2.ID != "file-2" || a2.Provider != "codex" || a2.Weight != 2 {
		t.Errorf("a2 = %+v, want weight 2", a2)
	}
	if a2.Attributes["api_key"] != "codex-key" || a2.Attributes["account_id"] != "acct-999" {
		t.Errorf("a2 attributes = %+v", a2.Attributes)
	}

	// 验证 runtime 账号直接走 CallGetRuntime
	a3 := accounts[2]
	if a3.ID != "file-runtime" || a3.Attributes["email"] != "runtime@example.com" {
		t.Errorf("a3 = %+v", a3)
	}
	if a3.Weight != 1 {
		t.Errorf("a3 weight = %d, want default 1", a3.Weight)
	}
}

func TestParseWeightValue(t *testing.T) {
	tests := []struct {
		input  any
		want   int64
		wantOk bool
	}{
		{input: 5, want: 5, wantOk: true},
		{input: int64(10), want: 10, wantOk: true},
		{input: float64(3.0), want: 3, wantOk: true},
		{input: float64(3.5), want: 0, wantOk: false},
		{input: "4", want: 4, wantOk: true},
		{input: "0", want: 0, wantOk: false},
		{input: "-1", want: 0, wantOk: false},
		{input: "", want: 1, wantOk: true}, // 空字符串默认 1
		{input: "invalid", want: 0, wantOk: false},
		{input: json.Number("8"), want: 8, wantOk: true},
		{input: nil, want: 0, wantOk: false},
	}

	for _, tt := range tests {
		got, ok := parseWeightValue(tt.input)
		if got != tt.want || ok != tt.wantOk {
			t.Errorf("parseWeightValue(%v) = (%d, %v), want (%d, %v)", tt.input, got, ok, tt.want, tt.wantOk)
		}
	}
}
