// task.thrift —— 任务域类型文件：TaskKind 枚举 + 任务/报告/用量/概览/影响面/地图 DTO
// + EventsResponse（/events 路由锚点，service 声明在 master argus.thrift）。
// 共享 namespace go argus（master idl/argus.thrift include 收口）；model 生成 biz/model/argus/task.go。
namespace go argus
include "findings.thrift"
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
    4: optional list<findings.Finding> findings,
    // 5: 编号保留——曾为 reportRef（v3.1 "报告落存储后按 ref 查询"预留），实现
    //    从未赋值，2026-10-27 收敛时移除；编号不复用，防旧客户端串字段。
    6: optional string failReason,
    7: optional string report,
    8: optional string debug,       // ARGUS_DEBUG=1 时的阶段产物（R1 spec/R2 摘要/R3 原始输出）
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

// StatsOverviewRequest 审查概览聚合入参（v3.88.0，仪表盘数据面）：四参全缺省
// = 全仓全时段。
struct StatsOverviewRequest {
    1: optional string platform (api.query="platform"),
    2: optional string owner (api.query="owner"),
    3: optional string repo (api.query="repo"),
    4: optional i32 days (api.query="days"),  // 统计窗口天数
}

// UsageMonthlyResponse data 为动态聚合（month/total/budget/repos[]，snake_case
// JSON key 以 service.UsageMonthly 实现为准）——不落死字段防漂移，IDL 只承载
// 路由与存在性契约。下同（Stats/Health/Impact）。
struct UsageMonthlyResponse {}

struct StatsOverviewResponse {}

// TaskImpactResponse 响应为自包含交互 HTML（c.Data 直写 text/html，无 JSON 信封）。
struct TaskImpactResponse {}

struct RepoMapResponse {} // 仓库级代码地图（v5.6.2）：自包含交互 HTML 直写（同 TaskImpact，无 JSON 信封）。

struct HealthReportResponse {}


struct EventsResponse {}

