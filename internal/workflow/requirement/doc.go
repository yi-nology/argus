// Package requirement 审查编排 R1→R5（design §4）：requirement-compliance
// workflow——R1 需求解析（r1.go）→ R2 变更理解（stages.go）→ R3 合规判定
// （compliance.go，agent ReAct 调工具，工具装配在 r3tools.go；R3.5 报告反思
// 见 reflection.go）→ R4 插件 fan-out 审查（逐插件 checkpoint，r4ckpt.go）
// → R5 裁决（verdict.go，R5.C 降级门/R5.3 人工确认）。另含完备性裁判
// （completeness.go）与循环异常守卫（anomaly.go）等确定性守卫件。
// 原则：编排是确定性 Go 代码——路由/裁决/合并不走 LLM；自主性只给 R3 阶段
// 内 agent（ReAct 调工具）。
package requirement
