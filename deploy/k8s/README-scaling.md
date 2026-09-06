# Argus 多副本横向扩展指南（v3.3+）

Argus 的任务面与控制面在架构上已支持多副本共库部署：

## 架构支撑（代码已就绪）

| 关注点 | 机制 | 位置 |
|---|---|---|
| 任务抢占分片 | `ClaimNextPending` 原子 UPDATE 抢占（pending→running），多实例不重复消费 | agentkit/worker + internal/storage |
| 崩溃接管 | running 任务心跳续期；实例失联后 `StaleRunningAfter`(90s) 重置回 pending 被他实例接走 | agentkit/worker 心跳 + ResetRunningToPending |
| 滚动发布安全 | `RunnerID`（实例身份）+ `HeartbeatAt`——停机硬取消只影响本实例在途任务 | tasks 表 |
| 轮询任务源选主 | `worker.LeaderElector` + `leader_leases` 表租约（ttl 30s / 续约 10s，失联自动换主，优雅停机主动让位） | main.go pollElector |
| 工作副本隔离 | 每实例 `WorkRoot/instance-<id>/` 子目录——孤儿扫描只看本实例 entries，共享 root 会互删 | main.go wcRoot |
| Webhook 幂等 | delivery ID 唯一索引 + 同 SHA 双查，任意实例处理均幂等 | internal/service |

## 前置条件（必须）

1. **共享 DB 必须是 MySQL**（`ARGUS_DB_DSN` 指向网络库）。SQLite 文件库不支持多写——
   当前 04-deployment.yaml 的默认形态（SQLite + PVC + Recreate）就是单副本档，
   保持 replicas: 1 不变；切 MySQL 后才放开副本数。
2. `ARGUS_WORK_ROOT` 用 emptyDir（per-pod，天然隔离）；**不要**挂共享 PVC 给多副本
   （代码已按 `WorkRoot/instance-<id>` 隔离，但共享盘的容量与 IO 争抢没必要背）。

## 编排改法（04-deployment.yaml）

```yaml
spec:
  replicas: 2                      # 2-3 副本足够（瓶颈通常在 LLM 限速而非实例数）
  strategy:
    rollingUpdate:
      maxUnavailable: 0            # 滚动期间吞吐不降
      maxSurge: 1
  template:
    spec:
      affinity:
        podAntiAffinity:           # 分散到不同节点（可选）
          preferredDuringSchedulingIgnoredDuringExecution:
            - weight: 100
              podAffinityTerm:
                labelSelector:
                  matchLabels: { app.kubernetes.io/name: argus }
                topologyKey: kubernetes.io/hostname
      terminationGracePeriodSeconds: 90   # ≥ StaleRunningAfter 的让位链路：SIGTERM →
                                          # worker 排水 → poller 让位 → 在途任务自然收尾
      containers:
        - name: argus
          env:
            - name: ARGUS_MAX_CONCURRENT_TASKS   # 每实例 worker 数 × 副本数 ≤ LLM 限速承受力
              value: "2"
```

## 容量公式

```
总评审并发 ≈ replicas × ARGUS_MAX_CONCURRENT_TASKS
LLM 请求速率 ≈ 总并发 × 平均每任务调用数 / 任务时长
```

百炼等端点有 burst 限速：`ARGUS_LLM_RATE_LIMIT` 是**实例级**限速器，
多副本时按 `限速总额 / replicas` 下调单实例值（agentkit llm 429 处理会自动降级链，但别靠它扛容量规划）。

## 验证清单

- [ ] 两副本同起，只有一份打出 `agentkit.leader.gained`（另一份无）
- [ ] kill leader pod → ≤40s（ttl+interval）另一份 `agentkit.leader.gained`
- [ ] 滚动发布期间：在途任务由存活实例接手（tasks 表 runner_id 变更），报告不重复
- [ ] webhook 任意实例接收均只产生一个任务（event_id 幂等）
