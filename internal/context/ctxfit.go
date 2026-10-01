// 上下文窗口治理（FitModel/FitMessages/真实 usage 校准）——v3.77.0 实现下沉
// agentkit/llm/wrap（eino 模型装饰器通用能力归位 SDK，观测依赖回调化 OnBreakdown）。
// 本文件保留 API 门面（类型/函数别名），调用方零改动；观测接线在 bootstrap
// （OnFit→指标+日志、OnBreakdown→obs.AddCtxBreakdown）。
package context

import "git.example.com/enjoydream/agentkit/llm/wrap"

type (
	// FitAction 拟合动作（观测标签；passthrough 应占绝对多数）。
	FitAction = wrap.FitAction
	// FitResult 拟合结果（rune 口径）。
	FitResult = wrap.FitResult
	// FitModel 输入自守恒装饰器：Generate/Stream 前三级拟合，窗口 ≤0 时直通。
	FitModel = wrap.FitModel
)

// 拟合动作取值（观测标签词表）。
const (
	FitPass       = wrap.FitPass
	FitMicro      = wrap.FitMicro
	FitTruncated  = wrap.FitTruncated
	FitMicroTrunc = wrap.FitMicroTrunc
)

var (
	// NewFitModel 包装 inner；WindowTokens ≤0 时保持直通（不改行为）。
	NewFitModel = wrap.NewFitModel
	// FitMessages 三级拟合纯内核（L1 microcompact / L2 截最长 user / L3 迭代截
	// 最大工具结果）；limitChars ≤0 或未超限时零拷贝直通。
	FitMessages = wrap.FitMessages
	// RecordUsage 回灌真实 prompt tokens（runner 的 UsageHandler 回调侧调用），
	// 与发送 rune 数配对出 tokens/char 校准系数。
	RecordUsage = wrap.RecordUsage
)
