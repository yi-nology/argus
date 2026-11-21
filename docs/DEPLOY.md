# Argus 部署指引

三种形态：**docker-compose**（单机）· **Helm Chart**（K8s HA）· **裸 K8s 清单**。
版本以 `helm/argus/Chart.yaml` 的 `appVersion` 为准（当前 v8.2.0）。

## 0. 发布二进制 / 镜像

```bash
# 二进制（linux amd64 + arm64 + checksums + SBOM）
scripts/release.sh v8.2.0

# 镜像（在 argus-service 仓构建）
cd ../argus-service && docker build -t argus:v8.2.0 .
```

GHCR：`ghcr.io/yi-nology/argus`（CI tag 推送）。

## 1. docker-compose（单机学习 / 内网）

```bash
cd deploy/docker-compose
cp .env.example .env && chmod 600 .env   # 填 ARGUS_APITOKEN + 平台 token + OPENAI_*
docker compose up -d
curl http://127.0.0.1:8899/healthz
curl http://127.0.0.1:8899/readyz
```

- SQLite 落 `./data`（单实例禁止 scale > 1）；多副本请用 PG + Helm。
- 前端：`argus-frontend` `npm run dev`，`/api` 代理 8899。
- 首管登录：`.env` 设 `ARGUS_BOOTSTRAP_ADMIN_USER/PASS`。

## 2. Helm Chart（生产 HA）

```bash
cd deploy/helm
helm lint argus
helm template argus argus > /tmp/rendered.yaml
helm install argus argus -n argus --create-namespace -f my-values.yaml
```

拓扑（缺省）：

- 双副本 RollingUpdate（maxUnavailable=0）
- PostgreSQL + PVC（memory/knowledge/ocr-home 共享）+ per-pod work 盘
- Migration Job 先行（`ARGUS_MIGRATE_MODE=job`）；readiness=/readyz · liveness=/healthz
- pg-backup CronJob + pg-restore 演练 Job

关键 values：

| 键 | 缺省 | 说明 |
|---|---|---|
| `image.repository` / `tag` | `argus` / AppVersion | 镜像 |
| `replicaCount` | 2 | 副本 |
| `resources` | 500m/512Mi ~ 2/4Gi | 配额 |
| `postgres.*` | 内置 | 或外接 DSN |

## 3. 裸 K8s 清单

```bash
cd deploy/k8s
kubectl apply -f 00-namespace.yaml
kubectl apply -f 01-secret.yaml       # 先编辑真实凭证
kubectl apply -f 02-configmap.yaml
kubectl apply -f 03-pvc.yaml
kubectl apply -f 05-plugins-configmap.yaml
kubectl apply -f 04-deployment.yaml
kubectl apply -f 06-service.yaml
# 可选：07-ingress.yaml.example / 08-ocrshim-sidecar.yaml.example
```

## 4. 部署后自检

```bash
scripts/preflight.sh --live
```

验收清单见 [argus-service/docs/delivery/ACCEPTANCE-CHECKLIST.md](https://github.com/yi-nology/argus-service/blob/main/docs/delivery/ACCEPTANCE-CHECKLIST.md)。

## 5. 升级 / 回滚

- **镜像换版**：改 image tag → rolling；**先跑 Migration Job**（破坏性 DDL 先行）。
- **二进制**：停机替换；SQLite 先备份 `data/argus.db`。
- **远端已发生的 commit/merge 无法用镜像回滚撤销**（只能平台 revert）。

## 6. 配置速查

| 变量 | 用途 |
|---|---|
| `ARGUS_APITOKEN` / `ARGUS_API_TOKENS` | API 鉴权（fail-closed） |
| `ARGUS_GITHUB_TOKEN` 等 | 平台凭据 |
| `OPENAI_*` | LLM |
| `ARGUS_LLM_EGRESS_CONSENT` | 出站同意（deny/require/yes） |
| `ARGUS_FIX_PUSH` | `1`/`direct`/`confirm` |
| `ARGUS_POLL_REPOS` | webhook 不可达轮询兜底 |

完整清单：[argus-service/README.md](https://github.com/yi-nology/argus-service/blob/main/README.md) §环境变量。
