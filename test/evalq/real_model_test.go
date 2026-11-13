package evalq_test

// 真实模型评测（P2-02，#25 交付 2）：单发审查 Reviewer + 跑批入口
// TestEvalBaselineRealModel（make eval-baseline-real）。
//
// 门禁照抄 golden eval（test/e2e TestGoldenReviewEval 的 realLLM 分支）：
// ARGUS_EVAL=1 且 OPENAI_BASE_URL/OPENAI_API_KEY/OPENAI_MODEL 齐全才跑
// （config.LoadDotEnv 载入 lacus/.env），否则 Skip——无网/未开环境正确跳过，
// 打桩回归（make eval-baseline）不受影响。
//
// 装配走 test 侧轻装配（openai.NewChatModel，与生产 bootstrap BuildModels 同一
// 构造器），不引 bootstrap 全家桶——评测旁路形态与 obs evalmetrics 一致。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/schema"
	"github.com/oekit/pkg/jsonrepair"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/evalq"
	"github.com/yi-nology/argus/internal/plugin"
)

// reviewSystemPrompt 单发审查协议：输入样例 title+diff，输出结构化 findings
// JSON。kind 词表与 eval/baseline 金标准同风格（kebab-case 类别词）——起步集
// 按闭集词表计量（开集召回随 P3-03 #28 扩容）；verdict 口径与 golden eval 的
// R5 真值表同向（high→request_changes、仅中低→comment、无意见→approve）。
const reviewSystemPrompt = `你是代码审查专家，对给定的 PR 标题与 diff 做一次独立审查（只依据 diff，不臆造文件）。

只输出一个 JSON 对象（不要 markdown 围栏、不要解释文字），形态：
{"verdict":"approve|comment|request_changes","findings":[{"file":"相对路径","line":12,"kind":"kebab-case 类别词","severity":"high|medium|low","comment":"中文一句话"}],"notes":""}

约定：
- kind 必须从下列闭集词表中选取**最贴切的一个**（协议统一标签，不要自造同义词）：
  bare-except、cmd-inject、concurrent-modification、data-race、equals-hashcode、
  error-swallow、exception-swallow、hardcoded-secret、int-overflow、
  loose-eq、missing-doc-comment、missing-tx、mutable-default、mutex-copy、none-deref、
  npe、path-traversal、prototype-pollution、resource-leak、serial-await、sql-inject、
  thread-safety、toc-tou、unawaited-promise、unsafe-yaml、xss、
  naming-convention、style-nit、unchecked-err、nil-deref、race-condition；
- severity 只取 high|medium|low；
- verdict：存在 high 意见 → request_changes；仅 medium/low 意见 → comment；无意见 → approve；
- 没有问题时 findings 输出 []；diff 中未出现的文件不要报；
- **纯风格偏好不算问题**：等价改写（类型注解补充、f-string/% 格式化互换、
  getter/命名微调、防御性判空、this限定等）不改变行为与契约——即使看起来
  可挑剔也输出 []，不要为此造 style-nit/naming-convention 意见；
  style 类词仅用于**新代码违反该语言强约定**的场景（如 Go 导出符号缺注释
  → missing-doc-comment），存量代码的风格瑕疵不报。`

// modelView 评测侧并行解码视图：kind（金标准匹配主键）与 verdict（模型自评）
// 不在 plugin.FindingsWire 生产契约里——生产链路 verdict 由 R5 真值表裁决（不读
// 模型自评）、意见身份按 file+comment+line 指纹（无 kind）。故评测侧自带视图
// 承载这两个字段：与 plugin.DecodeFindingsJSON 同输入、同修复（jsonrepair 单源
// + plugin.NormalizeFence），两遍解码条目序一一对应。
type modelView struct {
	Verdict  string `json:"verdict"`
	Findings []struct {
		Kind string `json:"kind"`
	} `json:"findings"`
}

// decodeModelOutcome 单发审查产出的归一化：宽容解析模型输出 → evalq.Outcome。
// kind/severity 去空白转小写（计分匹配本就大小写不敏感，归一后报告面直读）；
// kind 缺失的条目置空——计分侧自然不命中，诚实计量（不猜类别词）。
func decodeModelOutcome(text string) (evalq.Outcome, error) {
	wire, err := plugin.DecodeFindingsJSON(text)
	if err != nil {
		return evalq.Outcome{}, fmt.Errorf("解析 findings JSON: %w", err)
	}
	var view modelView
	if err := jsonrepair.Unmarshal(plugin.NormalizeFence(text), &view); err != nil {
		return evalq.Outcome{}, fmt.Errorf("解析 verdict/kind 视图: %w", err)
	}
	out := evalq.Outcome{Verdict: strings.ToLower(strings.TrimSpace(view.Verdict))}
	for i, f := range wire.Findings {
		var kind string
		if i < len(view.Findings) {
			kind = view.Findings[i].Kind
		}
		out.Findings = append(out.Findings, evalq.Finding{
			File:     strings.TrimSpace(f.File),
			Kind:     strings.ToLower(strings.TrimSpace(kind)),
			Severity: strings.ToLower(strings.TrimSpace(f.Severity)),
		})
	}
	return out, nil
}

// realReviewer 真实模型单发审查 Reviewer（test 侧轻装配，接入点 =
// evalq.Reviewer 接口；k 轮稳定性由 Evaluate 的 StabilityRuns 承担）。
type realReviewer struct {
	model *openai.ChatModel
}

// Review 单发调用：title+diff → 结构化 findings JSON → 归一化 Outcome。
// 调用或解析失败返回 error——评测侧保守计分（verdict 不命中 + 记漏检）。
func (r *realReviewer) Review(ctx context.Context, c evalq.Case) (evalq.Outcome, error) {
	msgs := []*schema.Message{
		schema.SystemMessage(reviewSystemPrompt),
		schema.UserMessage("标题：" + c.Title + "\n\n" + plugin.DataFence("PR diff", c.Diff)),
	}
	resp, err := r.model.Generate(ctx, msgs)
	if err != nil {
		return evalq.Outcome{}, fmt.Errorf("模型调用失败: %w", err)
	}
	return decodeModelOutcome(resp.Content)
}

// TestDecodeModelOutcome 单发审查产出的归一化规则（不联网，纯解析）：
// kind/severity 去空白转小写、kind 缺失置空（计分侧自然不命中，诚实计量）、
// 损坏 JSON 宽容修复、不可解析显式报错。
func TestDecodeModelOutcome(t *testing.T) {
	t.Run("完整形态与大小写归一", func(t *testing.T) {
		out, err := decodeModelOutcome(`{"verdict":"REQUEST_CHANGES","findings":[` +
			`{"file":"auth/login.go","line":4,"kind":"Hardcoded-Secret","severity":" HIGH ","comment":"口令硬编码"}],` +
			`"notes":""}`)
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if out.Verdict != "request_changes" {
			t.Errorf("verdict = %q, want request_changes", out.Verdict)
		}
		if len(out.Findings) != 1 {
			t.Fatalf("findings = %d, want 1", len(out.Findings))
		}
		f := out.Findings[0]
		if f.File != "auth/login.go" || f.Kind != "hardcoded-secret" || f.Severity != "high" {
			t.Errorf("归一化 = %+v, want file=auth/login.go kind=hardcoded-secret severity=high", f)
		}
	})

	t.Run("kind 缺失置空", func(t *testing.T) {
		out, err := decodeModelOutcome(`{"verdict":"comment","findings":[` +
			`{"file":"a.go","severity":"medium","comment":"缺 doc"}]}`)
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if len(out.Findings) != 1 {
			t.Fatalf("findings = %d, want 1", len(out.Findings))
		}
		if out.Findings[0].Kind != "" {
			t.Errorf("kind 缺失应置空，实得 %q", out.Findings[0].Kind)
		}
	})

	t.Run("损坏 JSON 宽容修复", func(t *testing.T) {
		// 尾逗号形态（jsonrepair 单源覆盖的典型损坏）
		out, err := decodeModelOutcome(`{"verdict":"approve","findings":[` +
			`{"file":"a.go","kind":"style-nit","severity":"low","comment":"命名"},],}`)
		if err != nil {
			t.Fatalf("宽容解析应成功: %v", err)
		}
		if out.Verdict != "approve" || len(out.Findings) != 1 || out.Findings[0].Kind != "style-nit" {
			t.Fatalf("宽容解析结果: %+v", out)
		}
	})

	t.Run("空 findings", func(t *testing.T) {
		out, err := decodeModelOutcome(`{"verdict":"approve","findings":[]}`)
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if out.Verdict != "approve" || len(out.Findings) != 0 {
			t.Fatalf("空 findings 结果: %+v", out)
		}
	})

	t.Run("不可解析报错", func(t *testing.T) {
		if _, err := decodeModelOutcome("not json at all {{{"); err == nil {
			t.Fatal("不可解析应报错（评测出错保守计分的输入面）")
		}
		if _, err := decodeModelOutcome(""); err == nil {
			t.Fatal("空输出应报错")
		}
	})
}

// TestEvalBaselineRealModel 真实模型基准集跑批入口（make eval-baseline-real）：
// 装载 eval/baseline → evalq.Evaluate（StabilityRuns 由 ARGUS_EVAL_STABILITY
// 控制、缺省 3；Model 记 OPENAI_MODEL）→ 报告写
// eval/results/baseline-latest.{json,md}（gitignored）。
// 门禁：ARGUS_EVAL=1 且 OPENAI_* 齐全才跑，否则 Skip（无网环境正确跳过）。
func TestEvalBaselineRealModel(t *testing.T) {
	if os.Getenv("ARGUS_EVAL") != "1" {
		t.Skip("真实模型评测需 ARGUS_EVAL=1（make eval-baseline-real）")
	}
	config.LoadDotEnv()
	baseURL := os.Getenv("OPENAI_BASE_URL")
	apiKey := os.Getenv("OPENAI_API_KEY")
	modelName := os.Getenv("OPENAI_MODEL")
	if baseURL == "" || apiKey == "" || modelName == "" {
		t.Skip("ARGUS_EVAL=1 需要 OPENAI_BASE_URL / OPENAI_API_KEY / OPENAI_MODEL（lacus/.env）")
	}

	ds, err := evalq.LoadDir(filepath.Join("..", "..", "eval", "baseline"))
	if err != nil {
		t.Fatalf("装载基准集: %v", err)
	}
	runs := 3
	if v := strings.TrimSpace(os.Getenv("ARGUS_EVAL_STABILITY")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("ARGUS_EVAL_STABILITY=%q 非法（需 ≥1 的整数轮数）", v)
		}
		runs = n
	}

	// 轻装配（与生产 BuildModels 同构造器；参数固定进 Options.Params 快照）
	maxTokens := 8192
	cm, err := openai.NewChatModel(context.Background(), &openai.ChatModelConfig{
		BaseURL: baseURL, APIKey: apiKey, Model: modelName,
		MaxTokens: &maxTokens, Timeout: 180 * time.Second,
	})
	if err != nil {
		t.Fatalf("装配模型: %v", err)
	}

	rep, err := evalq.Evaluate(context.Background(), ds.Cases, &realReviewer{model: cm}, evalq.Options{
		Model:         modelName,
		Params:        map[string]string{"prompt": "evalq-single-shot-v1", "max_tokens": strconv.Itoa(maxTokens)},
		StabilityRuns: runs,
	})
	if err != nil {
		t.Fatalf("评测跑批: %v", err)
	}
	if rep.Cases != len(ds.Cases) {
		t.Fatalf("跑批样例数 = %d, want %d", rep.Cases, len(ds.Cases))
	}

	// artifact 落盘（gitignored，与打桩面同路径同名）
	outDir := filepath.Join("..", "..", "eval", "results")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonPath := filepath.Join(outDir, "baseline-latest.json")
	f, err := os.Create(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := rep.WriteJSON(f); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	mdPath := filepath.Join(outDir, "baseline-latest.md")
	f, err = os.Create(mdPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := rep.WriteMarkdown(f); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	o := rep.Overall
	t.Logf("指标（model=%s runs=%d cases=%d errored=%d）: precision=%.3f recall=%.3f "+
		"fp_rate=%.3f miss_rate=%.3f f1=%.3f verdict_accuracy=%.3f stability=%.3f",
		rep.Model, rep.Runs, rep.Cases, rep.Errored,
		o.Precision, o.Recall, o.FPRate, o.MissRate, o.F1, o.VerdictAccuracy, rep.Stability)
	t.Logf("报告已写 %s / %s", jsonPath, mdPath)

	// 全部样例失败 = 一次有效观测都没有（网络不通/超时的首跑形态）——先留
	// artifact 再红，错误信息直指可重跑入口。
	if rep.Cases > 0 && rep.Errored == rep.Cases {
		t.Fatalf("全部 %d 个样例评测失败（网络不通/超时？）：首跑待网络，"+
			"恢复后重跑 make eval-baseline-real", rep.Cases)
	}
}
