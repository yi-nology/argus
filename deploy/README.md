# Argus 部署指南（deploy/）

两种部署形态，二选一：

| 模式 | 适用 | 目录 |
|---|---|---|
| docker-compose | 单机学习/内网（Mac、跳板机、NAS）| `deploy/docker-compose/` |
| k8s (kustomize) | studio/test 集群（buildkit 推 Harbor + ArgoCD 可直接对接）| `deploy/k8s/` |

## 模式一 · docker-compose

```bash
cd deploy/docker-compose
cp .env.example .env          # 填入凭证（至少 ARGUS_APITOKEN + 一个平台 token + OPENAI_*）
docker compose up -d --build  # 首次会从仓库根构建镜像（约 1 分钟）
curl http://127.0.0.1:8899/healthz
```

- SQLite 在 `./data/`；插件配置改 `./plugins.yaml` 后 `docker compose restart` 生效
- 平台 → 本机 webhook 不可达（NAT/VPN 环境，Mac 试点实测如此）：启用 `.env` 里的
  `ARGUS_POLL_INTERVAL` / `ARGUS_POLL_REPOS` 轮询兜底，零功能损失
- 日志轮转已配置（50m×5）
- **ocrshim sidecar（v3.9.0，可选）**：compose 已内置 `ocrshim` 服务（与 argus 同镜像双
  二进制，共享 `./data` 卷）——plugins.yaml 的 `ocr` 条目指向 `http://ocrshim:8090/mcp`
  即启用 OCR MCP 通道；OCR 二进制预编译到 `./bin/ocr-amd64`（scripts/install-ocr.sh）。
  argus 与 ocrshim 必须同 tag 滚动。OCR/delegate/sink 全部开关见 `.env.example` 注释

## 模式二 · k8s (kustomize)

```bash
# 0) 镜像：本地构建并推 Harbor（或先在本节点 load；tag 以实际版本为准，如 v3.9.1）
make docker && docker tag argus:v3.9.1 harbor.example.com/enjoydream/argus:v3.9.1 && docker push ...
# 并把 deploy/k8s/kustomization.yaml 的 images.newTag 改为 Harbor 地址

# 1) 修改凭证
vi deploy/k8s/01-secret.yaml   # 占位凭证必改

# 2) 部署
kubectl apply -k deploy/k8s/

# 3) 验证
kubectl -n argus get pods
kubectl -n argus logs -f deploy/argus | grep http_ready
curl http://<nodeIP>:<port>/healthz
```

### 清单说明

| 文件 | 内容 | 要点 |
|---|---|---|
| 01-secret | APIToken/平台凭证/LLM key | ⚠️ 占位必改；勿提交真实值 |
| 02-configmap | 非敏感运行配置 | 轮询兜底默认开启（20s），接 Ingress 后可关 |
| 03-pvc | 5Gi RWO | SQLite + 工作副本沙箱 |
| 04-deployment | 单副本 Deployment | **replicas=1 + Recreate**（SQLite 单写模型，见文件头注释）；90s 优雅终止 > worker 60s grace；Prometheus 注解已加；多副本路径见下 |
| 05-plugins-configmap | 插件声明 | 改完 `kubectl rollout restart deployment/argus -n argus` |
| 06-service | ClusterIP | webhook 需要时改 NodePort 30883 |
| 07-ingress.yaml.example | nginx 形态参考 | APISIX 集群不用它，走 APISIX 路由；未加入 kustomization |
| 08-ocrshim-sidecar.yaml.example | OCR MCP sidecar 粘贴块 | 可选；粘贴进 04-deployment 的 containers（同 Pod 共享 PVC/loopback），镜像需按文件头步骤加装 OCR 二进制 |

### v3.9 形态（可选装配）

- **OCR MCP 通道**：把 [08-ocrshim-sidecar.yaml.example](k8s/08-ocrshim-sidecar.yaml.example)
  的容器块粘贴进 04-deployment，按其文件头步骤构建含 OCR 二进制的镜像，05-plugins-configmap
  的 `ocr` 条目指向 `http://127.0.0.1:8090/mcp`。**argus 与 ocrshim 必须同 tag 滚动**
  （同一镜像双二进制；旧镜像无 `/app/ocrshim`，sidecar 会崩溃循环）
- **OCR delegate 委托前置**：镜像内含 OCR 二进制时，取消 02-configmap 的
  `ARGUS_OCR_BIN` 注释即可（与 sidecar 无关，可并用：delegate 供确定性输入、sidecar 供独立审查）
- **结果上报 sink**：取消 02-configmap 的 `ARGUS_SINK_URL` 注释 + 01-secret 加
  `ARGUS_SINK_TOKEN`，审查完成后 push 给 review-service（承接端见 review-service 仓
  argusingest 模块；URL 非空即启用，失败只告警不阻断审查）

### 工作区对接（后续演进）

- **studio 集群**：镜像推 Harbor → `fuxi/argocd-manifests` 里加 argus 的 Kustomize overlay
  （指向 `deploy/k8s`，`namespace` 覆盖为 enjoydream 或独立 ns），ArgoCD 自动同步
- **webhook 地址**：Ingress（argus.example.com）或 NodePort 30883，二选一后
  `./scripts/setup-webhook.sh <owner/repo>` 注册，并关闭轮询（删 ConfigMap 的 POLL_* 后 restart）
- **监控**：Deployment 已带 `prometheus.io/scrape` 注解，kube-prometheus-stack 自动发现
  `argus_tasks_pending/running` 等指标

## 通用注意事项

- **副本数**：SQLite 模式 replicas 必须为 1；横向扩副本必须切 MySQL（`ARGUS_DB_DSN` 换
  DSN 即可）——v3.3 起支持多活副本（poller 租约选主 + 工作副本按实例隔离），容量公式与
  验证清单见 **[k8s/README-scaling.md](k8s/README-scaling.md)**
- **bot 账号**：`ARGUS_GITEA_TOKEN` 使用独立 bot 账号（试点为 friday，非 PR 作者），
  blocking 评审才能以 official 状态落地（见 PILOT.md §7）
- **升级**：`make docker` 重建 → 换 tag → `kubectl rollout restart`；SQLite schema 由
  Argus 启动时 AutoMigrate 自动演进（v3.3 新增 `qa_sessions` / `leader_leases` 表同机制）
