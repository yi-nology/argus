// 限速装饰器（RateModel）——v3.77.0 实现下沉 agentkit/llm/wrap；
// 本文件保留 API 门面，调用方零改动。语义：直调旁路与 Generator 链同桶真全局。
package context

import "git.example.com/enjoydream/agentkit/llm/wrap"

type (
	// RateModel 限速装饰器；Limiter nil = 直通。
	RateModel = wrap.RateModel
)

// NewRateModel 包装 inner；limiter 为 nil 时返回原对象（零开销直通）。
var NewRateModel = wrap.NewRateModel
