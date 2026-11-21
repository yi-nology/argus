# Argus

**自托管多 Agent PR 审查与受控变更平台。** 审查留痕，远端写入与合并由维护者逐次确认。

> 产品由两个仓库组成：

| 组件 | 仓库 | 说明 |
|---|---|---|
| **argus-service** | [yi-nology/argus-service](https://github.com/yi-nology/argus-service) | Go 服务：R1–R5 需求复核、多专家插件、确定性裁决、合并资格门、修复审批票、受控推送、多用户与出站治理 |
| **argus-frontend** | [yi-nology/argus-frontend](https://github.com/yi-nology/argus-frontend) | 审查工作台前端（React + Vite + Ant Design）：任务 / 审批 / 合并 / 推送 confirm |

## 它如何工作

```
PR 事件（webhook / poller / TaskAPI）
        │
        ▼
R1 需求解析 → R2 变更理解 → R3 完成度判定 → R4 多专家并发审查
        → R5 确定性真值表裁决（APPROVE / COMMENT / REQUEST_CHANGES）
        │
        ▼
行内评论 + 总结报告（原地更新）
        │
        ├─ 高危（critical/high）→ 修复审批票（具名批准才放行）
        ├─ 修复推送 preview → confirm（绑定 expected_remote_head）
        └─ 合并资格门（七条 fail-closed）→ preview → confirm
```

## 核心边界（如实声明）

- **合并与推送全部受控**：preview → confirm，绑定 `expected_remote_head`，超时进 `uncertain` 读回对账。
- **无 CI 或 CI 读取失败 = 阻断合并**（fail-closed，不是缺陷）。
- **缺审批票 = 未批准**：critical/high 的 fix 与推送环一律拒。
- **邀请永不授予 admin**：平台管理与业务裁定分离。
- **权限服务端投影**（`GET /auth/me`）：前端置灰仅提示，服务端仍二次判定。

## 快速开始

```bash
# 服务
git clone git@github.com:yi-nology/argus-service.git && cd argus-service
export ARGUS_APITOKEN=change-me
export ARGUS_GITHUB_TOKEN=...   # 或其它平台凭据
export OPENAI_BASE_URL=... OPENAI_API_KEY=... OPENAI_MODEL=...
make run

# 前端（另开终端）
git clone git@github.com:yi-nology/argus-frontend.git && cd argus-frontend
npm install && npm run dev      # http://127.0.0.1:5177
```

首管登录：服务端设 `ARGUS_BOOTSTRAP_ADMIN_USER` / `ARGUS_BOOTSTRAP_ADMIN_PASS` 冷启动后，在前端「登录」页进入。

## 文档

- 能力矩阵与验收清单：[argus-service/docs/delivery](https://github.com/yi-nology/argus-service/tree/main/docs/delivery)
- 架构与设计：[argus-service/ARCHITECTURE.md](https://github.com/yi-nology/argus-service/blob/main/ARCHITECTURE.md)
- API 目录：[argus-service/README.md](https://github.com/yi-nology/argus-service/blob/main/README.md)
- 前端说明：[argus-frontend/README.md](https://github.com/yi-nology/argus-frontend/blob/main/README.md)

## License

Apache-2.0（见 argus-service/LICENSE）
