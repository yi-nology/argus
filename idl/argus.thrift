namespace go api

enum TaskKind {
    REQUIREMENT_COMPLIANCE = 0,
    CODE_REVIEW            = 1,
    VISUAL_CHECK           = 2,
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
}

struct GetTaskResponse {
    1: required string taskId,
    2: required string status,
    3: optional string verdict,
    4: optional list<Finding> findings,
    5: optional string reportRef,
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

service TaskService {
    CreateTaskResponse CreateTask(1: CreateTaskRequest request) (api.post="/tasks")
    GetTaskResponse GetTask(1: string taskId (api.path="taskId")) (api.get="/tasks/:taskId")
    ListAgentsResponse ListAgents(1: ListAgentsRequest request) (api.get="/agents")
    ListTasksResponse ListTasks(1: ListTasksRequest request) (api.get="/tasks")
}

service FindingFeedbackService {
    SuppressFindingResponse SuppressFinding(1: SuppressFindingRequest request) (api.post="/findings/suppress")
}

service WebhookService {
    WebhookResponse Receive(1: string platform (api.path="platform")) (api.post="/webhook/:platform")
}
