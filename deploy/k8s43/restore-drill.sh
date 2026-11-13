#!/usr/bin/env bash
# restore-drill.sh —— pg 备份恢复演练（#29 P3-04 验收：备份+恢复演练脚本可用）
#
# 把 argus-pg-backup PVC 里最新（或指定）的 pg_dump 恢复进一次性 postgres Pod
# （独立空库，绝不碰在役 argus-pg），跑完整性查询（表存在/行数/哨兵）后自动清理。
# 演练目标：证明「备份文件可恢复 + 恢复后数据可查」，RTO 口径实测。
#
# 用法（在能 kubectl 到 43 集群的环境执行）：
#   ./deploy/k8s43/restore-drill.sh                  # 最新一份备份
#   DUMP=argus-20261113-033000.dump ./deploy/k8s43/restore-drill.sh
#   KEEP=1 ./deploy/k8s43/restore-drill.sh           # 演练 Pod 保留（事后 psql 进去看）
#
# 前置：kubectl 当前上下文指向 43 集群；argus-pg-backup PVC 有 dump 文件。
# 安全：恢复目标是 drill 库（新 Pod 内存/临时卷），在役库零写入。
set -euo pipefail

NS=${NS:-argus}
POD=argus-restore-drill
DRILL_DB=${DRILL_DB:-argus_drill}
DUMP=${DUMP:-}          # 空 = 取 PVC 里最新一份
KEEP=${KEEP:-0}         # 1 = 演练后保留 Pod 供事后检查
IMAGE=${IMAGE:-postgres:16-alpine}   # 与 80-pg-backup 同款（节点已有镜像，零拉取）

command -v kubectl >/dev/null || { echo "缺 kubectl" >&2; exit 1; }

echo "==> 清理旧演练残留（幂等）"
kubectl -n "$NS" delete pod "$POD" --ignore-not-found --wait=true >/dev/null

echo "==> 起演练 Pod（挂 backup PVC，独立 PGDATA 临时卷）"
kubectl -n "$NS" run "$POD" --restart=Never --image="$IMAGE" \
  --labels="app=$POD" \
  --overrides='{
    "spec": {
      "nodeSelector": {"kubernetes.io/hostname": "ubuntu-2404-k3s-master01"},
      "volumes": [
        {"name": "backup", "persistentVolumeClaim": {"claimName": "argus-pg-backup"}},
        {"name": "drill", "emptyDir": {}}
      ],
      "containers": [{
        "name": "'"$POD"'",
        "image": "'"$IMAGE"'",
        "command": ["/bin/sh", "-c", "sleep 3600"],
        "volumeMounts": [
          {"name": "backup", "mountPath": "/backup", "readOnly": true},
          {"name": "drill", "mountPath": "/pgdata"}
        ]
      }]
    }
  }' -- sleep 3600 >/dev/null

kubectl -n "$NS" wait --for=condition=Ready "pod/$POD" --timeout=120s >/dev/null

cleanup() {
  if [ "$KEEP" = "1" ]; then
    echo "==> KEEP=1：演练 Pod 保留（kubectl -n $NS exec -it $POD -- psql -U postgres）"
  else
    echo "==> 清理演练 Pod"
    kubectl -n "$NS" delete pod "$POD" --ignore-not-found --wait=false >/dev/null
  fi
}
trap cleanup EXIT

echo "==> 定位备份文件${DUMP:+（指定: $DUMP）}"
LATEST=$(kubectl -n "$NS" exec "$POD" -- sh -c \
  'ls -1t /backup/argus-*.dump 2>/dev/null | head -1' | tr -d '\r')
if [ -z "$LATEST" ]; then
  echo "❌ PVC 里没有 argus-*.dump——备份 CronJob 从未成功？" >&2
  exit 2
fi
if [ -n "$DUMP" ]; then
  LATEST="/backup/$DUMP"
  kubectl -n "$NS" exec "$POD" -- test -f "$LATEST" || { echo "❌ 指定 dump 不存在: $DUMP" >&2; exit 2; }
fi
echo "    恢复源: $LATEST ($(kubectl -n "$NS" exec "$POD" -- du -h "$LATEST" | cut -f1))"

echo "==> 初始化演练库 + pg_restore"
# initdb 到临时卷（全新实例，与在役 PGDATA 零交集）；trust 认证仅本 Pod 内回环。
kubectl -n "$NS" exec "$POD" -- sh -c "
  set -e
  export PATH=/usr/local/bin:\$PATH
  initdb -D /pgdata/drill -U postgres >/dev/null 2>&1
  pg_ctl -D /pgdata/drill -o '-k /tmp -p 5433' -w start >/dev/null
  createdb -h /tmp -p 5433 -U postgres $DRILL_DB
  pg_restore -h /tmp -p 5433 -U postgres -d $DRILL_DB --no-owner --exit-on-error '$LATEST'
" || { echo "❌ pg_restore 失败（备份文件损坏或版本不兼容）" >&2; exit 3; }

echo "==> 完整性查询"
kubectl -n "$NS" exec "$POD" -- psql -h /tmp -p 5433 -U postgres -d "$DRILL_DB" -v ON_ERROR_STOP=1 -tAc "
SELECT 'tasks=' || count(*) FROM tasks
UNION ALL SELECT 'finding_prints=' || count(*) FROM finding_prints
UNION ALL SELECT 'schema_migrations=' || count(*) FROM schema_migrations
UNION ALL SELECT 'repo_settings=' || count(*) FROM repo_settings;
" || { echo "❌ 恢复后查询失败" >&2; exit 4; }

# 关键哨兵：一次性迁移记录在 = 恢复的是完整迁移态库
SENTINEL=$(kubectl -n "$NS" exec "$POD" -- psql -h /tmp -p 5433 -U postgres -d "$DRILL_DB" -tAc \
  "SELECT count(*) FROM schema_migrations WHERE version IN ('fingerprint_v2','print_alias_pr_scope')")
if [ "$SENTINEL" != "2" ]; then
  echo "❌ 迁移哨兵缺失（$SENTINEL/2）——备份来自未迁移完成的库？" >&2
  exit 5
fi

kubectl -n "$NS" exec "$POD" -- pg_ctl -D /pgdata/drill stop >/dev/null 2>&1 || true
echo "✅ 恢复演练通过：$LATEST → $DRILL_DB（表可查、哨兵 2/2、在役库零写入）"
