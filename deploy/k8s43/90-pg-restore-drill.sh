#!/usr/bin/env bash
# 90-pg-restore-drill.sh —— pg 备份恢复演练（P3-04，#29）
#
# 恢复能力不是「有备份」，是「恢复出来能查」。本脚本把最新备份恢复进
# scratch 库、跑行数对账断言、清理现场——全程不触碰在役 argus 库。
# 演练频率规范（季度/升级前）见 docs/deploy-production.md §备份恢复。
#
# 前置：kubectl 已连 43 集群（~/.kube/config-k3s43 或隧道）；argus-pg-backup
# PVC 里有至少一份 dump（80-pg-backup.yaml CronJob 产物）。
#
# 用法：./90-pg-restore-drill.sh [--db argus_drill2]
set -euo pipefail

NAMESPACE="${NAMESPACE:-argus}"
DRILL_DB="${DRILL_DB:-argus_drill}"
PG_POD="${PG_POD:-$(kubectl -n "$NAMESPACE" get pods -l app=argus-pg -o jsonpath='{.items[0].metadata.name}')}"
BACKUP_PVC="${BACKUP_PVC:-argus-pg-backup}"

while [ $# -gt 0 ]; do
  case "$1" in
    --db) DRILL_DB="$2"; shift 2 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
done

echo "==> 恢复演练开始（ns=$NAMESPACE pg_pod=$PG_POD drill_db=${DRILL_DB}）"

# 演练断言 0：备份非空——PVC 里最新 dump 存在且 >1KB（空 dump 是更深的故障）
LATEST=$(kubectl -n "$NAMESPACE" exec "$PG_POD" -- sh -c \
  'ls -1t /backup/argus-*.dump 2>/dev/null | head -n1 || true')
[ -n "$LATEST" ] || { echo "❌ 断言0 失败：备份 PVC 无 dump（CronJob 没跑？）" >&2; exit 1; }
SIZE=$(kubectl -n "$NAMESPACE" exec "$PG_POD" -- sh -c "stat -c %s /backup/$(basename "$LATEST") 2>/dev/null || stat -f %z /backup/$(basename "$LATEST")")
[ "$SIZE" -gt 1024 ] || { echo "❌ 断言0 失败：最新 dump 仅 ${SIZE}B（疑似空备）" >&2; exit 1; }
echo "    断言0 通过：最新备份 $(basename "$LATEST")（${SIZE}B）"

# 演练恢复：scratch 库全量 restore（--exit-on-error=恢复必须是完整的）
kubectl -n "$NAMESPACE" exec "$PG_POD" -- sh -c \
  "dropdb --if-exists -U argus '$DRILL_DB' && createdb -U argus '$DRILL_DB' && \
   pg_restore -U argus -d '$DRILL_DB' --no-owner --exit-on-error /backup/$(basename "$LATEST")"
echo "    断言1 通过：pg_restore --exit-on-error 全量恢复成功"

# 对账断言：核心表存在且行数非劣（在役库行数 ≤ 恢复库行数——备份只会更旧）
LIVE_TASKS=$(kubectl -n "$NAMESPACE" exec "$PG_POD" -- psql -U argus -d argus -tAc "SELECT COUNT(*) FROM tasks")
DRILL_TASKS=$(kubectl -n "$NAMESPACE" exec "$PG_POD" -- psql -U argus -d "$DRILL_DB" -tAc "SELECT COUNT(*) FROM tasks")
echo "    对账：tasks 在役=$LIVE_TASKS 恢复=$DRILL_TASKS"
[ "$DRILL_TASKS" -ge "$LIVE_TASKS" ] || \
  { echo "❌ 断言2 失败：恢复库行数少于在役库（备份滞后于删表？数据面回退？）" >&2; exit 1; }
echo "    断言2 通过：恢复库行数不劣于在役库"

# 清理现场（演练库 drop；dump 保留）
kubectl -n "$NAMESPACE" exec "$PG_POD" -- dropdb -U argus "$DRILL_DB"
echo "✅ 恢复演练 PASSED（scratch 库已清理，备份保留）"
