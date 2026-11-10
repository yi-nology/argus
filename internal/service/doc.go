// Package service 服务门面：HTTP handler 薄实现层——绑定请求 → 调编排 →
// 统一响应壳 {code,data,message,trace_id}。webhook 同步路径实现 §5.1 全部
// 过滤规则 + §5.2 幂等；评论指令路由与执行（指令词法层在 commentcmd，本包
// 只做执行体）；任务 API/Agent 可观测面；审计事件外送（SIEM 旁路）；体检/
// 影响面/知识检索等门面端点。异步执行交给 worker pool（§10.4 DB 即队列）。
package service
