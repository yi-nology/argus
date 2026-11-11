# Security Policy

## 报告漏洞

发现安全问题请**不要**开公开 issue。通过仓库托管平台的私密渠道联系维护者（私信 / private vulnerability report），
附：影响版本、复现步骤、影响面评估。维护者确认后回复修复计划与披露时间。

响应预期：确认收到 ≤ 3 个工作日；修复发布节奏按严重度协商。

## 支持范围

仅最新主干（`main`）接收安全修复；历史版本不回补。

## 设计上的安全面

Argus 的威胁模型围绕「LLM 输出不可信」：

- LLM 输出经 JSON parser，容错与结构校验在 `adapters.go` / `service.go`；
- Webhook 验签（`middleware.go`）+ 时间戳防重放（偏差 5 分钟）+ 随机 delivery-ID 去重；
- Token、PAT 只经 bearer/请求头下发，日志与 diff 脱敏（`secret_redact` 语义，see `internal/config`）；
- `provider.token_mode=header`（默认）：机密不进 URL；URL 模式仅限受控内网代理。

漏洞修复遵循根因修复（见 `CONTRIBUTING.md`），不在输出层加校验遮症状。
