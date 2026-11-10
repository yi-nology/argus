// webhook.thrift —— webhook 域：WebhookService 单端点（POST /webhook/:platform）。
// body 为平台原生 JSON 透传（签名校验需原始字节，IDL 只声明路由）；不挂 authMw（平台签名在 service 层校验）。
// 共享 namespace go argus；model 生成 biz/model/argus/webhook.go。
namespace go argus
struct WebhookResponse {
    1: required string status,      // accepted | duplicate | skipped | error
    2: optional string reason,
    3: optional string eventId,  // 幂等键=平台 delivery ID（缺失为空），非 SDK 纳秒 ID
    4: optional string taskId,      // duplicate 时为命中的既有任务（幂等断言落点）
}

