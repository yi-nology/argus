// findings.thrift —— 意见反馈域：Finding 共享 struct（GetTaskResponse.findings 消费，
// + FindingFeedbackService（suppress/restore 反馈通道，设计 §3.3；service 声明在 master）。
// 共享 namespace go argus；model 生成 biz/model/argus/findings.go。
namespace go argus
struct Finding {
    1: required string file,
    2: required i32 line,
    3: required string severity,
    4: required string comment,
    5: optional string by,
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

