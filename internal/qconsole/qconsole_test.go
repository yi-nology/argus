// Package qconsole_test 质量控制台聚合与输出形态回归（P3-02，#27）：
// httptest 桩起既有 API 面（/stats/overview · /usage/monthly · /metrics），
// 断言聚合口径、渲染双形态与「无新表只读」纪律（快照全部来自 API/文件）。
package qconsole_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/qconsole"
)

// stubArgus 既有聚合面桩（v6.9.0 双账本 + obs 计数器命名同款）。
func stubArgus(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/stats/overview", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer admin-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
		  "window_days": 30,
		  "repos": [
		    {"platform":"gitea","owner":"acme","repo":"pay","tasks_done":10,"approved":4,"commented":4,"rejected":2,"tokens":5200,"suppressed":7},
		    {"platform":"gitea","owner":"acme","repo":"web","tasks_done":6,"approved":5,"commented":1,"rejected":0,"tokens":1800,"suppressed":2}
		  ],
		  "tasks_done": 16,
		  "verdicts": {"approve":9,"comment":5,"request_changes":2},
		  "tokens_used": 7000,
		  "findings_suppressed": 9
		}`))
	})
	mux.HandleFunc("/usage/monthly", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
		  "month": "2026-11",
		  "total": 7000, "budget": 100000,
		  "real_total": 8600, "real_budget": 150000,
		  "repos": [{"platform":"gitea","owner":"acme","repo":"pay","tokens":5200}]
		}`))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer admin-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`# HELP argus_llm_failovers_total LLM 降级
# TYPE argus_llm_failovers_total counter
argus_llm_failovers_total{reason="timeout"} 3
argus_llm_failovers_total{reason="429"} 2
# TYPE argus_plugin_failures_total counter
argus_plugin_failures_total{plugin="sast"} 4
argus_plugin_failures_total{plugin="semsearch"} 1
# TYPE argus_plugin_skipped_total counter
argus_plugin_skipped_total{plugin="sast"} 2
# TYPE argus_budget_exceeded_total counter
argus_budget_exceeded_total{repo="acme/pay"} 1
`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestSnapshotAggregation 聚合口径：行/KPI/双账本/失败原因/插件表现。
func TestSnapshotAggregation(t *testing.T) {
	srv := stubArgus(t)
	src := &qconsole.HTTPSource{BaseURL: srv.URL, Token: "admin-token"}
	snap, err := src.Snapshot(context.Background(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Tasks != 16 || snap.Suppressed != 9 || snap.Tokens != 7000 {
		t.Fatalf("KPI: %+v", snap)
	}
	if len(snap.RepoRows) != 2 || snap.RepoRows[0].Suppressed != 7 {
		t.Fatalf("repo rows: %+v", snap.RepoRows)
	}
	if snap.Verdicts.Approve != 9 || snap.Verdicts.Comment != 5 || snap.Verdicts.RequestChanges != 2 {
		t.Fatalf("verdicts: %+v", snap.Verdicts)
	}
	if snap.Month != "2026-11" || snap.QuotaTotal != 7000 || snap.RealTotal != 8600 {
		t.Fatalf("双账本: %+v", snap)
	}
	// 失败原因归类：LLM 降级 3+2=5、插件失败 5、预算超限 1；零值计数不上屏
	wantReasons := map[string]uint64{"LLM 降级切换": 5, "插件失败": 5, "预算超限": 1}
	gotReasons := map[string]uint64{}
	for _, r := range snap.FailReasons {
		gotReasons[r.Name] = r.Count
	}
	for name, want := range wantReasons {
		if gotReasons[name] != want {
			t.Errorf("失败原因 %s = %d, want %d", name, gotReasons[name], want)
		}
	}
	// 插件表现：sast 4+2=6、semsearch 1
	gotPlugins := map[string]uint64{}
	for _, r := range snap.PluginRows {
		gotPlugins[r.Name] = r.Count
	}
	if gotPlugins["sast"] != 6 || gotPlugins["semsearch"] != 1 {
		t.Fatalf("插件表现: %+v", snap.PluginRows)
	}
}

// TestMetricsMissingTolerated /metrics 缺位（403/404）不构成控制台不可用。
func TestMetricsMissingTolerated(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/stats/overview", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"repos":[],"tasks_done":0,"verdicts":{},"tokens_used":0,"findings_suppressed":0}`))
	})
	mux.HandleFunc("/usage/monthly", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"month":"2026-11","total":0,"budget":0,"real_total":0,"real_budget":0,"repos":[]}`))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	snap, err := (&qconsole.HTTPSource{BaseURL: srv.URL, Token: "x"}).Snapshot(context.Background(), 30)
	if err != nil {
		t.Fatalf("metrics 缺位应容忍: %v", err)
	}
	if len(snap.FailReasons) != 0 || len(snap.PluginRows) != 0 {
		t.Fatalf("缺位面应为空: %+v", snap)
	}
}

// TestRenderForms 双形态：HTML 自包含（无外链、含各区块）、CLI 文本含 KPI。
func TestRenderForms(t *testing.T) {
	srv := stubArgus(t)
	snap, err := (&qconsole.HTTPSource{BaseURL: srv.URL, Token: "admin-token"}).Snapshot(context.Background(), 30)
	if err != nil {
		t.Fatal(err)
	}
	snap.EvalTrend = &qconsole.EvalTrend{
		Model: "mimo-v2", Stability: 0.93,
		Overall:    qconsole.EvalTrendMets{FP: 0.12, Miss: 0.08, P: 0.85, R: 0.91, CFP: 0.05, Unlabeled: 2},
		ByLanguage: []qconsole.EvalLangMets{{Language: "go", FP: 0.10, Miss: 0.06, CFP: 0.04}},
	}

	html, err := qconsole.RenderHTML(snap)
	if err != nil {
		t.Fatal(err)
	}
	h := string(html)
	if strings.Contains(h, "<script src") || strings.Contains(h, "<link") {
		t.Error("静态页应自包含（无外部 script/link）")
	}
	for _, want := range []string{"误报反馈", "插件表现", "失败原因归类", "Token 消费", "质量趋势", "无新表", "mimo-v2",
		"干净误报率", "Precision(受限)"} {
		if !strings.Contains(h, want) {
			t.Errorf("静态页缺区块: %q", want)
		}
	}

	txt := qconsole.RenderText(snap)
	for _, want := range []string{"任务 16", "误报抑制 9", "LLM 降级切换", "sast", "稳定性 0.930"} {
		if !strings.Contains(txt, want) {
			t.Errorf("CLI 文本缺: %q", want)
		}
	}
}

// TestLoadEvalTrendTolerant 评测基线文件：缺位 → nil（趋势面缺位常态）；
// 存在但字段残缺 → 容错解析零值安全。
func TestLoadEvalTrendTolerant(t *testing.T) {
	if got := qconsole.LoadEvalTrend(filepath.Join(t.TempDir(), "nope.json")); got != nil {
		t.Fatalf("缺文件应 nil: %+v", got)
	}
	path := filepath.Join(t.TempDir(), "baseline-latest.json")
	if err := os.WriteFile(path, []byte(`{"model":"m","overall":{"fp_rate":0.1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	trend := qconsole.LoadEvalTrend(path)
	if trend == nil || trend.Model != "m" || trend.Overall.FP != 0.1 {
		t.Fatalf("残缺报告应容错: %+v", trend)
	}
}

// TestLoadEvalTrendNestedByLanguage 分语言嵌套解码回归（终审 Important）：
// 生产者 by_language 是 [{language, metrics:{fp_rate,miss_rate,...}}] 嵌套形态
// （evalq.LanguageReport），扁平解码会把每个分语言指标零填——/console 分语言
// 行恒渲 0.0%。断言只有嵌套解码才能解出的非零值。
func TestLoadEvalTrendNestedByLanguage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline-latest.json")
	raw := `{"generated_at":"2026-11-14T00:00:00Z","model":"m","stability":0.9,` +
		`"overall":{"fp_rate":0.1,"miss_rate":0.0,"precision":0.9,"recall":1.0,"clean_case_fp_rate":0.05,"unlabeled_count":0},` +
		`"by_language":[{"language":"go","metrics":{"fp_rate":0.25,"miss_rate":0.125,` +
		`"precision":0.9,"recall":1.0,"clean_case_fp_rate":0.5,"unlabeled_count":2}}]}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	trend := qconsole.LoadEvalTrend(path)
	if trend == nil {
		t.Fatal("嵌套形态应解析成功（返回 nil=整体解码失败）")
	}
	if len(trend.ByLanguage) != 1 {
		t.Fatalf("分语言应 1 行: n=%d", len(trend.ByLanguage))
	}
	lang := trend.ByLanguage[0]
	if lang.Language != "go" || lang.Miss != 0.125 || lang.FP != 0.25 || lang.CFP != 0.5 {
		t.Fatalf("嵌套 metrics 应解出非零值（全零=扁平投影回归）: %+v", lang)
	}
}

// TestReadOnlyDiscipline 只读纪律：Snapshot 全程只发 GET。
func TestReadOnlyDiscipline(t *testing.T) {
	methods := map[string]int{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		methods[r.Method]++
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_, _ = (&qconsole.HTTPSource{BaseURL: srv.URL, Token: "x"}).Snapshot(context.Background(), 7)
	if methods["GET"] == 0 {
		t.Fatal("应有 GET 请求")
	}
	for m, n := range methods {
		if m != http.MethodGet {
			t.Errorf("只读纪律被破坏: %s x%d", m, n)
		}
	}
}
