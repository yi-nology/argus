# Argus

**自托管多 Agent PR 审查与受控变更平台。** 审查留痕，远端写入与合并由维护者逐次确认。

[产品站点](https://yi-nology.github.io/argus/) · [工程介绍](docs/ENGINEERING.md) · [部署指引](docs/DEPLOY.md) · [架构总览](docs/ARCHITECTURE.md)

## 三仓拓扑

| 仓库 | 职责 |
|---|---|
| **argus**（本仓） | **工程交付中心**：docker-compose · Helm Chart · 裸 K8s · 发布二进制 · 工程规范 · 产品站点 |
| [argus-service](https://github.com/yi-nology/argus-service) | Go 审查引擎 + 受控变更 API（R1–R5 / 插件 / 审批票 / 合并门 / 多用户） |
| [argus-frontend](https://github.com/yi-nology/argus-frontend) | TypeScript + Ant Design 6 + **Ant Design X** 审查工作台 |

## 它如何工作

```
PR 事件（webhook / poller / TaskAPI / MCP）
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

### docker-compose（单机）

```bash
git clone https://github.com/yi-nology/argus.git && cd argus/deploy/docker-compose
cp .env.example .env && chmod 600 .env   # 填 ARGUS_APITOKEN + 平台 token + OPENAI_*
docker compose up -d
curl http://127.0.0.1:8899/healthz
```

### Helm（K8s HA）

```bash
cd deploy/helm
helm lint argus && helm install argus argus -n argus --create-namespace
```

### 源码开发

```bash
# 服务
git clone https://github.com/yi-nology/argus-service.git && cd argus-service
make run

# 工作台
git clone https://github.com/yi-nology/argus-frontend.git && cd argus-frontend
npm install && npm run dev      # http://127.0.0.1:5177
```

首管登录：设 `ARGUS_BOOTSTRAP_ADMIN_USER` / `ARGUS_BOOTSTRAP_ADMIN_PASS` 冷启动后在工作台登录。

## 发布物

```bash
scripts/release.sh v8.2.0      # linux amd64/arm64 + checksums + SBOM
```

GHCR 镜像：`ghcr.io/yi-nology/argus` · Release 二进制见 [Releases](https://github.com/yi-nology/argus/releases)。

## 部署物索引（本仓）

| 路径 | 内容 |
|---|---|
| [`deploy/docker-compose/`](deploy/docker-compose/) | 单机 compose + plugins + `.env.example` |
| [`deploy/helm/argus/`](deploy/helm/argus/) | HA Chart（双副本 + Migration Job + 备份恢复） |
| [`deploy/k8s/`](deploy/k8s/) | 裸 K8s 清单（namespace → ingress） |
| [`deploy/schema.sql`](deploy/schema.sql) | MySQL 8.0 规范 schema |
| [`scripts/release.sh`](scripts/release.sh) | 二进制构建发布 |
| [`scripts/preflight.sh`](scripts/preflight.sh) | 部署前自检 |
| [`web/`](web/) | 产品站点（GitHub Pages） |

## 文档

- [工程介绍](docs/ENGINEERING.md) · [部署指引](docs/DEPLOY.md) · [架构总览](docs/ARCHITECTURE.md)
- 能力矩阵 / 验收清单：[argus-service/docs/delivery](https://github.com/yi-nology/argus-service/tree/main/docs/delivery)
- API 与环境变量：[argus-service/README](https://github.com/yi-nology/argus-service/blob/main/README.md)

## License

Apache-2.0（见 [argus-service/LICENSE](https://github.com/yi-nology/argus-service/blob/main/LICENSE)）
