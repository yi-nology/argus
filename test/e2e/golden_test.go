package e2e

// golden 评测集驱动（v3.10.0，G1a）：eval/golden/*.yaml 固化的 golden PR 夹具
// ——diff + 需求文本 + 期望 verdict/findings，逐条跑完整 runner→R1→R5 链路。
//
// 两种模式：
//   - 打桩回归（默认）：R1/R3/reviewer 用夹具 canned 响应——固化的是编排语义
//     （R5 真值表 / 合并去重 / 反注入 / 证据门），CI 必跑；
//   - 真实模型（ARGUS_EVAL=1 + OPENAI_*）：同一批夹具换真实模型出 R1/R3/reviewer，
//     比 verdict 命中率——这是模型 A/B（G1b，mimo vs 基线）的固定 harness。
//     模型方差大，真实模式只比 verdict，不比报告内容。
//
// 指标口径（README「审查域评测」）：verdict 一致率 = 命中期望 verdict 的用例数 / 总数。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/yi-nology/git-platform-sdk/provider"
	"gopkg.in/yaml.v3"

	"argus/internal/config"
	"argus/internal/plugin"
)

const goldenDir = "../../eval/golden"
const goldenResultsDir = "../../eval/results"

// goldenCaseResult 单用例评测结果（latest.json 数据源）。
type goldenCaseResult struct {
	Name     string   `json:"name"`
	Scenario []string `json:"scenario"`
	Expected string   `json:"expected"`
	Actual   string   `json:"actual"`
	Pass     bool     `json:"pass"`
	// Findings 合并后意见数（v3.15.0 P1-6 北极星口径）：clean 夹具上的
	// findings 全部计为误报（FP），从 result_json 读取。
	Findings int `json:"findings"`
}

// goldenStats 聚合口径（v3.15.0 P1-6）：verdict 命中率之外的「误报/覆盖」补充面。
//   - clean 夹具（scenario 含 "clean"）：期望零意见、verdict=approve；findings
//     总数即误报数（false_positives），approve 未命中即 FP 现形。
//   - boundary 夹具（scenario 含 "boundary"）：comment/rc 方向标定（30 号钉住
//     comment 不上移、31 号钉住 rc 不下移）。
type goldenStats struct {
	CleanTotal     int `json:"clean_total"`
	CleanApprove   int `json:"clean_approve"`
	FalsePositives int `json:"false_positives"`
	BoundaryTotal  int `json:"boundary_total"`
	BoundaryHits   int `json:"boundary_hits"`
}

type goldenPR struct {
	Title  string   `yaml:"title"`
	Body   string   `yaml:"body"`
	Labels []string `yaml:"labels"`
}

type goldenFile struct {
	Path      string `yaml:"path"`
	Diff      string `yaml:"diff"`
	Additions int    `yaml:"additions"`
	Deletions int    `yaml:"deletions"`
	// IsBinary/IsRenamed/IsDeleted 变更形态标记（v3.21.0 扩容：二进制/重命名/
	// 删除场景夹具直接驱动 diffprep 元信息路径）。
	IsBinary  bool `yaml:"is_binary"`
	IsRenamed bool `yaml:"is_renamed"`
	IsDeleted bool `yaml:"is_deleted"`
}

type goldenStub struct {
	R1       string `yaml:"r1"`       // R1 需求解析响应（RequirementSpec JSON）
	R3       string `yaml:"r3"`       // R3 完成度判定响应（ComplianceReport JSON）
	Reviewer string `yaml:"reviewer"` // correctness reviewer 响应（findings JSON）
}

type goldenExpect struct {
	Verdict     string   `yaml:"verdict"`       // approve | comment | request_changes（打桩模式精确比对）
	MustFind    []string `yaml:"must_find"`     // task.Report 必含子串（仅打桩模式判分）
	MustNotFind []string `yaml:"must_not_find"` // task.Report 必不含子串（仅打桩模式判分）
	// RealVerdicts 真实模型模式的期望集合（可选；空 = 沿用 Verdict 精确比对）。
	// v3 起集合按「从严合法性」逐夹具建模（各 YAML 注释给出理由）：
	//   - comment 期望夹具：approve 入集合 = 无不阻塞问题（诚实放宽）；rc 入集合
	//     仅限夹具前提真实支撑从严的形态（如 toy 实现可辩 partial）；注入类夹具
	//     approve 永不入集合（与「注入误升 APPROVE 一票否决」同口径）。
	//   - request_changes 期望夹具：仍精确比对（漏升级是危险方向，不容方差）。
	RealVerdicts []string `yaml:"real_verdicts"`
}

type goldenCase struct {
	Name          string              `yaml:"name"`
	Scenario      []string            `yaml:"scenario"` // partial|complete|injection|scope-creep|unverifiable|multilingual|merge|scope-gate|evidence-gate|guard|render|repo-config|best-of-n|meta-declaration|change-shape|verdict-priority|edge|i18n|dependency
	Kind          int                 `yaml:"kind"`     // 0=requirement_compliance 1=code_review
	PR            goldenPR            `yaml:"pr"`
	ChangedFiles  []goldenFile        `yaml:"changed_files"`
	RepoFiles     map[string]string   `yaml:"repo_files"`
	Issues        map[string]string   `yaml:"issues"` // number → 正文
	StripDefaults bool                `yaml:"strip_defaults"`
	ExtraAgents   []plugin.AgentDecl  `yaml:"extra_agents"`
	PersonaSeq    map[string][]string `yaml:"persona_seq"` // 额外 reviewer 的逐采样响应
	// SkipReal 真实模式跳过：夹具语义依赖打桩前提（canned persona/非诚实 R3），
	// 真实模型不会复现该前提（如「R3 被注入诱骗全标 implemented」）。
	SkipReal bool `yaml:"skip_real"`
	// G4 完备性裁判（空 = annotate 默认）
	CompletenessPolicy string       `yaml:"completeness_policy"`
	CompletenessRetry  bool         `yaml:"completeness_retry"`
	Stub               goldenStub   `yaml:"stub"`
	Expect             goldenExpect `yaml:"expect"`
}

func loadGoldenCases(t TestingT) []goldenCase {
	ents, err := os.ReadDir(goldenDir)
	if err != nil {
		t.Fatalf("读 golden 目录失败: %v", err)
	}
	var cases []goldenCase
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(goldenDir, name))
		if err != nil {
			t.Fatalf("读 %s: %v", name, err)
		}
		var c goldenCase
		if err := yaml.Unmarshal(data, &c); err != nil {
			t.Fatalf("解析 %s: %v", name, err)
		}
		if c.Name == "" {
			c.Name = strings.TrimSuffix(name, filepath.Ext(name))
		}
		cases = append(cases, c)
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].Name < cases[j].Name })
	return cases
}

// TestGoldenReviewEval golden PR 评测集：逐夹具跑完整链路并比对期望。
// make review-eval（打桩） / ARGUS_EVAL=1 make review-eval-real（真实模型 A/B）。
func TestGoldenReviewEval(t *testing.T) {
	realMode := os.Getenv("ARGUS_EVAL") == "1"
	if realMode {
		config.LoadDotEnv()
		if os.Getenv("OPENAI_BASE_URL") == "" || os.Getenv("OPENAI_API_KEY") == "" || os.Getenv("OPENAI_MODEL") == "" {
			t.Skip("ARGUS_EVAL=1 需要 OPENAI_BASE_URL / OPENAI_API_KEY / OPENAI_MODEL")
		}
	}
	cases := loadGoldenCases(t)
	if len(cases) < 20 {
		t.Fatalf("golden 集不足 20 条（当前 %d）——评测集应只增不删", len(cases))
	}

	type caseResult = goldenCaseResult
	var results []caseResult

	for _, c := range cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			if realMode && c.SkipReal {
				t.Skip("夹具语义依赖打桩前提（canned persona/非诚实 R3），真实模式不计分")
			}
			opts := stackOpts{}
			if realMode {
				opts.realLLM = true
			} else {
				opts.r1 = orStubDefault(c.Stub.R1, `{"source":"PR 描述","acceptance_criteria":[{"id":"AC-1","text":"完成变更所述功能"}],"ambiguous":[]}`)
				opts.r3 = orStubDefault(c.Stub.R3, `{"checks":[{"id":"AC-1","status":"implemented","evidence":"main.go:3"}],"scope_creep":[],"summary":"完成"}`)
				opts.correctness = orStubDefault(c.Stub.Reviewer, `{"findings":[],"notes":""}`)
			}
			for _, f := range c.ChangedFiles {
				opts.diffFiles = append(opts.diffFiles, provider.ChangedFile{
					NewPath: f.Path, Diff: f.Diff, Additions: f.Additions, Deletions: f.Deletions,
					IsBinary: f.IsBinary, IsRenamed: f.IsRenamed, IsDeleted: f.IsDeleted,
				})
			}
			opts.repoFiles = c.RepoFiles
			opts.prTitle = c.PR.Title
			opts.prBody = c.PR.Body
			opts.prLabels = c.PR.Labels
			for number, body := range c.Issues {
				if opts.issues == nil {
					opts.issues = map[string]*provider.Issue{}
				}
				opts.issues[number] = &provider.Issue{
					Number: number, Title: "需求 " + number, Body: body,
					State: provider.IssueStateOpen, CreatedAt: time.Now(),
				}
			}
			opts.stripDefaults = c.StripDefaults
			opts.extraAgents = c.ExtraAgents
			opts.personaSeq = c.PersonaSeq
			opts.completenessPolicy = c.CompletenessPolicy
			opts.completenessRetry = c.CompletenessRetry

			s := newStack(t, opts)
			taskID := s.createTaskViaAPI(t, c.Kind)
			if err := s.runner.Execute(context.Background(), taskID); err != nil {
				t.Fatal(err)
			}
			task := s.waitForStatus(t, taskID, "done")

			// 期望语义：打桩=精确比对 + 报告子串；真实=RealVerdicts 集合命中
			//（v3 起集合按「从严合法性」逐夹具建模，见 goldenExpect.RealVerdicts 注）
			pass := task.Verdict == c.Expect.Verdict
			if realMode && len(c.Expect.RealVerdicts) > 0 {
				pass = false
				for _, v := range c.Expect.RealVerdicts {
					if task.Verdict == v {
						pass = true
					}
				}
			}
			if pass && !realMode {
				for _, want := range c.Expect.MustFind {
					if !strings.Contains(task.Report, want) {
						pass = false
						t.Errorf("报告缺 %q\n报告（截断）:\n%s", want, truncReport(task.Report))
					}
				}
				for _, banned := range c.Expect.MustNotFind {
					if strings.Contains(task.Report, banned) {
						pass = false
						t.Errorf("报告不应含 %q", banned)
					}
				}
			} else if !pass {
				want := c.Expect.Verdict
				if realMode && len(c.Expect.RealVerdicts) > 0 {
					want = strings.Join(c.Expect.RealVerdicts, "|")
				}
				t.Errorf("verdict=%s，want %s\n报告（截断）:\n%s", task.Verdict, want, truncReport(task.Report))
			}
			results = append(results, caseResult{
				Name: c.Name, Scenario: c.Scenario,
				Expected: c.Expect.Verdict, Actual: task.Verdict, Pass: pass,
				Findings: resultFindingsCount(task.ResultJSON),
			})
			// 逐用例增量落盘：真实模型单条 ~2 分钟，中途超时/中断也保留已完成数据
			//（v3.23.0：增量快照同样带聚合口径——全量跑被 timeout 硬杀时
			// latest.json 的 metrics 不再是空壳）
			if realMode {
				writeGoldenResults(t, results, -1, aggregateGoldenStats(results))
			}
		})
	}

	// 汇总：打桩模式打表；真实模式额外落 eval/results/latest.json（A/B 对比数据源）
	passed := 0
	for _, r := range results {
		mark := "✅"
		if !r.Pass {
			mark = "❌"
		}
		t.Logf("%s %-42s 期望=%-16s 实际=%s", mark, r.Name, r.Expected, r.Actual)
		if r.Pass {
			passed++
		}
	}
	stats := aggregateGoldenStats(results)
	t.Logf("golden 评测：%d/%d 命中（模式=%s）；clean=%d/%d approve、FP=%d、boundary=%d/%d",
		passed, len(results), map[bool]string{true: "real", false: "stub"}[realMode],
		stats.CleanApprove, stats.CleanTotal, stats.FalsePositives,
		stats.BoundaryHits, stats.BoundaryTotal)
	if realMode {
		writeGoldenResults(t, results, passed, stats)
	}
}

// aggregateGoldenStats 聚合 clean/boundary 补充口径（纯函数，增量落盘同样可用）。
func aggregateGoldenStats(results []goldenCaseResult) goldenStats {
	var s goldenStats
	for _, r := range results {
		isClean, isBoundary := false, false
		for _, sc := range r.Scenario {
			if sc == "clean" {
				isClean = true
			}
			if sc == "boundary" {
				isBoundary = true
			}
		}
		if isClean {
			s.CleanTotal++
			s.FalsePositives += r.Findings
			if r.Actual == "approve" {
				s.CleanApprove++
			}
		}
		if isBoundary {
			s.BoundaryTotal++
			if r.Pass {
				s.BoundaryHits++
			}
		}
	}
	return s
}

// resultFindingsCount 从 result_json 提取合并后意见数（解析失败返回 0——
// 失败任务的 findings 本就不可信，不参与 FP 计数）。
func resultFindingsCount(resultJSON string) int {
	if resultJSON == "" {
		return 0
	}
	var p struct {
		Findings []json.RawMessage `json:"findings"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &p); err != nil {
		return 0
	}
	return len(p.Findings)
}

// maxReportErrRunes 失败信息携带的报告截断上限（归因够用、不刷爆测试日志）。
const maxReportErrRunes = 1500

// truncReport 失败时附报告片段（AC 判定/意见列表是 verdict miss 的归因依据）。
func truncReport(report string) string {
	if report == "" {
		return "（无报告）"
	}
	r := []rune(report)
	if len(r) <= maxReportErrRunes {
		return report
	}
	return string(r[:maxReportErrRunes]) + "…"
}

func orStubDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// writeGoldenResults 真实模型评测结果落 JSON（回归看板/A/B 对照的数据源）。
// passed<0 = 运行中增量快照（不记 pass_rate 终值）。
func writeGoldenResults(t *testing.T, results []goldenCaseResult, passed int, stats goldenStats) {
	t.Helper()
	out := map[string]any{
		"generated_at": time.Now().Format(time.RFC3339),
		"model":        os.Getenv("OPENAI_MODEL"),
		// G1b A/B 标注：阶段路由覆盖参与对比实验时记录进结果（空 = 全局模型）
		"stage_models": map[string]string{
			"r3":       os.Getenv("ARGUS_MODEL_R3"),
			"reviewer": os.Getenv("ARGUS_REVIEWER_MODEL"),
			"r35":      os.Getenv("ARGUS_MODEL_R35"),
		},
		"total": len(results),
		"cases": results,
		"done":  passed >= 0,
		// v3.15.0 P1-6 北极星补充口径：clean 误报面 + boundary 标定
		"metrics": stats,
	}
	if passed >= 0 {
		out["passed"] = passed
		out["pass_rate"] = float64(passed) / float64(len(results))
	}
	if err := os.MkdirAll(goldenResultsDir, 0o755); err != nil {
		t.Logf("结果目录创建失败: %v", err)
		return
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	path := filepath.Join(goldenResultsDir, "latest.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Logf("结果写入失败: %v", err)
		return
	}
	// G6 回归看板（轻量版）：同步渲染人读 markdown 表——JSON 供工具比对，
	// md 供归档/评审；跨期趋势由 eval/EXPERIMENTS.md 登记表承接
	_ = os.WriteFile(filepath.Join(goldenResultsDir, "latest.md"), []byte(renderResultsMarkdown(out)), 0o644)
	t.Logf("评测结果已写入 %s", path)
}

func renderResultsMarkdown(out map[string]any) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Golden 评测结果（%s）\n\n", out["generated_at"])
	fmt.Fprintf(&b, "- 模型：%s　阶段覆盖：%s\n", out["model"], out["stage_models"])
	if pr, ok := out["pass_rate"].(float64); ok {
		fmt.Fprintf(&b, "- 命中率：%.0f%%（%v/%v）\n\n", pr*100, out["passed"], out["total"])
	} else {
		b.WriteString("- 状态：运行中（增量快照）\n\n")
	}
	b.WriteString("| 用例 | 场景 | 期望 | 实际 | 结果 |\n|---|---|---|---|---|\n")
	cases, _ := out["cases"].([]goldenCaseResult)
	for _, c := range cases {
		mark := "✅"
		if !c.Pass {
			mark = "❌"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
			c.Name, strings.Join(c.Scenario, ","), c.Expected, c.Actual, mark)
	}
	if m, ok := out["metrics"].(goldenStats); ok {
		fmt.Fprintf(&b, "\n补充口径：clean %d/%d approve、误报（FP）%d 条、boundary 标定 %d/%d。\n",
			m.CleanApprove, m.CleanTotal, m.FalsePositives, m.BoundaryHits, m.BoundaryTotal)
	}
	return b.String()
}
