package e2e

import (
	"strings"
	"testing"
)

// 场景28：G5 健康报告查询面（v3.13.0）——GET /reports/health Bearer 门控 +
// 飞轮状态 JSON（与周期 IM 推送同数据源）。
func TestScenario28_HealthReportEndpoint(t *testing.T) {
	s := newStack(t, stackOpts{regretLearning: true})

	// 未带 token → 拒绝（fail-closed，与 /tasks 同策略）
	rec := performRequest(s.engine, "GET", "/reports/health", "", nil)
	if rec.Code == 200 {
		t.Fatalf("未鉴权应拒绝，got %d", rec.Code)
	}

	// 带 token → 200 + 信封（code/data）
	auth := []utHeader{{Key: "Authorization", Value: "Bearer e2e-token"}}
	rec = performRequest(s.engine, "GET", "/reports/health", "", auth)
	if rec.Code != 200 {
		t.Fatalf("带 token 应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"code":0`) || !strings.Contains(body, `"data"`) {
		t.Fatalf("响应缺标准信封: %s", body)
	}
	// 学习环开启：空库 → Empty=true 的空报告（不是错误）
	if !strings.Contains(body, `"empty":true`) {
		t.Fatalf("空库应返回空报告: %s", body)
	}
}
