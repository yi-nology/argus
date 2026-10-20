// Package codegraph 轻量代码图门面（v3.78.0 实现下沉 agentkit/codeintel——
// 目录符号索引是 agent 工具链通用能力，argus 保留 API 门面与工具壳语义；
// get_symbol/get_references/get_callers 的工具描述与注入面在本包消费方
// runner/inject.go）。类型/构造别名直通 SDK，调用方零改动。
//
// 历史边界（v3.15.0 P0-1 建，v3.22.0 v1.5 扩多语言）：词法级索引、纯 stdlib、
// 不经 LLM——现随 SDK 侧一并声明。
package codegraph

import "github.com/oagentkit/textx/codeintel"

type (
	// Symbol 一个顶层符号定义（func/type/var/const，含 receiver 与签名片段）。
	Symbol = codeintel.Symbol
	// Ref 一个标识符出现位置（文件:行，按行去重）。
	Ref = codeintel.Ref
	// PolySymbol 非 Go 语言的词法符号（py/js）。
	PolySymbol = codeintel.PolySymbol
	// Index 代码图索引（只读；多 goroutine 并发查询安全）。
	Index = codeintel.Index
)

// NewIndex 建立目录索引（dir 为工作副本根；maxFiles 封顶防超大仓，0 = 1000）。
var NewIndex = codeintel.NewIndex
