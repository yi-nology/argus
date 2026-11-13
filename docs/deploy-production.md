# 生产部署规范（P3-04，issue #29）

> 状态：骨架落地（HA Chart + Migration Job 拆分 + 备份恢复演练 + 滚动升级 runbook）。
> 两套形态并存：**Helm Chart**（`deploy/helm/argus`，新集群/规范化部署的单一入口）
> 与 **k3s43 手工形态**（`deploy/k8s43/`，在役集群的实弹记录）。本文是两者的
> 规范面；实弹细节以 `deploy/k8s43/README.md` 为准。

## 1. HA 拓扑（Chart 固化）

| 面 | 规范 | 收口位置 |
|---|---|---|
| 副本数 | `replicaCount: 2`（双副本在役口径） | `values.yaml` |
| 滚动策略 | `RollingUpdate` + `maxUnavailable: 0` / `maxSurge: 1`（容量不掉底） | `values.yaml updateStrategy` |
| 探活 | `/readyz` 单面；readiness 10s×6 / liveness 180s×60（与 k8s43 同参） | `values.yaml probes` |
| 资源配额 | requests 500m/512Mi，limits 2C/4Gi（k8s43 在役值为锚） | `values.yaml resources` |
| 存储 | 共享 PVC（memory/knowledge/ocr-home）+ per-pod `emptyDir` work 盘（8Gi） | `persistence` / `workDiskSize` |
| 敏感配置 | 一律 `existingSecret`（DSN/token），values 明面只放非敏感 env | `existingSecret` / `config` |

`helm lint` 与 `helm template` 是本 Chart 的最低验收（CI 未接 helm job，
PR 内本地跑；`pgRestore.enabled=true` 时恢复演练 Job 一并渲染）。

## 2. Migration Job 拆分（联动 v6.7.0 迁移语义）

- **形态**：`templates/migration-job.yaml` 是 `pre-install,pre-upgrade` helm hook
  （weight -5，成功即删）——`helm upgrade` 时 Job 先行，业务副本随后滚动。
- **语义联动**：v6.7.0 起迁移受迁移租约串行 + **fail-closed**（迁移失败拒绝启动）；
  Job 与滚动副本不会并发 DDL，副本侧 `storage.Open` 只校验不回写。
- **接入点（诚实边界）**：argus 主程序当前**没有** `-migrate-only` 子命令（迁移随
  `storage.Open` 启动执行）。缺省 `migrationJob.command` 是 **fail-closed 占位**
  ——显式 `exit 1` 拒绝假装成功；接入点落地后在 `values.migrationJob.command`
  覆写为 `["/app/argus", "-migrate-only"]`。在此之前，手工形态的等价顺序见 §4。

## 3. 备份与恢复演练

- **备份**：CronJob `*-pg-backup`（缺省每天 03:30）——`pg_dump -Fc` 全库压缩 →
  备份 PVC，循环保留 `pgBackup.retention`（缺省 7）份；与 k8s43 在役
  `80-pg-backup.yaml` 同形态。
- **恢复演练**（「有备份」≠「恢复出来能查」）：
  - Helm 形态：`helm upgrade --set pgRestore.enabled=true` → Job 走
    dump → 恢复进 scratch 库 → 行数对账 → 清理；
  - k3s43 手工形态：`deploy/k8s43/90-pg-restore-drill.sh`（断言 0 备份非空
    >1KB、断言 1 `pg_restore --exit-on-error` 全量成功、断言 2 恢复库行数
    ≥ 在役库——备份只会更旧，恢复库更少 = 备份滞后/数据面回退），全程不触碰
    在役库，演练完 drop scratch 库。
- **演练频率规范**：季度一次 + 每次重大升级前一次（本文即该规范的落点）。

## 4. 滚动升级 runbook

### 4.1 Helm 形态（规范顺序）

```bash
# 0) 升级前：确认备份新鲜（最近一次 CronJob 成功；重大升级前手动触发一次）
kubectl -n <ns> create job --from=cronjob/<rel>-pg-backup pre-upgrade-$(date +%s)

# 1) 升级（pre-upgrade hook 自动先跑 Migration Job——失败即中止，副本不动）
helm upgrade <rel> deploy/helm/argus -n <ns> -f <values>

# 2) 滚动确认（maxUnavailable=0：旧副本全部健康替换才收敛）
kubectl -n <ns> rollout status deploy/<rel> --timeout=300s

# 3) 抽验：/readyz 两副本就绪 + 迁移 Job 日志无 fail-closed 报错
kubectl -n <ns> get pods -l app.kubernetes.io/instance=<rel>
kubectl -n <ns> logs job/<rel>-migrate --tail=50
```

失败回退：`helm rollback <rel> <prev-revision> -n <ns>`（迁移已执行时不回滚
schema——数据迁移是单向前推的，回退只回应用层；见 §2 fail-closed 语义）。

### 4.2 k3s43 手工形态（在役等价步骤）

```bash
# 1) 升级前备份（等价于 §4.1 step 0）
kubectl -n argus create job --from=cronjob/argus-pg-backup pre-upgrade-$(date +%s)

# 2) 迁移先行：同镜像起一次性 Pod（启动即退出；迁移租约串行 + fail-closed 保护）
kubectl -n argus run migrate-$(date +%s) --rm -it --restart=Never \
  --image=argus:<new-tag> --env-from=secret/argus-env -- /app/argus <迁移入口>
#    （-migrate-only 接入点落地前，业务副本首启同样会做迁移——v6.7.0 起
#      租约串行保证不会两副本并发 DDL；本步是把失败面收敛到滚动之前）

# 3) 滚动（k8s43 实弹口径：set image 后 rollout status）
kubectl -n argus set image deploy/argus argus=argus:<new-tag>
kubectl -n argus rollout status deploy/argus --timeout=300s

# 4) 抽验：两副本 READY、/readyz 过、pkey/租约日志无红字
kubectl -n argus get pods
kubectl -n argus logs deploy/argus --tail=100 | grep -iE 'migrate|lease|error'
```

## 5. 完成度边界（骨架口径）

- 已落地：Chart 双副本拓扑与配额、Migration Job（hook + fail-closed 占位）、
  备份 CronJob、恢复演练（Helm Job + k3s43 脚本）、本文 runbook。
- 未含（后续批次）：`-migrate-only` 子命令接入（接入点已在 values 显式声明，
  落地前 Migration Job 按 fail-closed 拒绝假装成功）；Chart 的 CI lint job
  （当前 PR 本地 `helm lint`/`helm template` 验收）；跨集群 GitOps 化
  （kustomize 形态与 Helm 二选一时再定）。
