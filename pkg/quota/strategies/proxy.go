package strategies

import (
	"os"
	"strings"
)

// DefaultEnvProxyURL 检查环境变量获取出站代理地址（优先 PROXY_URL，其次 HTTPS_PROXY，再次 HTTP_PROXY）。
func DefaultEnvProxyURL() string {
	if p := strings.TrimSpace(os.Getenv("PROXY_URL")); p != "" {
		return p
	}
	if p := strings.TrimSpace(os.Getenv("HTTPS_PROXY")); p != "" {
		return p
	}
	if p := strings.TrimSpace(os.Getenv("HTTP_PROXY")); p != "" {
		return p
	}
	if p := strings.TrimSpace(os.Getenv("https_proxy")); p != "" {
		return p
	}
	if p := strings.TrimSpace(os.Getenv("http_proxy")); p != "" {
		return p
	}
	return ""
}
