// reflection.go —— R3.5 报告质量收敛轮（agentkit v0.8.0 reflection 架构原语）。
//
// 定位：R4 各插件 findings 经 Merger 去重/抑制过滤后、分歧标注与 R5 裁决之前的
// 一道"终审编辑"闸门——生成→批判→修订循环（reflection.Refine）按误报特征 Rubric
// 逐条核对，剔除误报嫌疑条目、修正严重级别与定位。架构选型依据（agentkit README
// "Reflection 该用"）：误报有可判定的硬标准（文件不在变更清单/行号越界/纯风格/
// 无证据定位），且产出重要到值得加倍调用成本（报告直接发给人工与平台）。
//
// 纪律：
//   - 只删/修，不增——修订协议按 id 回写，模型无法发明原列表之外的意见（防幻觉注入）；
//   - 逐字段校验回退：file 越界/severity 非法/line 负数/评语清空 → 回退原值；
//   - 反思是逐轮统计性判断，不写 suppress（持久抑制必须来自人工反馈，防自动化误杀）；
//   - 任何失败（模型错误/输出不可解析）保留原 findings——审查不因反思故障而失败；
//   - 默认关闭（ARGUS_REFLECTION_ROUNDS=0），开启后每任务多 2~2N 次模型调用。
package requirement

import (
	"context"
	"encoding/json"
	"strings"

	"git.example.com/enjoydream/agentkit/llm"
	"git.example.com/enjoydream/agentkit/reflection"
	"git.example.com/enjoydream/agentkit/severity"

	"argus/internal/plugin"
)

// ReflectSummary R3.5 反思结果（Output.Reflect；nil = 未启用或无 findings）。
type ReflectSummary struct {
	Rounds    int  // 实际批判轮数
	Converged bool // 是否通过 Rubric 收敛（false = 达轮次上限取末稿）
	Removed   int  // 剔除的误报嫌疑条数
	Adjusted  int  // 修正（file/line/severity/comment）的条数
	Kept      int  // 保留条数
}

// reflectMaxRounds 轮次上限：与 agentkit reflection 缺省一致（3）。
// 审查场景更多轮数收益递减而成本线性上涨，配置侧不放开。
const reflectMaxRounds = 3

// reflectFindingWire 反思协议的 findings 线格式：注入 id 字段作为回写锚点。
type reflectFindingWire struct {
	ID           int    `json:"id"`
	File         string `json:"file"`
	Line         int    `json:"line"`
	Severity     string `json:"severity"`
	Comment      string `json:"comment"`
	SuggestedFix string `json:"suggested_fix,omitempty"`
}

// reflectTask 终审编辑角色指令（reflection 包把它拼进执行者系统提示词）。
// 含【Argus R3.5 报告反思】前缀——e2e mockLLM 按系统提示词前缀路由桩响应，前缀即桩协议。
func reflectTask() string {
	return "【Argus R3.5 报告反思】你是代码审查报告的终审编辑。" +
		"多位审查专家对同一 PR 给出了 findings 列表（每条带 id）。" +
		"请严格按评审标准逐条核对，输出修订后的 findings JSON 数组：\n" +
		"1. 真实问题保留，可修正 file/line/severity/comment 使其更准确（id 必须保持不变）；\n" +
		"2. 命中评审标准任一条的条目直接剔除（不出现在输出中）；\n" +
		"3. 禁止新增原列表之外的条目。\n" +
		"只输出 JSON 数组本身，不要解释、不要 markdown 围栏。"
}

// reflectRubric 误报特征评审标准（可判定、可执行——reflection 的全部价值在 Rubric 质量）。
const reflectRubric = `逐条核对以下误报特征，命中任一条即剔除该条目：
1. 引用的 file 不在【变更文件清单】中（模型幻觉或越界引用）；
2. line <= 0 或 comment 为空或无法定位到具体问题；
3. severity 与描述的后果不匹配（如纯风格/命名偏好问题标为 high——应降级或剔除）；
4. 纯风格/格式偏好意见，且无功能影响（正确性/安全/并发/资源问题不算此类）；
5. 描述空泛无具体证据（如"代码质量不佳""建议优化"，说不出问题是什么、在哪）；
6. 同一问题重复条目（保留描述最完整的一条，其余剔除）；
7. 对测试代码/表驱动数据等刻意写法的误判（如把测试用例字面量当硬编码配置）。`

// reflectFindings 对合并后的 findings 跑批判→修订收敛，原位更新 merged.Findings，
// 返回反思摘要（调用方写进 Output；nil = 未执行）。
func (w *Workflow) reflectFindings(ctx context.Context, task plugin.Task, summary string, merged *plugin.MergedFindings) *ReflectSummary {
	if len(merged.Findings) == 0 {
		return nil
	}
	chatModel := w.ReflectModel
	if chatModel == nil {
		chatModel = w.R3ChatModel // 未专配时沿用 R3 的强模型（质量关键路径同档）
	}
	if chatModel == nil && w.LLM != nil {
		chatModel = w.LLM.RawModel()
	}
	if chatModel == nil {
		return nil
	}

	changed := map[string]bool{}
	if task.Diff != nil {
		for _, f := range task.Diff.Files {
			changed[f.Path] = true
		}
	}

	// 组装输入：findings 线格式（id 锚点）+ 变更文件清单 + 变更摘要。
	// 超长输入由反思包内部的 llm.Client fitInput 截断兜底（裁最长 user 消息并留痕）。
	wires := make([]reflectFindingWire, len(merged.Findings))
	for i, f := range merged.Findings {
		wires[i] = reflectFindingWire{ID: i, File: f.File, Line: f.Line,
			Severity: f.Severity, Comment: f.Comment, SuggestedFix: f.SuggestedFix}
	}
	wireJSON, _ := json.MarshalIndent(wires, "", " ")
	var in strings.Builder
	if len(changed) > 0 {
		in.WriteString("【变更文件清单】\n")
		for p := range changed {
			in.WriteString("- " + p + "\n")
		}
	}
	in.WriteString("\n【findings（待核对，每条带 id）】\n")
	in.WriteString(string(wireJSON))
	if summary != "" {
		in.WriteString("\n\n【变更摘要】\n")
		in.WriteString(summary)
	}

	maxIter := w.ReflectRounds
	if maxIter > reflectMaxRounds {
		maxIter = reflectMaxRounds
	}
	res, err := reflection.Refine(ctx, &reflection.Config{
		Model: chatModel, ModelName: "argus-r35",
		Task: reflectTask(), Input: in.String(),
		Rubric: reflectRubric, MaxIterations: maxIter,
	})
	if err != nil {
		w.Log.Warn("argus.r35.failed", "error", err.Error(), "hint", "保留原 findings")
		return nil
	}

	// 解析末稿并按 id 回写：只认原列表已有 id（防新增幻觉），逐字段校验回退（防篡改越界）。
	var revised []reflectFindingWire
	raw := llm.ExtractJSON(res.Output)
	if raw == "" || json.Unmarshal([]byte(raw), &revised) != nil {
		w.Log.Warn("argus.r35.parse_failed", "hint", "输出不可解析，保留原 findings")
		return nil
	}
	info := &ReflectSummary{Rounds: len(res.Rounds), Converged: res.Converged}
	seen := map[int]bool{}
	out := make([]plugin.Finding, 0, len(merged.Findings))
	for _, rv := range revised {
		if rv.ID < 0 || rv.ID >= len(merged.Findings) || seen[rv.ID] {
			continue // 编造/越界/重复的 id：丢弃
		}
		seen[rv.ID] = true
		f := merged.Findings[rv.ID]
		adjusted := false
		// file：只能改成变更清单内的路径；清单缺失（Diff nil）时接受非空改写；
		// 否则维持原值（防幻觉搬家）。
		if rv.File != "" && rv.File != f.File {
			if len(changed) == 0 || changed[rv.File] {
				f.File = rv.File
				adjusted = true
			}
		}
		if rv.Line > 0 && rv.Line != f.Line {
			f.Line = rv.Line
			adjusted = true
		}
		if sev, ok := severity.Normalize(rv.Severity); ok && sev != f.Severity {
			f.Severity = sev
			adjusted = true
		}
		if c := strings.TrimSpace(rv.Comment); c != "" && c != f.Comment {
			f.Comment = c
			adjusted = true
		}
		if rv.SuggestedFix != "" && rv.SuggestedFix != f.SuggestedFix {
			f.SuggestedFix = rv.SuggestedFix
			adjusted = true
		}
		if adjusted {
			info.Adjusted++
		}
		info.Kept++
		out = append(out, f)
	}
	info.Removed = len(merged.Findings) - len(out)
	merged.Findings = out

	w.Log.Info("argus.r35.done", "rounds", info.Rounds, "converged", info.Converged,
		"removed", info.Removed, "adjusted", info.Adjusted, "kept", info.Kept)
	return info
}
