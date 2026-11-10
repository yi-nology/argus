// Package runner 任务执行编排（§10.4 worker 的执行体）：拉起任务 → 组装
// 上下文（PR/diff，runner_prctx.go）→ 前置注入（OCR delegate / SAST /
// CodeGraph，inject.go）→ 调 workflow（R1→R5 或 R4+R5）→ 报告产物/公告
// （runner_report.go）→ poster 回帖。错误处置遵循 §10.2 矩阵：上下文/LLM
// 失败 → task=failed（不在 PR 评论，避免平台故障刷屏用户）；注入器/写盘类
// 失败一律告警降级、不阻断审查。
package runner
