// 输出截断续写（ContinueModel）——v3.77.0 实现下沉 agentkit/llm/wrap；
// 本文件保留 API 门面，调用方零改动。语义：finish reason 为 length/max_tokens
// 且消息无工具调用时保留部分输出、续写指令再调、顺序拼接 ≤3 轮。
package context

import "git.example.com/enjoydream/agentkit/llm/wrap"

type (
	// ContinueModel 输出截断续写装饰器；无可变状态，可跨任务共享。
	ContinueModel = wrap.ContinueModel
)

// NewContinueModel 包装 inner；轮数上限 ≤0 时取缺省 3。
var NewContinueModel = wrap.NewContinueModel
