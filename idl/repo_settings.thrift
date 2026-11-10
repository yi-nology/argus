// repo_settings.thrift —— 按仓配置域：RepoSettingsService 四端点（v3.187.0 观测/写档面，
// v3.189.0 收编进 IDL）。PUT body 为 reposettings.Settings JSON 原始字节透传（service 层
// DisallowUnknownFields 严格校验，不经 IDL struct 绑定），上限 4KB。
// 共享 namespace go argus；model 生成 biz/model/argus/repo_settings.go。
namespace go argus
// RepoSettingsResponse repo-settings 四端点共用响应壳（data 为动态 map：
// Get/Put 返回覆盖集+全局生效值视图、List 返回全部配置集合、Delete 返回幂等
// 确认——字段以 service_reposettings.go 实现为准，不落死字段防漂移）。
struct RepoSettingsResponse {}

// —— v3.189.0 收编：v3.187.0 散落 router.go 手挂的按仓配置路由（原
// customizedRegister 四行）——
