package ocrshim

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestAuthGate 共享密钥门：配置 AuthToken 后 /mcp 无/错 token 一律 401，
// 对 token 放行到 MCP 层；/healthz 刻意留在门外（探针不携带凭证）。
func TestAuthGate(t *testing.T) {
	probe := filepath.Join(t.TempDir(), "ocr") // healthz 只 stat 该路径
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{OcrBin: probe, AuthToken: "s3cret"}
	ts := httptest.NewServer(Handler(cfg))
	t.Cleanup(ts.Close)

	get := func(path, auth string) int {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := get("/mcp", ""); code != http.StatusUnauthorized {
		t.Fatalf("无 token 应 401，got %d", code)
	}
	if code := get("/mcp", "Bearer wrong"); code != http.StatusUnauthorized {
		t.Fatalf("错误 token 应 401，got %d", code)
	}
	if code := get("/mcp", "Bearer s3cret"); code == http.StatusUnauthorized {
		t.Fatal("正确 token 不应 401（应放行到 MCP 层）")
	}
	if code := get("/healthz", ""); code != http.StatusOK {
		t.Fatalf("healthz 不应被鉴权拦截，got %d", code)
	}
}

// 未配置 AuthToken → 行为与旧版完全一致（/mcp 不设防）。
func TestAuthDisabledByDefault(t *testing.T) {
	probe := filepath.Join(t.TempDir(), "ocr")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{OcrBin: probe}
	ts := httptest.NewServer(Handler(cfg))
	t.Cleanup(ts.Close)
	resp, err := http.Get(ts.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatal("未配置 token 不应启用鉴权")
	}
}
