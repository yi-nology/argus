// Package dispatch 审查引擎面（v3.173.0 自 plugin 拆出）：插件分发执行面
// （Dispatcher fan-out/预算/熔断，dispatcher*.go）、结果聚合（Merger 去重/
// 抑制 + best-of-N 采样共识，merger.go/bestofn.go）、编队共识（squad_
// consensus.go：多 agent 共识黑板跨轮合并）与注册表（registry.go）；契约/
// 观测经别名桥引用 plugin（contract_alias.go/observe_bridge.go），方向单一。
// 原则：路由/裁决/合并不走 LLM，全部确定性 Go 代码；N 采样的模型自评 verdict
// 不参与裁决，复现计数不进指纹（Finding.Samples 结构化标注，渲染层展示）。
package dispatch
