package strategies

import (
	"os"
	"testing"
)

func TestDefaultEnvProxyURL(t *testing.T) {
	// 保存原环境变量
	origProxy := os.Getenv("PROXY_URL")
	origHTTPS := os.Getenv("HTTPS_PROXY")
	origHTTP := os.Getenv("HTTP_PROXY")
	defer func() {
		_ = os.Setenv("PROXY_URL", origProxy)
		_ = os.Setenv("HTTPS_PROXY", origHTTPS)
		_ = os.Setenv("HTTP_PROXY", origHTTP)
	}()

	_ = os.Unsetenv("PROXY_URL")
	_ = os.Unsetenv("HTTPS_PROXY")
	_ = os.Unsetenv("HTTP_PROXY")
	_ = os.Unsetenv("https_proxy")
	_ = os.Unsetenv("http_proxy")

	// 1. 无环境变量
	if got := DefaultEnvProxyURL(); got != "" {
		t.Errorf("expected empty proxy, got %q", got)
	}

	// 2. 只有 HTTP_PROXY
	_ = os.Setenv("HTTP_PROXY", "http://127.0.0.1:1080")
	if got := DefaultEnvProxyURL(); got != "http://127.0.0.1:1080" {
		t.Errorf("got %q, want http://127.0.0.1:1080", got)
	}

	// 3. 优先级：HTTPS_PROXY 高于 HTTP_PROXY
	_ = os.Setenv("HTTPS_PROXY", "http://127.0.0.1:1081")
	if got := DefaultEnvProxyURL(); got != "http://127.0.0.1:1081" {
		t.Errorf("got %q, want http://127.0.0.1:1081", got)
	}

	// 4. 优先级：PROXY_URL 最高
	_ = os.Setenv("PROXY_URL", "http://127.0.0.1:1082")
	if got := DefaultEnvProxyURL(); got != "http://127.0.0.1:1082" {
		t.Errorf("got %q, want http://127.0.0.1:1082", got)
	}
}
