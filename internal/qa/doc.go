// Package qa 审查问答引擎（@argus ask）：PR 评论问答——ReAct 优先（agentrun +
// 审查工具表，模型能自查 diff/文件/知识库）+ 多轮会话；ReAct 失败自动落回
// 单轮直调。v3.5.6 自 service 拆出——门面层不再持有 LLM 编排逻辑；Engine 无
// 自有状态（会话历史在 Store，节流与 delivery 幂等留在 service 评论路由层），
// 字段全为共享引用，可按调用临时构造。另含会话历史 token 预算压实（budget.go）
// 与程序化工具编排（ptc.go）两个加速面（失败一律回退既有 ReAct 路径）。
package qa
