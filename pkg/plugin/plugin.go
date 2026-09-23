package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hangox/cliproxy-quota-plugin/pkg/quota"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	// PluginName 是插件唯一注册标识符。
	PluginName = "cliproxy-quota-plugin"
	// ResourcePath 是插件暴露的管理端点路径。
	ResourcePath = "/status"
	// DefaultRequestTimeout 是管理接口默认请求超时时间。
	DefaultRequestTimeout = 30 * time.Second
)

// Registration 定义插件向宿主汇报的注册信息。
type Registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  RegistrationCapabilities `json:"capabilities"`
}

// RegistrationCapabilities 声明插件支持的宿主扩展能力。
type RegistrationCapabilities struct {
	ManagementAPI bool `json:"management_api"`
}

// ManagementRegistration 声明 Management 资源列表。
type ManagementRegistration struct {
	Resources []ManagementResource `json:"resources,omitempty"`
}

// ManagementResource 声明单个资源端点信息。
type ManagementResource struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description"`
}

// ManagementRequest 代表宿主路由转发进来的请求。
type ManagementRequest struct {
	Method         string      `json:"Method"`
	Path           string      `json:"Path"`
	Headers        http.Header `json:"Headers"`
	Query          url.Values  `json:"Query"`
	Body           []byte      `json:"Body"`
	HostCallbackID string      `json:"host_callback_id,omitempty"`
}

// ManagementResponse 代表插件返回给宿主的 HTTP 响应。
type ManagementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

// Plugin 管理配额插件的注册与端点路由。
type Plugin struct {
	engine  *quota.QuotaEngine
	timeout time.Duration
}

// NewPlugin 创建插件实例。
func NewPlugin(engine *quota.QuotaEngine) *Plugin {
	return &Plugin{
		engine:  engine,
		timeout: DefaultRequestTimeout,
	}
}

// SetTimeout 设置管理接口请求超时时间。
func (p *Plugin) SetTimeout(d time.Duration) {
	if d > 0 {
		p.timeout = d
	}
}

// Register 返回插件基础元数据与能力声明。
func (p *Plugin) Register() Registration {
	return Registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             PluginName,
			Version:          "0.1.0",
			Author:           "hangox",
			GitHubRepository: "https://github.com/hangox/cliproxy-quota-plugin",
			ConfigFields:     []pluginapi.ConfigField{},
		},
		Capabilities: RegistrationCapabilities{
			ManagementAPI: true,
		},
	}
}

// ManagementRegister 返回管理 API 资源列表。
func (p *Plugin) ManagementRegister() ManagementRegistration {
	return ManagementRegistration{
		Resources: []ManagementResource{{
			Path:        ResourcePath,
			Menu:        "Quota Status",
			Description: "Multi-account weighted quota pooling status for CLIProxyAPI",
		}},
	}
}

// HandleManagement 处理针对插件注册端点的 HTTP 调用（注入请求级超时控制）。
func (p *Plugin) HandleManagement(ctx context.Context, raw []byte) (ManagementResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	var req ManagementRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return errorJSONResponse(http.StatusBadRequest, fmt.Sprintf("invalid management request: %v", errUnmarshal)), nil
		}
	}

	path := strings.TrimSpace(req.Path)
	if path != "" && path != ResourcePath && !strings.HasSuffix(path, ResourcePath) {
		return errorJSONResponse(http.StatusNotFound, "resource not found: "+path), nil
	}

	provider := ""
	if req.Query != nil {
		provider = strings.TrimSpace(req.Query.Get("provider"))
	}

	if provider != "" {
		res, err := p.engine.CollectProvider(reqCtx, provider)
		if err != nil {
			return errorJSONResponse(http.StatusInternalServerError, err.Error()), nil
		}
		return jsonResponse(http.StatusOK, res), nil
	}

	allRes, err := p.engine.CollectAll(reqCtx)
	if err != nil {
		return errorJSONResponse(http.StatusInternalServerError, err.Error()), nil
	}
	return jsonResponse(http.StatusOK, allRes), nil
}

func jsonResponse(statusCode int, data any) ManagementResponse {
	body, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		body = []byte(fmt.Sprintf(`{"error": %q}`, err.Error()))
	}
	return ManagementResponse{
		StatusCode: statusCode,
		Headers: http.Header{
			"Content-Type":  []string{"application/json; charset=utf-8"},
			"Cache-Control": []string{"no-store, no-cache, must-revalidate"},
		},
		Body: body,
	}
}

func errorJSONResponse(statusCode int, message string) ManagementResponse {
	body, _ := json.Marshal(map[string]any{
		"error": message,
	})
	return ManagementResponse{
		StatusCode: statusCode,
		Headers: http.Header{
			"Content-Type": []string{"application/json; charset=utf-8"},
		},
		Body: body,
	}
}
