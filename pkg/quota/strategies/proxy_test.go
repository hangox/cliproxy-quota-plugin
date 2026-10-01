package strategies

import "testing"

func TestDefaultEnvProxyURL(t *testing.T) {
	for _, key := range []string{"PROXY_URL", "HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"} {
		t.Setenv(key, "")
	}

	// 1. 无环境变量
	if got := DefaultEnvProxyURL(); got != "" {
		t.Errorf("expected empty proxy, got %q", got)
	}

	// 2. 只有 HTTP_PROXY
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1080")
	if got := DefaultEnvProxyURL(); got != "http://127.0.0.1:1080" {
		t.Errorf("got %q, want http://127.0.0.1:1080", got)
	}

	// 3. 优先级：HTTPS_PROXY 高于 HTTP_PROXY
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1081")
	if got := DefaultEnvProxyURL(); got != "http://127.0.0.1:1081" {
		t.Errorf("got %q, want http://127.0.0.1:1081", got)
	}

	// 4. 优先级：PROXY_URL 最高
	t.Setenv("PROXY_URL", "http://127.0.0.1:1082")
	if got := DefaultEnvProxyURL(); got != "http://127.0.0.1:1082" {
		t.Errorf("got %q, want http://127.0.0.1:1082", got)
	}
}
