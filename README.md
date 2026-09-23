# CLIProxy Quota Plugin (`cliproxy-quota-plugin`)

`cliproxy-quota-plugin` 是基于 CLIProxyAPI 官方 C-ABI 插件规范开发的多账号配额加权池化动态库插件。它完全解耦于网关核心代码，通过标准共享库（`.so` / `.dylib`）实现即插即用。

---

## 核心特性

1. **零侵入 C-ABI 架构**：
   - 严格遵循 CLIProxyAPI C-ABI 标准（导出了 `cliproxy_plugin_init`、`cliproxyPluginCall`、`cliproxyPluginFree`、`cliproxyPluginShutdown`）；
   - 网关主干无需打补丁或重编译，只需在 `plugins` 目录中放置对应动态库文件。
2. **Management API 端点**：
   - 声明 `ManagementAPI: true` 能力；
   - 暴露 `/status` 资源端点，宿主启动后可直接通过 `GET /v0/resource/plugins/cliproxy-quota-plugin/status` 访问；
   - 支持返回清晰的纯 JSON 格式聚合指标，可传参 `?provider=antigravity` 或 `?provider=codex` 过滤单提供方。
3. **加权池化与时间对齐引擎**：
   - **动态加权平均**：依据账号配置的有效权重（`weight`）按比例融合各账号的剩余百分比；
   - **最早重置时间对齐**：提取所有参与池化账号的最早 `reset_at` 时间点，精确反映窗口刷新时刻；
   - **重置卡累加**：聚合所有活跃账号的重置卡（`resetCards`）；
   - **健康度与禁用过滤**：自动剔除已禁用（`disabled: true` 或状态为 `disabled`/`error`）或权重 `<= 0` 的异常账号。
4. **内置多提供方采集策略**：
   - **Antigravity 策略**：调用 `https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary`，使用标准客户端 User-Agent，将配额精准划分为 `gemini` 和 `claude_gpt` 两大分组及 `5h`/`7d` 窗口；
   - **Codex 策略**：调用 `https://chatgpt.com/backend-api/wham/usage`，支持多源凭据（`access_token`/`api_key`、`account_id`）与出站代理配置，解析主次窗口与重置卡。

---

## 目录结构

```text
.
├── Makefile                # 本地构建、Docker Linux 交叉编译与单测命令
├── README.md               # 项目工程与使用说明文档
├── go.mod                  # Go 模块定义，依赖 CLIProxyAPI/v7
├── main.go                 # C-ABI 导出符号层（cgo）
├── main_nocgo.go           # 纯 Go 构建桩（支持 CGO_ENABLED=0 编译与测试）
└── pkg/
    ├── plugin/             # 插件注册与 Management API 路由处理
    │   ├── plugin.go
    │   └── plugin_test.go
    └── quota/              # 配额与加权池化引擎核心
        ├── engine.go       # 加权平均计算、窗口聚合、账号过滤管道
        ├── engine_test.go  # 加权池化、时间对齐与异常降级单测
        ├── host_auth.go    # 宿主 host.auth.list/get/get_runtime 回调封装
        ├── host_auth_test.go
        ├── types.go        # 通用数据结构与 ProviderStrategy 接口定义
        └── strategies/     # 针对各模型的具体配额采集策略
            ├── antigravity.go
            ├── antigravity_test.go
            ├── codex.go
            └── codex_test.go
```

---

## 构建与测试

### 1. 运行单元测试
工程核心库采用纯 Go 设计，支持完全脱离 CGO 环境运行测试：
```bash
make test
# 或直接运行
CGO_ENABLED=0 go test -v -count=1 ./...
```

### 2. 本地编译动态库
```bash
make build
```
- macOS 产物：`cliproxy-quota-plugin.dylib`
- Linux 产物：`cliproxy-quota-plugin.so`
- Windows 产物：`cliproxy-quota-plugin.dll`

### 3. Docker 交叉编译 Linux amd64 动态库
在 macOS/Windows 上直接为 Linux 服务器编译 `.so`：
```bash
make build-linux-amd64
```
产物为：`cliproxy-quota-plugin-linux-amd64.so`。

---

## 宿主配置与启用

将编译出的动态库文件复制到 CLIProxyAPI 的插件目录（例如 `plugins/`），并在网关配置 `config.yaml` 中启用：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cliproxy-quota-plugin:
      enabled: true
      priority: 1
```

启动 CLIProxyAPI 宿主后，访问端点：
```bash
# 获取所有 Provider 聚合配额
curl http://localhost:8080/v0/resource/plugins/cliproxy-quota-plugin/status

# 单独获取 Antigravity 配额
curl http://localhost:8080/v0/resource/plugins/cliproxy-quota-plugin/status?provider=antigravity
```

---

## 响应数据格式示例

```json
{
  "updated_at": "2026-09-23T18:30:00Z",
  "providers": {
    "antigravity": {
      "provider": "antigravity",
      "total_accounts": 4,
      "active_accounts": 3,
      "windows": [
        {
          "name": "5h",
          "remaining_percentage": 88.5,
          "available": true,
          "resets_at": "2026-09-23T20:15:00Z"
        },
        {
          "name": "7d",
          "remaining_percentage": 64.2,
          "available": true,
          "resets_at": "2026-09-28T08:00:00Z"
        }
      ],
      "groups": {
        "gemini": {
          "windows": [
            {
              "name": "5h",
              "remaining_percentage": 92.0,
              "available": true,
              "resets_at": "2026-09-23T20:15:00Z"
            }
          ]
        },
        "claude_gpt": {
          "windows": [
            {
              "name": "5h",
              "remaining_percentage": 85.0,
              "available": true,
              "resets_at": "2026-09-23T21:00:00Z"
            }
          ]
        }
      }
    },
    "codex": {
      "provider": "codex",
      "total_accounts": 2,
      "active_accounts": 2,
      "windows": [
        {
          "name": "5h",
          "remaining_percentage": 90.0,
          "available": true
        },
        {
          "name": "7d",
          "remaining_percentage": 75.5,
          "available": true,
          "resets_at": "2026-09-25T14:30:00Z"
        }
      ],
      "reset_cards": 5
    }
  }
}
```
