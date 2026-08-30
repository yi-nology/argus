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

struct ListAgentsResponse {
    1: required list<AgentManifestDTO> agents,
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
}

struct WebhookResponse {
    1: required string status,      // accepted | duplicate | skipped | error
    2: optional string reason,
    3: optional string eventId,  // 幂等键=平台 delivery ID（缺失为空），非 SDK 纳秒 ID
    4: optional string taskId,      // duplicate 时为命中的既有任务（幂等断言落点）
}

service TaskService {
    CreateTaskResponse CreateTask(1: CreateTaskRequest request) (api.post="/tasks")
    GetTaskResponse GetTask(1: string taskId (api.path="taskId")) (api.get="/tasks/:taskId")
    ListAgentsResponse ListAgents() (api.get="/agents")
}

service WebhookService {
    WebhookResponse Receive(1: string platform (api.path="platform")) (api.post="/webhook/:platform")
}
