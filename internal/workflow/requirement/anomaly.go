// 循环异常守卫（对标 ZCode model-anomaly.ts）：ReAct 循环无人类兜底，死循环
// （反复以相同参数调用同一工具）是高频真实故障。守卫对每笔工具调用计算稳定
// 签名（工具名 + 规范化参数——键排序 JSON，模型输出键序不稳定是漏检源），
// 连续同签名达到阈值即短路返回提醒文本（不再执行工具，省掉无效开销）；
// 提醒次数有每 run 封顶——封顶后放行执行，防模型被持续拒绝而卡死在别处。
package requirement

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/components/tool"

	argusctx "argus/internal/context"
	"argus/internal/obs"
)

const (
	// anomalyThreshold 连续同签名调用达到该次数即短路提醒（前 anomalyThreshold-1 次放行）。
	anomalyThreshold = 3
	// anomalyMaxNudges 单 run 提醒封顶：防提醒本身成为新的刷屏源（ZCode 同款封顶）。
	anomalyMaxNudges = 3
)

// anomalyGuard 单个 ReAct run 一个实例（r3Tools 每次构造工具表时新建）。
type anomalyGuard struct {
	mu      sync.Mutex
	consec  map[string]int // 签名 -> 连续次数（链断即重计）
	lastSig string
	nudges  int
}

func newAnomalyGuard() *anomalyGuard {
	return &anomalyGuard{consec: map[string]int{}}
}

// check 记录一次调用并判定是否短路；返回的提醒文本已按 system-reminder 纪律包装。
func (g *anomalyGuard) check(toolName, rawArgs string) (string, bool) {
	sig := toolName + "|" + canonicalArgs(rawArgs)
	g.mu.Lock()
	defer g.mu.Unlock()
	if sig != g.lastSig {
		g.lastSig = sig
		g.consec[sig] = 1
		return "", false
	}
	g.consec[sig]++
	if g.consec[sig] < anomalyThreshold || g.nudges >= anomalyMaxNudges {
		return "", false
	}
	g.nudges++
	obs.IncrToolAnomaly(toolName)
	return argusctx.ReminderNote("loop-anomaly",
		fmt.Sprintf("你已连续 %d 次以完全相同的参数调用工具 %q，本次调用被跳过。不要重复同一调用：换个查询目标、缩小范围，或基于已获取的信息直接推进结论。", g.consec[sig], toolName)), true
}

// canonicalArgs 规范化参数 JSON：解析后重序列化（Go map 序列化键排序确定），
// 消除模型输出键序差异造成的漏检；解析失败原样返回（签名退化为原文比对）。
func canonicalArgs(raw string) string {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return string(b)
}

// guardedTool 异常守卫装饰器：Invoke 前过守卫，短路时提醒文本作为工具结果回喂。
type guardedTool struct {
	tool.InvokableTool
	name  string
	guard *anomalyGuard
}

func (g *guardedTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	if nudge, blocked := g.guard.check(g.name, argumentsInJSON); blocked {
		return nudge, nil
	}
	return g.InvokableTool.InvokableRun(ctx, argumentsInJSON)
}

// wrapAnomalyGuard 给整表套循环异常守卫（不可调用或无名字的工具原样透传）。
// 守卫在 LimitCalls 之外层：被拦截的调用不消耗外部工具的调用限额。
func wrapAnomalyGuard(guard *anomalyGuard, tools []tool.BaseTool) []tool.BaseTool {
	out := make([]tool.BaseTool, 0, len(tools))
	for _, t := range tools {
		it, ok := t.(tool.InvokableTool)
		if !ok {
			out = append(out, t)
			continue
		}
		info, err := t.Info(context.Background())
		if err != nil || info == nil || info.Name == "" {
			out = append(out, t)
			continue
		}
		out = append(out, &guardedTool{InvokableTool: it, name: info.Name, guard: guard})
	}
	return out
}
