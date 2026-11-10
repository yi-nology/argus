// argus HTTP API IDL —— 路由唯一事实来源（design.md §5）。
// v6.3.0 拆分：类型（enum/struct）按域拆入域文件，service 全部声明在 master（hz
// 只为 --idl 顶层文件的 service 生成路由，探针实锤；域文件共享单一 namespace
// go argus），model 按域文件拆、handler/router 归 biz/{handler,router}/argus/（此前 */api/）。
//
// 改路由后必跑：`make idl-gen`（hz update），生成物严禁手改（"DO NOT EDIT"
// 文件手改一次 hz update 即失真——TaskKind 枚举/duplicated 字段手改前科）；
// CI 有 idl-drift 门禁（hz update 后 git diff --exit-code），手改生成物必红。
// 生成物手写区（hz 保留、不被重写）：biz/handler/argus/*_service.go 函数体、
// biz/router/argus/middleware.go、biz/handler/argus/respond.go。
//
// 刻意不进 IDL 的路由（均有设计背书，勿私挂业务端点效仿）：
//   /healthz /readyz /version /metrics —— 基础设施探针/可观测（main.go registerCustom）
//   /mcp、/.well-known/agent.json      —— MCP streamable http 与 A2A 卡片，规范要求裸协议体
// /events 已于 v6.3.0 收编（task.thrift EventsService，响应空 struct、handler 体
// 保持 SSE 流式实现）；/ping（hz 脚手架遗留）已于 v3.189.0 删除。
//
// include 规则：域文件必须自行 include 它引用的类型文件（thrift 作用域无传递性），
// 共享 namespace（namespace go argus）下 thrift include 前缀只存在于 IDL 源，
// 生成的 Go 同包无前缀。
namespace go argus

include "task.thrift"
include "agents.thrift"
include "findings.thrift"
include "repo_settings.thrift"
include "webhook.thrift"
service TaskService {
    task.CreateTaskResponse CreateTask(1: task.CreateTaskRequest request) (api.post="/tasks")
    task.GetTaskResponse GetTask(1: string taskId (api.path="taskId")) (api.get="/tasks/:taskId")
    task.ListTasksResponse ListTasks(1: task.ListTasksRequest request) (api.get="/tasks")
    // —— 2026-10-27 收编的手工注册业务面（原 router.go/main.go customizedRegister）——
    // 按 head SHA 查最近一次成功报告（v3.1），响应复用 task.GetTaskResponse。
    task.GetTaskResponse ReportByRef(1: string ref (api.path="ref")) (api.get="/reports/:ref")
    // 本月各仓库 token 用量与预算水位（v3.1）。
    task.UsageMonthlyResponse UsageMonthly() (api.get="/usage/monthly")
    // 审查概览聚合（v3.88.0，按仓任务量/verdict 分布/抑制计数）。
    task.StatsOverviewResponse StatsOverview(1: task.StatsOverviewRequest request) (api.get="/stats/overview")
    // 任务影响面交互视图（v3.183.0）。
    task.TaskImpactResponse TaskImpact(1: string taskId (api.path="taskId")) (api.get="/tasks/:taskId/impact")
    // 仓库级代码地图（v5.6.2）：整仓目录依赖图自包含交互 HTML（浅克隆+TTL 缓存）。
    task.RepoMapResponse RepoMap(1: string owner (api.query="owner"), 2: string repo (api.query="repo"), 3: optional string platform (api.query="platform")) (api.get="/repos/map")
    // 健康报告查询面（v3.13.0，与周期 IM 推送同数据源）。
    task.HealthReportResponse HealthReport() (api.get="/reports/health")
}

service FindingFeedbackService {
    findings.SuppressFindingResponse SuppressFinding(1: findings.SuppressFindingRequest request) (api.post="/findings/suppress")
    // 恢复被抑制意见（v3.131.0）：admin 档 token（service 层 scope 二次判定，fail-closed）。
    findings.RestoreFindingResponse RestoreFinding(1: findings.RestoreFindingRequest request) (api.post="/findings/restore")
}

service WebhookService {
    webhook.WebhookResponse Receive(1: string platform (api.path="platform")) (api.post="/webhook/:platform")
}

// —— v3.189.0 收编：v3.187.0 散落 router.go 手挂的按仓配置路由（原
// customizedRegister 四行）——
service RepoSettingsService {
    // 全部按仓配置（观测面，read 档）。
    repo_settings.RepoSettingsResponse RepoSettingsList() (api.get="/repo-settings")
    // 单仓配置视图（覆盖集 + 全局生效值，read 档）。
    repo_settings.RepoSettingsResponse RepoSettingsGet(1: string platform (api.path="platform"),
        2: string owner (api.path="owner"),
        3: string repo (api.path="repo")) (api.get="/repo-settings/:platform/:owner/:repo")
    // 写入单仓覆盖（admin 档由 requiredScope 收口）：body 为 reposettings.Settings
    // JSON 原始字节透传（DisallowUnknownFields 严格校验在 service 层 Parse，
    // 不经 IDL struct 绑定），上限 4KB。
    repo_settings.RepoSettingsResponse RepoSettingsPut(1: string platform (api.path="platform"),
        2: string owner (api.path="owner"),
        3: string repo (api.path="repo")) (api.put="/repo-settings/:platform/:owner/:repo")
    // 移除单仓覆盖（admin 档；幂等）。
    repo_settings.RepoSettingsResponse RepoSettingsDelete(1: string platform (api.path="platform"),
        2: string owner (api.path="owner"),
        3: string repo (api.path="repo")) (api.delete="/repo-settings/:platform/:owner/:repo")
}

// —— ListAgents 由 TaskService 归位 AgentService（v6.3.0 拆分）——
service AgentService {
    agents.ListAgentsResponse ListAgents(1: agents.ListAgentsRequest request) (api.get="/agents")
}

// —— v6.3.0 收编（原 router.go customizedRegister 手挂）——
service EventsService {
    // /events SSE 任务进度流（v3.1）。
    // v6.3.0 收编进 IDL：响应空 struct 仅作路由声明锚点，
    // handler 体保持 SSE 流式实现。
    task.EventsResponse Events() (api.get="/events")
}
