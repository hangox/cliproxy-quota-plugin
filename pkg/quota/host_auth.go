package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// HostCaller 抽象调用宿主 C-ABI 回调的接口。
type HostCaller interface {
	CallHost(method string, payload any) (json.RawMessage, error)
}

type authListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

// HostAuthManager 封装宿主 auth 回调客户端。
type HostAuthManager struct {
	caller HostCaller
}

// NewHostAuthManager 创建宿主 Auth 回调管理器。
func NewHostAuthManager(caller HostCaller) *HostAuthManager {
	return &HostAuthManager{caller: caller}
}

// CallList 直接调用 host.auth.list 获取凭据文件摘要列表。
func (m *HostAuthManager) CallList(ctx context.Context) ([]pluginapi.HostAuthFileEntry, error) {
	_ = ctx
	if m.caller == nil {
		return nil, fmt.Errorf("host caller is not configured")
	}
	raw, err := m.caller.CallHost(pluginabi.MethodHostAuthList, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("call host.auth.list failed: %w", err)
	}
	var resp authListResponse
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host.auth.list response: %w", errUnmarshal)
	}
	return resp.Files, nil
}

// CallGet 直接调用 host.auth.get 根据 authIndex 获取实体凭据 JSON。
func (m *HostAuthManager) CallGet(ctx context.Context, authIndex string) (pluginapi.HostAuthGetResponse, error) {
	_ = ctx
	if m.caller == nil {
		return pluginapi.HostAuthGetResponse{}, fmt.Errorf("host caller is not configured")
	}
	raw, err := m.caller.CallHost(pluginabi.MethodHostAuthGet, pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	if err != nil {
		return pluginapi.HostAuthGetResponse{}, fmt.Errorf("call host.auth.get failed: %w", err)
	}
	var resp pluginapi.HostAuthGetResponse
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		return pluginapi.HostAuthGetResponse{}, fmt.Errorf("decode host.auth.get response: %w", errUnmarshal)
	}
	return resp, nil
}

// CallGetRuntime 直接调用 host.auth.get_runtime 获取运行时凭据信息。
func (m *HostAuthManager) CallGetRuntime(ctx context.Context, authIndex string) (pluginapi.HostAuthGetRuntimeResponse, error) {
	_ = ctx
	if m.caller == nil {
		return pluginapi.HostAuthGetRuntimeResponse{}, fmt.Errorf("host caller is not configured")
	}
	raw, err := m.caller.CallHost(pluginabi.MethodHostAuthGetRuntime, pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	if err != nil {
		return pluginapi.HostAuthGetRuntimeResponse{}, fmt.Errorf("call host.auth.get_runtime failed: %w", err)
	}
	var resp pluginapi.HostAuthGetRuntimeResponse
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		return pluginapi.HostAuthGetRuntimeResponse{}, fmt.Errorf("decode host.auth.get_runtime response: %w", errUnmarshal)
	}
	return resp, nil
}

// ListAccounts 封装调用 host.auth.list 与 host.auth.get，解析出用于配额池化的统一 AuthAccount 列表。
func (m *HostAuthManager) ListAccounts(ctx context.Context) ([]*AuthAccount, error) {
	files, errList := m.CallList(ctx)
	if errList != nil {
		return nil, errList
	}

	accounts := make([]*AuthAccount, 0, len(files))
	for _, f := range files {
		acc := &AuthAccount{
			ID:         strings.TrimSpace(f.ID),
			AuthIndex:  strings.TrimSpace(f.AuthIndex),
			Name:       strings.TrimSpace(f.Name),
			Provider:   strings.TrimSpace(f.Provider),
			Status:     strings.TrimSpace(f.Status),
			Disabled:   f.Disabled,
			Weight:     1, // 默认权重为 1
			Attributes: make(map[string]string),
			Metadata:   make(map[string]any),
		}

		if acc.ID == "" {
			acc.ID = acc.Name
		}
		if acc.Provider == "" {
			acc.Provider = strings.TrimSpace(f.Type)
		}

		if f.Email != "" {
			acc.Attributes["email"] = f.Email
			acc.Metadata["email"] = f.Email
		}
		if f.ProjectID != "" {
			acc.Attributes["project_id"] = f.ProjectID
			acc.Metadata["project_id"] = f.ProjectID
		}
		if f.Path != "" {
			acc.Attributes["path"] = f.Path
		}

		// 如果是内存运行时凭据，直接查询运行时信息，消除对物理文件发起无意义的 CallGet
		if f.RuntimeOnly {
			if acc.AuthIndex != "" {
				if runtimeResp, errRuntime := m.CallGetRuntime(ctx, acc.AuthIndex); errRuntime == nil {
					if runtimeResp.Auth.Email != "" {
						acc.Attributes["email"] = runtimeResp.Auth.Email
						acc.Metadata["email"] = runtimeResp.Auth.Email
					}
					if runtimeResp.Auth.ProjectID != "" {
						acc.Attributes["project_id"] = runtimeResp.Auth.ProjectID
						acc.Metadata["project_id"] = runtimeResp.Auth.ProjectID
					}
				}
			}
		} else if acc.AuthIndex != "" {
			// 通过 host.auth.get 读取物理 JSON 详情
			getResp, errGet := m.CallGet(ctx, acc.AuthIndex)
			if errGet == nil && len(getResp.JSON) > 0 {
				var meta map[string]any
				if errUnmarshal := json.Unmarshal(getResp.JSON, &meta); errUnmarshal == nil {
					for k, v := range meta {
						acc.Metadata[k] = v
						if strVal, ok := v.(string); ok && strings.TrimSpace(strVal) != "" {
							acc.Attributes[k] = strings.TrimSpace(strVal)
						}
					}
					// 解析权重
					if rawWeight, exists := meta["weight"]; exists {
						weight, ok := parseWeightValue(rawWeight)
						if ok {
							acc.Weight = weight
						} else {
							acc.Weight = 0
						}
					}
				}
			}
		}

		accounts = append(accounts, acc)
	}

	return accounts, nil
}

func parseWeightValue(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		if typed <= 0 {
			return 0, false
		}
		return int64(typed), true
	case int64:
		if typed <= 0 {
			return 0, false
		}
		return typed, true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed || typed <= 0 {
			return 0, false
		}
		return int64(typed), true
	case json.Number:
		n, err := typed.Int64()
		if err != nil || n <= 0 {
			return 0, false
		}
		return n, true
	case string:
		typed = strings.TrimSpace(typed)
		if typed == "" {
			return 1, true
		}
		n, err := strconv.ParseInt(typed, 10, 64)
		if err != nil || n <= 0 {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}
