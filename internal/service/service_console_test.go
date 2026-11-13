package service

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/storage"
)

// TestQualityConsoleHTML 控制台页（#27 验收）：200 + 自包含 HTML +
// 四面聚合段（概览/消费/失败归类/修复有效率）+ 零外链纪律。
func TestQualityConsoleHTML(t *testing.T) {
	st := openVisStore(t)
	s := &Service{Cfg: &config.Config{}, Store: st}
	ctx := context.Background()

	// 造一行任务让概览/明细有数据
	task := storage.NewTask("code_review", "gitlab", "demo", "pay-gateway", "1")
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	page, code := s.QualityConsoleHTML(ctx)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	for _, want := range []string{
		"Argus 质量控制台",
		"审查概览",
		"Token 消费",
		"失败原因归类",
		"修复闭环有效率",
		"按仓明细",
		"零新表",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("页面缺段 %q", want)
		}
	}
	// 自包含纪律：零外链（http(s):// 引用即破单页承诺）
	if strings.Contains(page, "https://") || strings.Contains(page, "http://") {
		t.Error("页面含外链，违背自包含单页纪律")
	}
	// HTML 转义：仓名注入面
	if strings.Contains(page, "<script") {
		t.Error("页面含未预期 script 块")
	}
}

// TestQualityConsoleRowFilter 约束 token 行过滤（授权矩阵 gate=rows 对账）：
// 约束外仓库的行不出现在按仓明细。
func TestQualityConsoleRowFilter(t *testing.T) {
	st := openVisStore(t)
	s := &Service{Cfg: &config.Config{}, Store: st}
	ctx := context.Background()

	for _, r := range [][3]string{{"gitlab", "myorg", "app"}, {"gitlab", "other", "secret"}} {
		task := storage.NewTask("code_review", r[0], r[1], r[2], "1")
		if err := st.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}

	scoped := ContextWithTokenRepos(ctx, "gitlab:myorg/*")
	page, code := s.QualityConsoleHTML(scoped)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if strings.Contains(page, "secret") {
		t.Error("约束外仓库透出控制台明细")
	}
	if !strings.Contains(page, "app") {
		t.Error("约束内仓库缺失")
	}
}

// TestConsoleMetricsWhitelist 白名单单源（obs.ConsoleMetricEnabled）：透出面
// 与白名单一致——白名单外指标不进控制台（与 /metrics 全量面隔离）。
func TestConsoleMetricsWhitelist(t *testing.T) {
	for _, m := range obs.ConsoleMetrics() {
		if !obs.ConsoleMetricEnabled(m.Name) {
			t.Errorf("白名单外指标透出: %s", m.Name)
		}
	}
	// 白名单本身在册（修复有效率两枚是阶段 3 验收面）
	for _, name := range []string{"argus_fix_verify_total", "argus_fix_push_total"} {
		if !obs.ConsoleMetricEnabled(name) {
			t.Errorf("验收指标 %s 掉出白名单", name)
		}
	}
}

// TestQualityConsoleTrend 两态（#27 质量趋势验收）：基线文件在 → 总指标卡
// 与分语言表渲染；缺位 → 占位文案且不 500（趋势面缺位属常态）。
func TestQualityConsoleTrend(t *testing.T) {
	st := openVisStore(t)
	ctx := context.Background()

	t.Run("缺位占位", func(t *testing.T) {
		s := &Service{Cfg: &config.Config{EvalReportPath: t.TempDir() + "/nope.json"}, Store: st}
		page, code := s.QualityConsoleHTML(ctx)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if !strings.Contains(page, "质量趋势") {
			t.Error("趋势段标题缺失")
		}
		if !strings.Contains(page, "暂无基线") {
			t.Error("缺位占位文案缺失")
		}
	})

	t.Run("基线在", func(t *testing.T) {
		path := t.TempDir() + "/baseline.json"
		raw := `{"generated_at":"2026-11-13T18:53:37+08:00","model":"mimo-v2.6-pro",` +
			`"stability":0.952,"overall":{"fp_rate":0.727,"miss_rate":0.0,` +
			`"precision":0.805,"recall":1.0},` +
			`"by_language":[{"language":"go","fp_rate":1.0,"miss_rate":0.0}]}`
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		s := &Service{Cfg: &config.Config{EvalReportPath: path}, Store: st}
		page, code := s.QualityConsoleHTML(ctx)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		for _, want := range []string{
			"mimo-v2.6-pro", "72.7%", "100.0%", "95.2%", "分语言", "go",
		} {
			if !strings.Contains(page, want) {
				t.Errorf("趋势面缺 %q", want)
			}
		}
		if strings.Contains(page, "暂无基线") {
			t.Error("基线在时不应渲染缺位占位")
		}
	})
}
