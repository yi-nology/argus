# Argus 工程介绍

> 本仓是 Argus 的**工程与交付中心**：部署物（compose / chart / k8s）、发布脚本、
> 工程规范与产品站点。业务代码在 [argus-service](https://github.com/yi-nology/argus-service)，
> 工作台在 [argus-frontend](https://github.com/yi-nology/argus-frontend)。

## 仓库拓扑

| 仓 | 职责 | 语言栈 |
|---|---|---|
| **argus**（本仓） | 工程交付：部署、发布、文档站、规范 | Shell / YAML / 静态站 |
| argus-service | 审查引擎 + 受控变更 API | Go 1.27 · Hertz · GORM |
| argus-frontend | 审查工作台 UI | TS · React 19 · antd 6 · Ant Design X |

## 工程原则

1. **fail-closed 优先**：无 CI 不合并、缺票不修复、无 token 不服务、未知态先对账。
2. **受控远端写**：三类写（评论 / 推送 / 合并）全部 preview → confirm，绑定 `expected_remote_head`。
3. **确定性裁决**：R5 真值表零 LLM；路由/去重/合并指纹全是 Go 确定性代码。
4. **架构门禁可执行**：`architecture-policy.yaml` + `make arch`（分层白名单 / 组合根单向 / 词表门禁）。
5. **版本一处源**：`scripts/sync-version.sh` 五处同源；CI `check-version-consistency` 钉住。

## 质量门（argus-service 本地四连）

```bash
make lint    # vet + staticcheck + gofmt
make arch    # 架构守卫
make test    # 全量 -race
make eval    # golden 编排回归
```

## 可观测

- `/metrics` Prometheus（argus_* 前缀；多副本默认鉴权）
- `/healthz` 进程级 · `/readyz` 依赖面（db+llm）
- `/events` SSE 任务进度流
- `/reports/health` 学习环飞轮状态

## 交付物清单（本仓）

| 路径 | 内容 |
|---|---|
| `deploy/docker-compose/` | 单机 compose + plugins + `.env.example` |
| `deploy/helm/argus/` | HA Chart（双副本 + Migration Job + 备份恢复） |
| `deploy/k8s/` | 裸 K8s 清单（namespace → ingress） |
| `deploy/schema.sql` | MySQL 8.0 规范 schema（AutoMigrate 兜底） |
| `scripts/release.sh` | linux amd64/arm64 二进制 + checksums + SBOM |
| `scripts/preflight.sh` | 部署前环境自检 |
| `web/` | 产品站点（GitHub Pages） |
