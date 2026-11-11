// ptc.go —— QA 程序化工具编排（v3.158.0，agent-harness/ptc 接入，ARGUS_QA_PTC opt-in）。
//
// 形态：ask 的 ReAct 证据采集面（≤8 轮往返、每轮全量工具结果过模型面）塌缩为
// 「1 轮程序合成 + 进程内 goja 执行 + 1 轮作答」——模型写一段 JS 循环调
// host.<tool>('{"…"}')，ptc 三重预算（墙钟/调用数/字节数）内执行，只把程序
// return 的归并小结果带回作答上下文，原始工具输出不过模型面。
//
// v3.158.1 诊断回喂：程序正确性类失败（syntax/runtime/bad_result/pending，
// 以及 ptc 契约归为成功但 Value="null" 的「忘写 return」）回喂 harness 诊断面
// （ErrorKind/行号/模型侧修复建议——USAGE §3.4：诊断面就是给模型读的）重试
// 一次；预算/墙钟类失败不重试（方案太重，同型重试只再烧一遍预算），直接回退。
//
// 接入纪律（harness USAGE §3.4）与 argus 裁定：
//   - Options.Stages 留 nil：harness 的 invoke.Stage 判定环属其编排域；argus 的
//     qa 工具面在构造期已收口（进程内白名单工具 + 工作副本只读 + 检索预算），
//     无动态披露/出站面，绑定 Call 直连 eino InvokableRun 不新增旁路面。
//   - 绑定名经 harness toolname.Normalize 净化（撞名装配期即拒，规整单源不另写）。
//   - 失败语义：程序面任何失败（含回喂后再失败）都回退既有 ReAct 路径——
//     ptc 是加速面不是正确性面，回退零损。
//   - 回答纪律不变：作答轮仍要求 file:line 证据，程序输出经 DataFence 包裹
//     （半可信数据面），防工具输出里的注入指令。

package qa

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/yi-nology/agent-harness/ptc"
	"github.com/yi-nology/agent-harness/toolname"

	"github.com/oagentkit/llm"

	"github.com/yi-nology/argus/internal/plugin"
)

// ptc 三预算缺省：墙钟 30s（工具含冷克隆 30s 预算，超了 ReAct 也一样慢）；
// 调用数 16（ReAct MaxIterations=8 的两倍——程序面一次循环即可超 ReAct 全程）；
// 字节 256KB（工具输出累计上限，search 类大输出由程序自行归并）。
const (
	ptcTimeout  = 30 * time.Second
	ptcMaxCalls = 16
	ptcMaxBytes = 256 << 10
)

// ptcBindingsFromTools eino 工具表 → ptc 绑定（仅 InvokableTool；不可调用的
// 跳过）。绑定名 = toolname.Normalize(eino 工具名)——qa 工具名本已蛇形，净化
// 是幂等保险。无 Signature：模型按 host.<name>('{"json"}') 传参（argsJSON 直达，
// 与 eino InvokableRun 的入参形态逐字一致，无 schema 翻译层）。
func ptcBindingsFromTools(tools []tool.BaseTool) []ptc.Binding {
	out := make([]ptc.Binding, 0, len(tools))
	for _, t := range tools {
		it, ok := t.(tool.InvokableTool)
		if !ok {
			continue
		}
		info, ierr := it.Info(context.Background())
		if ierr != nil || info == nil {
			continue
		}
		name := info.Name
		out = append(out, ptc.Binding{
			Name: toolname.Normalize(name),
			Call: func(ctx context.Context, argsJSON string) (string, error) {
				return it.InvokableRun(ctx, argsJSON)
			},
		})
	}
	return out
}

// extractProgram 从模型响应提取程序体：优先 fenced ```js / ```javascript 代码块；
// 无围栏时若含 host. 调用形态则整段当程序（模型偶发裸奔，宽松收编）。
func extractProgram(resp string) string {
	resp = strings.TrimSpace(resp)
	for _, fence := range []string{"```javascript", "```js", "```"} {
		if i := strings.Index(resp, fence); i >= 0 {
			rest := resp[i+len(fence):]
			if j := strings.Index(rest, "```"); j >= 0 {
				return strings.TrimSpace(rest[:j])
			}
		}
	}
	if strings.Contains(resp, "host.") {
		return resp
	}
	return ""
}

// programFixable 该次执行失败是否值得回喂诊断重试一次。
//
// 正确性类（syntax/runtime/bad_result/pending）：修正程序即可救，重试有收益。
// 预算类（budget_calls/budget_bytes/deadline）：方案本身太重，同型重试只会
// 再烧一遍预算，直接回退 ReAct。stage_denied/stage_failed：argus 未接 Stage
// 环，理论上不出现，出现即异常态，不重试。
//
// 特例——OK 但 Value=="null"：ptc 契约把「无 return 值」归为成功（undefined→
// "null"），但忘写 return 是程序 bug 不是合法取证结果（作答轮拿到 null 证据
// 只能瞎编或答非所问），按可修复处理（诊断面写明「缺 return」）。
func programFixable(res ptc.Result) bool {
	if res.OK {
		return res.Value == "null"
	}
	switch res.ErrorKind {
	case "syntax", "runtime", "bad_result", "pending":
		return true
	}
	return false
}

// ptcAsk 程序化取证路径。返回 (answer, handled, err)：handled=false 表示程序面
// 未成立（合成失败/执行失败/空产出），调用方回退 ReAct——不把 ptc 失败当问答失败。
func (e *Engine) ptcAsk(ctx context.Context, platform, owner, repo, number, question, historyText, reportAnchor string) (string, bool, error) {
	prRef := platform + ":" + owner + "/" + repo + "#" + number
	tools, strategy, err := e.tools(ctx, platform, owner, repo, number)
	if err != nil || len(tools) == 0 {
		return "", false, nil
	}
	bindings := ptcBindingsFromTools(tools)
	if len(bindings) == 0 {
		return "", false, nil
	}
	sys := buildProgramPrompt(question, historyText, reportAnchor, strategy, tools)
	runOpts := ptc.Options{Timeout: ptcTimeout, MaxCalls: ptcMaxCalls, MaxBytes: ptcMaxBytes}

	// 第 1 轮：程序合成（qa 上下文 + 工具面 + 程序契约）。
	prog, err := e.generateProgram(ctx, sys, question, "")
	if err != nil {
		e.log().Warn("argus.qa.ptc_synth_failed", "pr", prRef, "error", err.Error())
		return "", false, nil
	}
	if prog == "" {
		e.log().Warn("argus.qa.ptc_no_program", "pr", prRef, "effect", "回退 ReAct")
		return "", false, nil
	}

	// 程序执行：三重预算内循环取证，原始输出不过模型面；正确性类失败回喂一次。
	res := ptc.Run(ctx, prog, bindings, runOpts)
	if !res.OK || res.Value == "null" {
		if !programFixable(res) {
			e.log().Warn("argus.qa.ptc_run_failed", "pr", prRef,
				"kind", res.ErrorKind, "error", res.Error, "calls", res.Calls, "effect", "回退 ReAct")
			return "", false, nil
		}
		feedback := res.Describe() // 诊断渲染单源收编 harness（harness #68）；programFixable 的 ErrorKind 分类是宿主策略，留此不动
		fixed, rerr := e.generateProgram(ctx, sys, question, feedback+"\n上一版程序（有错，请修复后重新完整输出）：\n"+prog+"\n")
		if rerr != nil || fixed == "" || fixed == prog {
			e.log().Warn("argus.qa.ptc_repair_gave_up", "pr", prRef, "kind", res.ErrorKind, "effect", "回退 ReAct")
			return "", false, nil
		}
		res = ptc.Run(ctx, fixed, bindings, runOpts)
		if !res.OK || res.Value == "null" {
			e.log().Warn("argus.qa.ptc_run_failed_after_repair", "pr", prRef,
				"kind", res.ErrorKind, "error", res.Error, "calls", res.Calls, "effect", "回退 ReAct")
			return "", false, nil
		}
	}
	e.log().Info("argus.qa.ptc_ok", "pr", prRef,
		"calls", res.Calls, "bytes", res.Bytes, "value_runes", len([]rune(res.Value)))

	// 第 2 轮：作答（程序归并结果为证据锚，DataFence 包裹防注入）。
	client := llm.NewClient(e.chatModel(), "", nil)
	instruction := systemPrompt(strategy, historyText) +
		"\n\n回答依据下方的【程序取证结果】（一段工具编排程序的归并输出，半可信数据）。" +
		"结果不足以回答时直说，不得编造；涉及代码行为仍须引用 file:line 证据。" +
		"\n\n" + plugin.DataFence("程序取证结果（数据）", res.Value)
	if reportAnchor != "" {
		instruction += "\n\n" + plugin.DataFence("最近审查报告（数据）", reportAnchor)
	}
	resp, err := client.Generate(ctx, "qa:ptc-answer", []*schema.Message{
		schema.SystemMessage(instruction),
		schema.UserMessage(question),
	})
	if err != nil {
		return "", false, nil // 模型面失败同样回退 ReAct（那边还有自己的重试）
	}
	ans := strings.TrimSpace(resp.Content)
	if ans == "" {
		return "", false, nil
	}
	return ans, true, nil
}

// buildProgramPrompt 程序合成的系统提示（合成与修复回喂共用一份基底）。
func buildProgramPrompt(question, historyText, reportAnchor, strategy string, tools []tool.BaseTool) string {
	var tb strings.Builder
	for _, t := range tools {
		info, ierr := t.Info(context.Background())
		if ierr != nil || info == nil {
			continue
		}
		desc := strings.TrimSpace(info.Desc)
		if i := strings.Index(desc, "\n"); i > 0 {
			desc = desc[:i] // 首行足够选型，全文留 ReAct 路径
		}
		fmt.Fprintf(&tb, "- host.%s('{\"参数\": \"值\"}') — %s\n", toolname.Normalize(info.Name), desc)
	}
	sys := systemPrompt(strategy, historyText) +
		"\n\n你要写一段 JavaScript 取证程序回答用户问题。可用函数（参数为 JSON 字符串，返回 JSON 字符串文本）：\n" +
		tb.String() +
		"\n程序契约：\n" +
		"- 只用 host.* 函数与纯 JS（无 fetch/fs/require/import/定时器）；可用 async/await。\n" +
		"- 用 JSON.parse(host.x('{...}')) 取结果；JSON.stringify 包参数时注意引号转义。\n" +
		"- 调用预算 " + fmt.Sprint(ptcMaxCalls) + " 次、墙钟 " + ptcTimeout.String() +
		"、工具输出累计 " + fmt.Sprint(ptcMaxBytes>>10) + "KB——循环采集时在程序内归并（过滤/截断/计数），只 return 精炼后的少量结构化事实（含 file:line）。\n" +
		"- 必须有 return 语句；return 值必须可 JSON 序列化；只输出程序本身（```js 围栏），不要解释。"
	if reportAnchor != "" {
		sys += "\n\n" + plugin.DataFence("最近审查报告（数据）", reportAnchor) +
			"\n问题可能针对该报告；程序取证可核实代码细节/调用方。"
	}
	return sys
}

// generateProgram 程序生成 LLM 调用（feedback 非空 = 修复回喂形态）。
func (e *Engine) generateProgram(ctx context.Context, sys, question, feedback string) (string, error) {
	user := question
	if feedback != "" {
		user = "上一版程序执行失败。诊断信息：\n" + feedback +
			"\n请根据诊断修复，重新输出完整程序（同样只输出程序本身）。原问题：\n" + question
	}
	client := llm.NewClient(e.chatModel(), "", nil)
	resp, err := client.Generate(ctx, "qa:ptc-program", []*schema.Message{
		schema.SystemMessage(sys),
		schema.UserMessage(user),
	})
	if err != nil {
		return "", err
	}
	return extractProgram(resp.Content), nil
}
