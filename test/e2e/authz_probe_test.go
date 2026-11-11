// authz_probe_test.go —— 🔒 路由匿名探针门禁（v6.3.0）：路由清单单源 = idl/*.thrift
// （正则解析 api.get/post/put/delete 注解），e2e 栈无 token 逐路由打，断言 401。
//
// 动机：v5.6.2 repo-map 曾因 hz update 重生成吞掉 authMw 致匿名失守（事后热修 +
// 部署后人工探针）。本测试把「IDL 声明的每个 🔒 路由都必须鉴权」钉成 CI 门禁：
// hz update 漏挂 authMw → 路由仍生成 → 本测试立即红。
//
// 白名单（无 Bearer 语义，设计背书）：
//
//	POST /webhook/:platform —— 平台签名校验在 service 层，不走 Bearer
package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var idlRouteRe = regexp.MustCompile(`api\.(get|post|put|delete)="([^"]+)"`)

// idlProtectedRoutes 解析 idl/*.thrift 全部路由注解，剔除白名单后返回
// method+path 展开清单（:param 段替换为探针值）。
func idlProtectedRoutes(t *testing.T) []struct {
	method, path string
} {
	t.Helper()
	entries, err := filepath.Glob("../../idl/*.thrift")
	if err != nil || len(entries) == 0 {
		t.Fatalf("解析 idl 路由清单失败: %v", err)
	}
	type route struct{ method, path string }
	var routes []route
	seen := map[string]bool{}
	for _, f := range entries {
		b, rerr := os.ReadFile(f)
		if rerr != nil {
			t.Fatalf("读取 %s: %v", f, rerr)
		}
		for _, m := range idlRouteRe.FindAllStringSubmatch(string(b), -1) {
			method := strings.ToUpper(m[1])
			p := m[2]
			if strings.HasPrefix(p, "/webhook") {
				continue // 白名单：平台签名面
			}
			key := method + " " + p
			if seen[key] {
				continue
			}
			seen[key] = true
			routes = append(routes, route{method, p})
		}
	}
	if len(routes) < 15 {
		t.Fatalf("IDL 路由解析过少（%d 条），疑似解析失败", len(routes))
	}
	out := make([]struct {
		method, path string
	}, len(routes))
	for i, r := range routes {
		out[i] = struct {
			method, path string
		}{r.method, r.path}
	}
	return out
}

// TestAuthzProbe 无 token 打全部 IDL 🔒 路由，断言 401（fail-closed）。
func TestAuthzProbe(t *testing.T) {
	s := newStack(t, stackOpts{})
	routes := idlProtectedRoutes(t)
	for _, r := range routes {
		path := r.path
		path = strings.ReplaceAll(path, ":taskId", "t-probe")
		path = strings.ReplaceAll(path, ":ref", "deadbeef")
		path = strings.ReplaceAll(path, ":platform", "gitea")
		path = strings.ReplaceAll(path, ":owner", "o")
		path = strings.ReplaceAll(path, ":repo", "r")
		url := s.baseURL + path
		req, err := http.NewRequest(r.method, url, strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("构造请求 %s: %v", r.method+" "+path, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("打 %s %s: %v", r.method, path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("🔒 路由匿名可访问: %s %s → %d（期望 401，鉴权中间件疑似漏挂）",
				r.method, r.path, resp.StatusCode)
		}
	}
	t.Logf("auth 探针覆盖 %d 条 IDL 路由，全部 401", len(routes))
}
