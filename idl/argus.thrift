// argus HTTP API IDL —— 路由唯一事实来源（design.md §5）。
// 2026-10-27 收敛：把散落 router.go / main.go 手工注册的业务面收编进来，
// 生成物（biz/model、biz/router、biz/handler 空壳）一律以本文件再生成为准，
// 严禁手改 "DO NOT EDIT" 生成文件（此前 TaskKind 枚举/duplicated 字段手改生成物、
// 六条业务路由绕过 IDL，hz update 一次即失真——已全部收编）。
//
// 刻意不进 IDL 的路由（均有设计背书，勿私挂业务端点效仿）：
//   /healthz /readyz /version /metrics —— 基础设施探针/可观测（main.go registerCustom）
//   /mcp、/.well-known/agent.json      —— MCP streamable http 与 A2A 卡片，规范要求裸协议体
//   /events                            —— SSE 流式响应体无法用 IDL 声明（design.md §5 v3.2 注）
// /ping（hz 脚手架遗留）已于 v3.189.0 删除，与 /healthz 重复。
namespace go api

enum TaskKind {
    REQUIREMENT_COMPLIANCE = 0,
    CODE_REVIEW            = 1,
    VISUAL_CHECK           = 2,
    DESCRIBE               = 3,  // v3.71.0 写作面：PR 描述生成
    CHANGELOG              = 4,  // v3.71.0 写作面：changelog 条目
    CI_ANALYSIS            = 5,  // v3.72.0：CI 失败日志归因
    // diff_preview（v3.123.0 本地 diff 预审）刻意不入此枚举：内部任务由 MCP
    // review_diff_async 创建（platform=local、四元组空），不具 POST /tasks 的
    // PR 语义；词表单源在 domain/taskkind.go，经 service.kindIDLTable 映射。
}

enum AgentKind {
    BUILTIN = 0,
    CLI     = 1,
    REMOTE  = 2,
}

enum AgentProtocol {
    NONE = 0,
    A2A  = 1,
    MCP  = 2,
}

struct Finding {
    1: required string file,
    2: required i32 line,
    3: required string severity,
    4: required string comment,
    5: optional string by,
}

struct AgentManifestDTO {
    1: required string name,
    2: required AgentKind kind,
    3: required list<string> skills,
    4: required bool enabled,
    5: optional string cost,
    6: optional i64 timeoutMs,
    7: optional string version,
    8: optional AgentProtocol protocol,
    9: optional list<string> needs,
    10: optional list<string> triggerPaths,
    11: optional list<string> triggerLabels,
}

struct ListAgentsRequest {
    1: optional string platform,  // api.query="platform" 仓库配置视图（三参全传才启用）
    2: optional string owner,     // api.query="owner"
    3: optional string repo,      // api.query="repo"
}

struct RoutingRuleView {
    1: optional list<string> paths,
    2: optional list<string> labels,
    3: optional list<string> use,
}

struct RepoAgentStatus {
    1: required string name,
    2: required string repoEffect,      // default | selected | routed | excluded_by_use | disabled | locked
    3: optional string routedBy,        // 被哪些 routing 规则引用（"0,2"）
    4: optional string note,
}

struct RepoConfigView {
    1: required bool loaded,            // 是否成功读取仓库 .reviewagents.yaml
    2: optional list<string> use,
    3: optional list<string> disable,
    4: optional list<RoutingRuleView> routing,
    5: optional list<string> warnings,  // 信任边界告警（未知名忽略/lock 保护拒绝）
    6: optional list<RepoAgentStatus> plugins,
}

struct ListAgentsResponse {
    1: required list<AgentManifestDTO> agents,
    2: optional RepoConfigView repoConfig,  // platform/owner/repo 三参全传时返回
}

struct CreateTaskRequest {
    1: required TaskKind kind,
    2: required string platform,
    3: required string owner,
    4: required string repo,
    5: required string number,
    6: optional map<string, string> artifacts,
}

struct CreateTaskResponse {
    1: required string taskId,
    2: required string status,
    3: optional bool duplicated,  // 命中在飞任务幂等返回（v3.8.0）：同 PR 已有在飞任务时 true
}

struct GetTaskResponse {
    1: required string taskId,
    2: required string status,
    3: optional string verdict,
    4: optional list<Finding> findings,
    // 5: 编号保留——曾为 reportRef（v3.1 "报告落存储后按 ref 查询"预留），实现
    //    从未赋值，2026-10-27 收敛时移除；编号不复用，防旧客户端串字段。
    6: optional string failReason,
    7: optional string report,
    8: optional string debug,       // ARGUS_DEBUG=1 时的阶段产物（R1 spec/R2 摘要/R3 原始输出）
}

struct WebhookResponse {
    1: required string status,      // accepted | duplicate | skipped | error
    2: optional string reason,
    3: optional string eventId,  // 幂等键=平台 delivery ID（缺失为空），非 SDK 纳秒 ID
    4: optional string taskId,      // duplicate 时为命中的既有任务（幂等断言落点）
}

struct ListTasksRequest {
    1: optional string status (api.query="status"),  // pending|running|done|failed|stale，空=全部
    2: optional i32 limit (api.query="limit"),       // 默认 20，上限 100
}

struct TaskSummary {
    1: required string taskId,
    2: required string status,
    3: required string kind,
    4: required string platform,
    5: required string owner,
    6: required string repo,
    7: required string number,
    8: optional string headSha,
    9: optional string verdict,
    10: required i64 createdAt,     // Unix 秒
}

struct ListTasksResponse {
    1: required list<TaskSummary> tasks,
}

struct SuppressFindingRequest {
    1: required string platform,
    2: required string owner,
    3: required string repo,
    4: required string number,
    5: optional string file,        // 与报告中的意见 file 一致
    6: optional string comment,     // 与报告中的意见原文一致（与 fingerprint 二选一）
    7: optional string fingerprint, // 报告意见行的 fp 短码（≥4 位前缀，服务端解析完整指纹）
}

struct SuppressFindingResponse {
    1: required bool matched,       // 是否命中既有指纹（false = 新增抑制记录，下轮起生效）
    2: required string fingerprint, // 指纹（file + 规范化评论文本哈希；行号不入指纹）
}

// RestoreFindingRequest 恢复被抑制意见（v3.131.0，suppress 反向）：字段与
// SuppressFinding 对齐，指纹用报告行的 fp 短码（6-64 位十六进制）。
struct RestoreFindingRequest {
    1: required string platform,
    2: required string owner,
    3: required string repo,
    4: required string number,
    5: required string fp,
}

struct RestoreFindingResponse {
    1: required bool matched,       // 命中并置回 open；false = 未找到该指纹的抑制记录
}

// StatsOverviewRequest 审查概览聚合入参（v3.88.0，仪表盘数据面）：四参全缺省
// = 全仓全时段。
struct StatsOverviewRequest {
    1: optional string platform (api.query="platform"),
    2: optional string owner (api.query="owner"),
    3: optional string repo (api.query="repo"),
    4: optional i32 days (api.query="days"),  // 统计窗口天数
}

// RepoSettingsResponse repo-settings 四端点共用响应壳（data 为动态 map：
// Get/Put 返回覆盖集+全局生效值视图、List 返回全部配置集合、Delete 返回幂等
// 确认——字段以 service_reposettings.go 实现为准，不落死字段防漂移）。
struct RepoSettingsResponse {}

// UsageMonthlyResponse data 为动态聚合（month/total/budget/repos[]，snake_case
// JSON key 以 service.UsageMonthly 实现为准）——不落死字段防漂移，IDL 只承载
// 路由与存在性契约。下同（Stats/Health/Impact）。
struct UsageMonthlyResponse {}

struct StatsOverviewResponse {}

// TaskImpactResponse 响应为自包含交互 HTML（c.Data 直写 text/html，无 JSON 信封）。
struct TaskImpactResponse {}

struct RepoMapResponse {} // 仓库级代码地图（v5.6.2）：自包含交互 HTML 直写（同 TaskImpact，无 JSON 信封）。

struct HealthReportResponse {}

service TaskService {
    CreateTaskResponse CreateTask(1: CreateTaskRequest request) (api.post="/tasks")
    GetTaskResponse GetTask(1: string taskId (api.path="taskId")) (api.get="/tasks/:taskId")
    ListAgentsResponse ListAgents(1: ListAgentsRequest request) (api.get="/agents")
    ListTasksResponse ListTasks(1: ListTasksRequest request) (api.get="/tasks")
    // —— 2026-10-27 收编的手工注册业务面（原 router.go/main.go customizedRegister）——
    // 按 head SHA 查最近一次成功报告（v3.1），响应复用 GetTaskResponse。
    GetTaskResponse ReportByRef(1: string ref (api.path="ref")) (api.get="/reports/:ref")
    // 本月各仓库 token 用量与预算水位（v3.1）。
    UsageMonthlyResponse UsageMonthly() (api.get="/usage/monthly")
    // 审查概览聚合（v3.88.0，按仓任务量/verdict 分布/抑制计数）。
    StatsOverviewResponse StatsOverview(1: StatsOverviewRequest request) (api.get="/stats/overview")
    // 任务影响面交互视图（v3.183.0）。
    TaskImpactResponse TaskImpact(1: string taskId (api.path="taskId")) (api.get="/tasks/:taskId/impact")
    // 仓库级代码地图（v5.6.2）：整仓目录依赖图自包含交互 HTML（浅克隆+TTL 缓存）。
    RepoMapResponse RepoMap(1: string owner (api.query="owner"), 2: string repo (api.query="repo"), 3: optional string platform (api.query="platform")) (api.get="/repos/map")
    // 健康报告查询面（v3.13.0，与周期 IM 推送同数据源）。
    HealthReportResponse HealthReport() (api.get="/reports/health")
}

service FindingFeedbackService {
    SuppressFindingResponse SuppressFinding(1: SuppressFindingRequest request) (api.post="/findings/suppress")
    // 恢复被抑制意见（v3.131.0）：admin 档 token（service 层 scope 二次判定，fail-closed）。
    RestoreFindingResponse RestoreFinding(1: RestoreFindingRequest request) (api.post="/findings/restore")
}

service WebhookService {
    WebhookResponse Receive(1: string platform (api.path="platform")) (api.post="/webhook/:platform")
}

// —— v3.189.0 收编：v3.187.0 散落 router.go 手挂的按仓配置路由（原
// customizedRegister 四行）——
service RepoSettingsService {
    // 全部按仓配置（观测面，read 档）。
    RepoSettingsResponse RepoSettingsList() (api.get="/repo-settings")
    // 单仓配置视图（覆盖集 + 全局生效值，read 档）。
    RepoSettingsResponse RepoSettingsGet(1: string platform (api.path="platform"),
        2: string owner (api.path="owner"),
        3: string repo (api.path="repo")) (api.get="/repo-settings/:platform/:owner/:repo")
    // 写入单仓覆盖（admin 档由 requiredScope 收口）：body 为 reposettings.Settings
    // JSON 原始字节透传（DisallowUnknownFields 严格校验在 service 层 Parse，
    // 不经 IDL struct 绑定），上限 4KB。
    RepoSettingsResponse RepoSettingsPut(1: string platform (api.path="platform"),
        2: string owner (api.path="owner"),
        3: string repo (api.path="repo")) (api.put="/repo-settings/:platform/:owner/:repo")
    // 移除单仓覆盖（admin 档；幂等）。
    RepoSettingsResponse RepoSettingsDelete(1: string platform (api.path="platform"),
        2: string owner (api.path="owner"),
        3: string repo (api.path="repo")) (api.delete="/repo-settings/:platform/:owner/:repo")
}
