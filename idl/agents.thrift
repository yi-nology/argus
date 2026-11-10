// agents.thrift —— 插拔层可观测域类型文件：AgentKind/AgentProtocol 枚举 +
// AgentManifestDTO 族 DTO（service AgentService 声明在 master argus.thrift）。
// 共享 namespace go argus；model 生成 biz/model/argus/agents.go。
namespace go argus
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


