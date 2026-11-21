# Argus 架构总览

> 深读：[argus-service/ARCHITECTURE.md](https://github.com/yi-nology/argus-service/blob/main/ARCHITECTURE.md) ·
> 能力矩阵：[docs/delivery/CAPABILITY-MATRIX.md](https://github.com/yi-nology/argus-service/blob/main/docs/delivery/CAPABILITY-MATRIX.md)

## 一图流

```text
        webhook / poller / TaskAPI / MCP
                    │
                    ▼
        ┌───────────────────────┐
        │   argus-service (Go)  │
        │  R1→R5 审查管线       │
        │  多专家插件 fan-out   │
        │  确定性真值表裁决     │
        └───────────┬───────────┘
                    │ 行内评论 + 总结报告
                    ▼
        ┌───────────────────────┐
        │  受控变更面（preview→confirm）│
        │  · 修复审批票（critical/high）│
        │  · 受控推送（绑定 head）      │
        │  · 合并资格门（七条 fail-closed）│
        └───────────┬───────────┘
                    │
                    ▼
        ┌───────────────────────┐
        │ argus-frontend 工作台 │
        │ 任务 / 审批 / 合并 / AI │
        └───────────────────────┘
```

## 分层（argus-service）

```text
L1 bootstrap 组合根（装配一切，无人 import 它）
L2 runner / service / poller / worker / mcpserver（编排与入口）
L3 workflow / poster / qa / storage / sastscan / effect / egress …
L4 plugin 契约单源 + domain + learning
L0 config / obs（纯底座）
```

依赖由 `architecture-policy.yaml` 钉住，`make arch` 可执行门禁。

## 受控写三类（全部 preview → confirm）

| 写 | 门 | 绑定 |
|---|---|---|
| 修复推送 | 审批票 + await_confirm | `task_id` + `expected_remote_head` |
| 合并 | 资格门七条 | `op_id` + `expected_remote_head` |
| 评论 | 陈旧 head 复核 + 幂等原位更新 | task_id 锚点 |

超时/断连 → `uncertain` → 读回对账（不盲重试）。

## 审查管线

R1 需求解析（AC 清单）→ R2 变更理解 → R3 完成度判定（ReAct+工具，证据 `file:line`）
→ R4 多专家并发（builtin/cli/acpx/remote，互盲或共识）→ R5 确定性真值表
（R5.1 missing/partial 否决 → R5.2 high → R5.3 人工确认 → R5.4 comment → R5.5 approve）。

## 安全边界

- 反注入：DataFence 数据区 + 无证据判定降级（极限 COMMENT，永不白给 APPROVE）
- 出站治理：同意门 + 正则/熵双通道掩码 + 哈希审计
- 鉴权：静态 token + 会话（TokenVerifier 语义）；邀请永不授予 admin
- 副作用闸：at-most-once + 栅栏 + 未知态对账
