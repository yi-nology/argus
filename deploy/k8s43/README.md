# argus @ k3s43（10.0.0.43 三节点 ARM64 集群）

v5.4.6 自 215（x86_64 docker）迁移至此的 k8s 部署（2026-11-05 切换，停机 ~10 分钟）。

## 形态
- 单副本 + **PostgreSQL**（v5.4.8=cdb28cf 起，ARGUS_DB_DSN=postgres://argus:<pw>@argus-pg:5432/argus，pg=argus-pg Deployment+PVC 5Gi+Secret argus-pg+ClusterIP，镜像 postgres:16-alpine 节点已有；SQLite→pg 迁移用 cmd/dbcopy 一次性工具（镜像内 /app/dbcopy，stage pod 跑）3216 行全拷；**旧 sqlite /data/argus.db 留 PVC 作回滚锚**——env 换回文件 DSN+image 换回 v5.4.7 即回滚（pg 期间新增任务不回流，有损））+ PVC（local-path 20Gi，work/memory/knowledge/ocr-home 仍在此；仅 db 挪走）+ Recreate 策略 —— 多副本须先切 MySQL（见 deploy/README-scaling）
- 节点 pin：master01（镜像与 PV 均在 master01 本地）
- 镜像：argus:v5.4.9-arm64-ocr（v5.4.9=ab8758e+d9e0ef0：多副本加固——迁移哨兵租约单飞 + webhook ensure 租约门控 + TryAcquire INSERT 冲突降噪）（v5.4.7=d0db25c：funcdiff 浅克隆 deepen 补全 + 总结评论 head 轮换浮顶；换芯层叠法：本地交叉编译 arm64 → FROM 旧镜像 COPY /app/argus）（=v5.4.6-arm64 + OCR v1.13.6 层；底座 Mac 原生 arm64 构建；集群无公网，`docker save --platform linux/arm64` → 43 `k3s ctr -n k8s.io images import`）
- 入站链路：om-gitlab → **215 前门**（systemd `argus-frontdoor`，python3 L4 转发 215:8899 → 43:30899）→ NodePort → pod:8899
  - 依据：om-gitlab → 10.60 网段全端口被封（实测 80/443/8080/30899 均 Failed to open TCP connection）
  - 回滚：`systemctl disable --now argus-frontdoor`（215）→ `docker start argus`（215 原容器 restart=no 保留）
- webhook 自愈：ARGUS_ENSURE_WEBHOOK=1 + ARGUS_PUBLIC_URL=http://10.0.0.215:8899，启动期对 48 轮询仓逐仓校准（boot 即 updated=41/created=1/unchanged=4）
- LLM/embedding：内部网关 llm-gateway.example.com:9080/v1（chat=qwen3.8-flash-2 / emb=qwen3-emb dim=4096；替代欠费的阿里云 token-plan）**；⚠️ 网关有 per-model RPM 限流，security 插件高峰可 429（插件级降级不阻塞主链，可配 repo_settings 关 security 或接受降级）

## 与 215 的差异（遗留项）
- ~~ARGUS_SEMSEARCH=0~~ **已于同日开启**（ARGUS_SEMSEARCH=1 + qwen3-emb dim=4096；215/43 的 kernel.db atoms=0 旧索引本不存在无需重建；MCP search_sem 实弹命中闭环。注意：atom_vectors 同 namespace 单一维度硬约束，换 embedding 模型须先清 atom_vectors 再换）
- ~~OCR 插件不可用~~ **同日已修复**：镜像 argus:v5.4.6-arm64-ocr（基础镜像 + 上游 v1.13.6 linux-arm64 预编译层，sha256 校验；Dockerfile.ocr 在本目录）；config.json 灌 PVC ocr-home/（provider=bailian 保留、url/key 指内部网关）；plugins.yaml cmd model→qwen3.8-flash-2；实弹报告头「插件: ocr,security」确认参与
- review-service sink/审计转发未接（215 docker 内网别名不可达；REVIEW_TOKEN 已从 env 移除）
- ARGUS_OTEL_ENDPOINT 未接（bianque-jaeger-1 为 215 docker 别名）

## 日常运维
- kubectl：本地 `~/.kube/config-k3s43`（VPN 直连 6443），断连时走 215 隧道：`ssh -N -L 16443:10.0.0.43:6443 10.0.0.215` + config-k3s43 server→https://127.0.0.1:16443
- 日志：`kubectl -n argus logs deploy/argus -f`
- 升级：Mac `make docker` 同款 Dockerfile + `--platform linux/arm64`，save→scp→import→`kubectl set image`

## 多副本形态（v5.4.9 实弹验证 2026-11-06）

- **存储形态（v5.4.9 实弹）**：`/data/work` = **per-pod emptyDir（8Gi 上限）**嵌套挂载覆盖 PVC 子路径（workcopy 瞬态、pod 死即回收、容量/IO 不与共享盘争抢——README-scaling 既定要求）；`/data/{memory,knowledge,ocr-home}` + 旧 sqlite 锚 = 共享 PVC（同节点 pin 前提下 kernel.db WAL 多进程安全）；pg 数据独立 PVC 5Gi
- **已验证**：`kubectl -n argus scale deploy argus --replicas=2` 双副本同启——ensure 单飞（一跑一 skipped_lease）、任务单实例认领（SKIP LOCKED，claimed 1/0，runner_id 唯一）、poller 选主租约 leader_leases 表、pkey 红字零
- **同节点约束**：local-path PV 不跨节点——多副本必须同 pin master01（镜像+PVC+kernel.db 都在）；memorykernel sqlite WAL+busy_timeout+单连接，同节点共享 PVC 多进程写安全
- **容量现实**：每副本 MAX_CONCURRENT_TASKS=4 → LLM 并发翻倍，网关 per-model RPM 限流（security 插件已撞过 429）是第一瓶颈，扩副本前先确认网关配额
- **当前在役形态**：replicas=2（10-06 起）；回单副本 `--replicas=1`

## 备份（2026-11-06 上线）

- **pg 备份**：CronJob `argus-pg-backup`（每天 03:30，postgres:16-alpine 节点已有镜像，零新组件）——`pg_dump -Fc` 全库压缩 → PVC `argus-pg-backup`（5Gi），循环保留 7 份；手动触发 `kubectl -n argus create job --from=cronjob/argus-pg-backup <name>`；恢复：`pg_restore -h argus-pg -U argus -d argus --clean <dump>`
- **MinIO 决策：不挂**——argus 全代码零 S3/OSS 消费面（grep 实锤）；报告/artifacts 在 pg TEXT（无上限）；work=emptyDir 瞬态；持久小文件=共享 PVC。对象存储的动机（跨节点共享/归档/S3 API）当前都不存在；触发条件=①跨节点多副本②长期归档合规③pg 备份外移——届时再挂（镜像 Mac 拉→save→scp→43 import，43 无公网）

