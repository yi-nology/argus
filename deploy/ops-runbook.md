# Argus 运维手册

本文档覆盖 Argus 生产环境常见故障场景的诊断与修复流程。每个场景遵循 **症状 -> 诊断 -> 解决 -> 预防** 四段式结构。

---

## 1. 任务卡在 running 状态

### 症状

- `GET /tasks?status=running` 返回长时间未更新的任务
- 任务 `heartbeat_at` 停留在过去的时间点
- 新任务排队但 worker 不消费

### 诊断

```bash
# 1. 查看 running 任务的心跳时间
curl -s -H "Authorization: Bearer $ARGUS_APITOKEN" \
  "$ARGUS_ADDR/tasks?status=running" | jq '.tasks[] | {task_id, heartbeat_at, runner_id, created_at}'

# 2. 检查日志中心跳续期是否正常
grep "argus.worker.heartbeat" /var/log/argus.log | tail -5

# 3. 查看是否有 stale reset 日志
grep "argus.worker.stale_running_reset" /var/log/argus.log | tail -5
```

**根因判定**：
- `heartbeat_at` 超过 90 秒未更新 -> worker 进程已崩溃或被 OOM Kill
- `runner_id` 为空 -> 旧版本遗留数据（v3.0.1 前无心跳字段）
- worker 日志有 `argus.worker.claim_failed` -> 数据库连接异常

### 解决

Argus 内建自动恢复机制：心跳协程每 20 秒续期，超过 90 秒未续的任务自动重置回 `pending`。

```bash
# 方案 A：等待自动恢复（最多 90 秒）
# 心跳协程会自动将过期 running 任务重置为 pending

# 方案 B：手动重启触发恢复
# docker compose
docker compose restart argus
# K8s
kubectl rollout restart deployment/argus -n argus

# 方案 C：直接操作数据库（紧急）
# SQLite
sqlite3 /data/argus.db "UPDATE tasks SET status='pending', runner_id='', heartbeat_at=NULL WHERE status='running' AND (heartbeat_at IS NULL OR heartbeat_at < datetime('now', '-90 seconds'));"
# MySQL
mysql -u argus -p argus_db -e "UPDATE tasks SET status='pending', runner_id='', heartbeat_at=NULL WHERE status='running' AND (heartbeat_at IS NULL OR heartbeat_at < DATE_SUB(NOW(), INTERVAL 90 SECOND));"
```

### 预防

- 确保 `terminationGracePeriodSeconds`（K8s）大于 worker 优雅停机 grace（默认 60 秒），推荐 90 秒
- 监控 `argus_task_total{status="running"` 的积压数量
- OOM 场景：调高 Pod memory limits（默认 512Mi）

---

## 2. LLM 提供商返回 429（速率限制）

### 症状

- 日志出现 `argus.plugin.failed` 且 error 包含 `429` / `rate limit` / `too many requests`
- 任务状态变为 `failed`，`fail_reason` 包含 `调用失败（已重试 N 次）`
- 插件熔断器被触发：日志出现 `argus.plugin.circuit_open`

### 诊断

```bash
# 1. 查看失败任务的 fail_reason
curl -s -H "Authorization: Bearer $ARGUS_APITOKEN" \
  "$ARGUS_ADDR/tasks?status=failed&limit=5" | jq '.tasks[] | {task_id, fail_reason}'

# 2. 查看 LLM token 消耗指标（Prometheus）
# argus_llm_tokens_total{stage, model}

# 3. 检查当前限速配置
echo "ARGUS_LLM_RATE_LIMIT=$ARGUS_LLM_RATE_LIMIT"
echo "ARGUS_LLM_RATE_BURST=$ARGUS_LLM_RATE_BURST"
echo "ARGUS_MAX_CONCURRENT_TASKS=$ARGUS_MAX_CONCURRENT_TASKS"
```

### 解决

```bash
# 方案 A：降低全局 LLM 请求速率（推荐）
# 在 .env 中设置（单位：requests/sec）
ARGUS_LLM_RATE_LIMIT=2.0      # 每秒最多 2 次 LLM 请求
ARGUS_LLM_RATE_BURST=3        # 突发容量（默认等于 MaxConcurrentTasks）

# 方案 B：降低并发任务数
ARGUS_MAX_CONCURRENT_TASKS=1   # 单任务串行

# 方案 C：插件熔断器已自动恢复
# 连续 3 次失败 -> 熔断 5 分钟 -> 半开探测 1 次 -> 成功即恢复
# 无需手动干预，等待 5 分钟后自动探测恢复
```

### 预防

- 根据 LLM 提供商的 RPM/TPM 配额设置 `ARGUS_LLM_RATE_LIMIT`
- 百炼等有 burst limit 的 API，同时调低 `ARGUS_MAX_CONCURRENT_TASKS`
- 监控 `argus_plugin_failures_total` 和 429 相关日志告警

---

## 3. SQLite 数据库持续增长

### 症状

- `argus.db` 文件体积持续增大（数 GB 级别）
- 查询变慢，webhook 响应延迟增加
- 磁盘告警

### 诊断

```bash
# 1. 查看数据库文件大小
ls -lh /data/argus.db

# 2. 查看各状态任务数量
sqlite3 /data/argus.db "SELECT status, COUNT(*), SUM(LENGTH(report)+LENGTH(result_json)+LENGTH(debug_json)) as total_bytes FROM tasks GROUP BY status;"

# 3. 查看最老的任务时间
sqlite3 /data/argus.db "SELECT MIN(created_at), MAX(created_at) FROM tasks;"

# 4. 查看 finding_prints 表大小
sqlite3 /data/argus.db "SELECT COUNT(*), SUM(LENGTH(comment)) FROM finding_prints;"
```

### 解决

```bash
# 方案 A：启用自动清理（推荐）
# 设置任务保留天数，超期已终结任务（done/failed/stale）启动时 + 每日自动清理
ARGUS_TASK_RETENTION_DAYS=30   # 保留 30 天

# 方案 B：手动清理历史任务
sqlite3 /data/argus.db "DELETE FROM tasks WHERE status IN ('done','failed','stale') AND created_at < datetime('now', '-30 days');"

# 方案 C：SQLite VACUUM 回收空间（需停机或独占访问）
sqlite3 /data/argus.db "VACUUM;"
# docker compose 场景：
docker compose stop argus
docker compose run --rm argus sqlite3 /data/argus.db "VACUUM;"
docker compose start argus

# 方案 D：迁移到 MySQL（见场景 4）
```

### 预防

- 生产环境必须设置 `ARGUS_TASK_RETENTION_DAYS`（推荐 30）
- `ARGUS_DEBUG=false`（默认）避免留存大量调试产物
- 定期监控磁盘使用率

---

## 4. SQLite 迁移到 MySQL

### 症状

- 单实例 SQLite 无法满足并发/容量需求
- 需要多副本部署（MySQL 支持多 writer）

### 诊断

```bash
# 确认当前使用 SQLite
grep ARGUS_DB_DSN .env
# 输出 argus.db -> SQLite

# 确认目标 MySQL 可达
mysql -h $MYSQL_HOST -P $MYSQL_PORT -u $MYSQL_USER -p -e "SELECT 1"
```

### 解决

```bash
# 1. 停止 Argus
docker compose stop argus   # 或 kubectl scale deployment argus --replicas=0 -n argus

# 2. 导出 SQLite 数据
sqlite3 /data/argus.db .dump > argus_dump.sql

# 3. 创建 MySQL 数据库和用户
mysql -h $MYSQL_HOST -u root -p -e "
CREATE DATABASE argus_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'argus'@'%' IDENTIFIED BY 'strong_password';
GRANT ALL PRIVILEGES ON argus_db.* TO 'argus'@'%';
FLUSH PRIVILEGES;
"

# 4. 导入数据（SQLite dump 需要少量兼容性调整）
# 替换 SQLite 特有语法
sed -i '' 's/AUTOINCREMENT/AUTO_INCREMENT/g' argus_dump.sql
sed -i '' 's/INTEGER PRIMARY KEY AUTOINCREMENT/INT PRIMARY KEY AUTO_INCREMENT/g' argus_dump.sql
sed -i '' 's/INTEGER/INT/g' argus_dump.sql
# 移除 SQLite PRAGMA 语句
sed -i '' '/^PRAGMA/d' argus_dump.sql
# 导入
mysql -h $MYSQL_HOST -u argus -p argus_db < argus_dump.sql

# 5. 切换 DSN（Argus 启动时会自动执行 AutoMigrate 升级 LONGTEXT）
# .env 修改：
ARGUS_DB_DSN="argus:strong_password@tcp(mysql-host:3306)/argus_db?charset=utf8mb4&parseTime=True&loc=Local"
# 也支持 mysql:// 前缀：
# ARGUS_DB_DSN="mysql://argus:strong_password@tcp(mysql-host:3306)/argus_db"

# 6. 启动 Argus（自动迁移 + LONGTEXT 升级）
docker compose start argus

# 7. 验证
curl -s -H "Authorization: Bearer $ARGUS_APITOKEN" "$ARGUS_ADDR/tasks?limit=1" | jq .
curl -s "$ARGUS_ADDR/healthz"
```

**重要说明**：
- Argus 启动时自动执行 `AutoMigrate`，会创建缺失的表/索引
- MySQL 下 GORM `type:text` 生成 `TEXT`（64KB 上限），Argus 自动升级为 `LONGTEXT`（`report`/`result_json`/`fail_reason`/`artifacts_json`/`debug_json`/`comment` 列）
- 迁移后可将 `ARGUS_MAX_CONCURRENT_TASKS` 调高（MySQL 支持多连接并发写入）

### 预防

- SQLite 模式仅限学习/单机环境，生产环境使用 MySQL
- SQLite 模式禁止 `scale > 1`（单写串行是幂等/去重的正确性基础）

---

## 5. Webhook 不接收事件

### 症状

- PR 创建/更新后 Argus 无反应
- `GET /tasks` 无新任务产生
- 平台 webhook 配置页面显示投递失败

### 诊断

```bash
# 1. 确认 Argus 在线
curl -s "$ARGUS_ADDR/healthz"

# 2. 检查平台配置是否完整
# 每个平台需要 URL + TOKEN + SECRET 三项
echo "ARGUS_GITEA_URL=$ARGUS_GITEA_URL"
echo "ARGUS_GITEA_TOKEN=${ARGUS_GITEA_TOKEN:+***已设置***}"
echo "ARGUS_GITEA_SECRET=${ARGUS_GITEA_SECRET:+***已设置***}"

# 3. 检查日志中的 webhook 相关事件
grep "argus.webhook" /var/log/argus.log | tail -20

# 4. 常见日志关键词：
# - "argus.webhook.rejected_no_secret" -> SECRET 未配置（fail-closed 拒绝）
# - "argus.webhook.signature_rejected"  -> 签名校验失败（SECRET 不匹配）
# - "事件类型不在白名单"                -> 非 PR 事件（评论/push 等被过滤）
# - "bot 自身事件"                      -> bot username 匹配（防自我触发）
# - "draft PR 默认不审"                 -> Draft PR 被跳过

# 5. 手动测试 webhook 端点可达性
curl -s -X POST "$ARGUS_ADDR/webhook/gitea" -H "Content-Type: application/json" -d '{}'
# 应返回 401（签名校验失败）而非连接超时
```

### 解决

```bash
# 问题 1：SECRET 未配置
# 症状：日志 "argus.webhook.rejected_no_secret"
# Argus v3.0.2 起 fail-closed：空 SECRET 直接拒绝 webhook
ARGUS_GITEA_SECRET="your_webhook_secret_here"

# 问题 2：签名校验失败
# 症状：日志 "argus.webhook.signature_rejected"，平台返回 401
# 确认 Argus 的 SECRET 与平台 webhook 配置中的 Secret 一致
# Gitea: 仓库设置 -> Webhooks -> 编辑 -> Secret
# GitHub: Settings -> Webhooks -> Edit -> Secret

# 问题 3：网络不可达
# 平台无法访问 Argus 的 webhook URL
# 方案 A：配置公网可达的 URL + 反向代理
# 方案 B：启用轮询兜底
ARGUS_POLL_INTERVAL=60s
ARGUS_POLL_REPOS="gitea:owner/repo1,gitea:owner/repo2"

# 问题 4：事件类型被过滤
# 确认平台发送的是 PR 事件（opened/synchronize/reopened），不是评论/push 事件

# 问题 5：Content-Length 超限
# 默认 1MB，超大 PR body 会被拒绝
ARGUS_WEBHOOK_MAX_BODY=2097152   # 调高到 2MB
```

### 预防

- 部署后立即测试：在平台上发一个测试 PR，确认 Argus 有响应
- 监控 `argus_webhook_accepted_total` 指标
- webhook 不可达时使用 `ARGUS_POLL_*` 轮询兜底

---

## 6. 插件熔断器卡在 open 状态

### 症状

- 日志持续出现 `argus.plugin.circuit_open`
- 任务报告中出现 `<plugin> 未参与（熔断中）`
- 插件明明已修复但仍不参与审查

### 诊断

```bash
# 1. 查看哪些插件被熔断
curl -s -H "Authorization: Bearer $ARGUS_APITOKEN" \
  "$ARGUS_ADDR/agents" | jq '.agents[] | {name, enabled}'

# 2. 查看最近的插件失败日志
grep "argus.plugin.failed" /var/log/argus.log | tail -10

# 3. 理解熔断机制
# 连续 3 次失败 -> 熔断 5 分钟 -> 冷却期过后自动进入半开状态
# 半开状态放行 1 个探测请求：
#   - 成功 -> 恢复正常
#   - 失败 -> 重新熔断 5 分钟
```

**熔断器参数**（硬编码，无需配置）：
- `BreakerTripThreshold = 3`：连续失败 3 次触发熔断
- `BreakerCooldown = 5 * time.Minute`：熔断冷却期 5 分钟

### 解决

```bash
# 方案 A：等待自动恢复（推荐）
# 5 分钟冷却期后自动进入半开探测，1 次成功即恢复

# 方案 B：重启 Argus（熔断器状态在内存中，重启即清零）
docker compose restart argus

# 方案 C：排查根因
# 如果插件持续失败，需修复根因：
# - LLM 429 -> 调低 ARGUS_LLM_RATE_LIMIT
# - CLI 插件二进制缺失 -> 重新安装
# - Remote 插件 endpoint 不可达 -> 检查网络/服务状态
```

### 预防

- 修复插件根因后重启一次 Argus 清零熔断计数
- 监控 `argus_plugin_failures_total` 持续告警

---

## 7. LLM 输出被截断

### 症状

- 日志出现 `输出被截断（finish_reason=length）`
- JSON 解析失败：`JSON 解析失败（已带错误重试 1 次）`
- 任务 `fail_reason` 包含 `截断` 或 `JSON`
- 审查报告不完整或降级为 COMMENT

### 诊断

```bash
# 1. 查看当前 MaxTokens 配置
echo "OPENAI_MAX_TOKENS=$OPENAI_MAX_TOKENS"       # 单次生成上限
echo "ARGUS_MODEL_CONTEXT_TOKENS=$ARGUS_MODEL_CONTEXT_TOKENS"  # 模型窗口

# 2. 查看失败任务详情
curl -s -H "Authorization: Bearer $ARGUS_APITOKEN" \
  "$ARGUS_ADDR/tasks/$TASK_ID" | jq '{fail_reason, debug}'

# 3. 开启调试模式查看 R1/R2/R3 原始输出
ARGUS_DEBUG=true
# 重启后触发任务，查看 debug_json
curl -s -H "Authorization: Bearer $ARGUS_APITOKEN" \
  "$ARGUS_ADDR/tasks/$TASK_ID" | jq '.debug'
```

### 解决

```bash
# 方案 A：调高 MaxTokens（推荐）
# 对于推理模型（如 mimo），需 >= 4096
OPENAI_MAX_TOKENS=8192

# 方案 B：设置正确的模型窗口
# Argus 从窗口自动派生各上限：
#   MaxDiffChars = 窗口 35% × 4 chars/token（最小 10k）
#   TaskTokenBudget = 窗口 60%
#   MaxTokens = max(4096, 窗口 5%)
# 换了大窗口模型后只需改这一个值：
ARGUS_MODEL_CONTEXT_TOKENS=131072   # 128k（默认值）

# 方案 C：保持默认值（自动派生）
# 不设置 OPENAI_MAX_TOKENS 时，Argus 按 max(4096, 窗口×5%) 自动计算
# 128k 窗口 -> MaxTokens = 6400，一般够用
```

**重要说明**：Argus 有截断重试机制——检测到 `finish_reason=length` 后自动将 MaxTokens 提升到 1.5 倍重试一次，并追加精简指令要求模型压缩输出。

### 预防

- `ARGUS_MODEL_CONTEXT_TOKENS` 必须与实际模型窗口匹配
- 推理模型（mimo 等）的输出 token 消耗更高，`OPENAI_MAX_TOKENS` 不要低于 4096
- 监控 `argus_llm_tokens_total` 的 completion 占比

---

## 8. 备份与恢复

### SQLite 备份

```bash
# 方案 A：文件复制（需停机或无写入时）
docker compose stop argus
cp /data/argus.db /backup/argus-$(date +%Y%m%d).db
docker compose start argus

# 方案 B：SQLite 在线备份（不停机）
sqlite3 /data/argus.db ".backup '/backup/argus-$(date +%Y%m%d).db'"

# 方案 C：WAL checkpoint + 复制（确保一致性）
sqlite3 /data/argus.db "PRAGMA wal_checkpoint(TRUNCATE);"
cp /data/argus.db /backup/argus-$(date +%Y%m%d).db
# 同时备份 WAL 和 SHM（如果存在）
cp /data/argus.db-wal /backup/ 2>/dev/null
cp /data/argus.db-shm /backup/ 2>/dev/null
```

### SQLite 恢复

```bash
docker compose stop argus
cp /backup/argus-20260901.db /data/argus.db
# 清理 WAL/SHM（如有）
rm -f /data/argus.db-wal /data/argus.db-shm
docker compose start argus
```

### MySQL 备份

```bash
# 全量备份
mysqldump -h $MYSQL_HOST -u argus -p --single-transaction argus_db > argus-$(date +%Y%m%d).sql

# 定时备份（crontab）
0 3 * * * mysqldump -h $MYSQL_HOST -u argus -p'password' --single-transaction argus_db | gzip > /backup/argus-$(date +\%Y\%m\%d).sql.gz
```

### MySQL 恢复

```bash
mysql -h $MYSQL_HOST -u argus -p argus_db < argus-20260901.sql
# 或从压缩备份
gunzip < argus-20260901.sql.gz | mysql -h $MYSQL_HOST -u argus -p argus_db
```

---

## 9. 升级 Argus 版本

### Docker Compose 升级

```bash
# 1. 备份数据库（见场景 8）

# 2. 拉取新镜像
docker compose pull   # 或 docker build -t argus:新版 .

# 3. 更新 compose.yaml 中的镜像版本
# image: argus:v3.0.1 -> image: argus:v3.1.0

# 4. 滚动更新（Argus 有两阶段优雅停机）
docker compose up -d
# 旧实例：停止领取新任务 -> 在途任务 grace 60s 自然完成 -> 超时硬取消
# 新实例：启动 -> ResetRunningToPending（心跳过期的 running 任务重置回 pending）-> 开始消费

# 5. 验证
curl -s "$ARGUS_ADDR/healthz"
curl -s "$ARGUS_ADDR/readyz"
docker compose logs --tail=20 argus
```

### K8s 滚动升级

```bash
# 1. 备份数据库

# 2. 更新镜像
# 编辑 deploy/k8s/04-deployment.yaml 中的 image
# 或直接 kubectl set image：
kubectl set image deployment/argus argus=harbor.example.com/enjoydream/argus:v3.1.0 -n argus

# 3. 监控滚动状态
kubectl rollout status deployment/argus -n argus

# 4. 验证
kubectl get pods -n argus
kubectl logs -f deployment/argus -n argus --tail=20
```

**重要说明**：
- K8s Deployment 使用 `Recreate` 策略（非 RollingUpdate），因为 SQLite + RWO PVC 不支持多副本
- `terminationGracePeriodSeconds: 90` > 优雅停机 grace 60s，确保在途审查自然完成
- 如果升级后发现严重问题，快速回滚：`kubectl rollout undo deployment/argus -n argus`

### 预防

- 升级前必须备份数据库
- 先在测试环境验证新版本
- 查看 CHANGELOG.md 确认是否有破坏性变更（如数据库 schema 变更）

---

## 10. 调试失败的审查任务

### 症状

- 任务状态为 `failed`，需要定位失败原因
- 审查结果不符合预期，需要追踪各阶段产出

### 诊断

```bash
# 1. 获取任务详情
curl -s -H "Authorization: Bearer $ARGUS_APITOKEN" \
  "$ARGUS_ADDR/tasks/$TASK_ID" | jq '.'

# 2. 关键字段解读
# .status    -> done/failed/stale
# .verdict   -> approve/comment/request_changes
# .fail_reason -> 失败原因（failed 时有值）
# .result_json -> 结构化结果（findings/compliance_report/plugins/degraded）
# .report    -> 渲染后的 markdown 报告
# .debug     -> 调试产物（ARGUS_DEBUG=true 时有值）
# .tokens_used -> 本任务 LLM token 消耗

# 3. 开启调试模式查看各阶段产出
# 设置 ARGUS_DEBUG=true 后重启，再次触发任务
ARGUS_DEBUG=true

# 4. 查看 debug_json 中的各阶段产出
curl -s -H "Authorization: Bearer $ARGUS_APITOKEN" \
  "$ARGUS_ADDR/tasks/$TASK_ID" | jq '.debug' | jq '.'  # R1 spec / R2 摘要 / R3 原始输出

# 5. 日志关联
# 用 task_id 在日志中搜索全流程
grep "$TASK_ID" /var/log/argus.log

# 6. 常见失败模式
# "获取 PR 详情失败"      -> 平台 API 不可达或 token 过期
# "获取 diff 失败"        -> 平台 API 限流或 PR 已删除
# "llm: xxx 调用失败"     -> LLM 提供商故障（见场景 2）
# "回帖失败"              -> 平台 API 权限不足（需 write 权限）
# "JSON 解析失败"         -> LLM 输出格式异常（见场景 7）
# "仓库本月 token 预算已耗尽" -> ARGUS_REPO_MONTHLY_TOKEN_BUDGET 超限
```

### 逐阶段排查

```bash
# R1（需求提取）失败 -> 检查 PR 描述是否可读
# R2（变更摘要）失败 -> 检查 diff 获取是否正常
# R3（完成度判定）失败 -> 通常是 LLM JSON 输出问题
# R4（插件 fan-out）失败 -> 检查各插件状态（见场景 6）
# R5（裁决+回帖）失败 -> 检查平台 API 权限

# 查看降级信息（哪些插件未参与）
curl -s -H "Authorization: Bearer $ARGUS_APITOKEN" \
  "$ARGUS_ADDR/tasks/$TASK_ID" | jq -r '.result_json' | jq '.degraded'

# 查看具体 findings
curl -s -H "Authorization: Bearer $ARGUS_APITOKEN" \
  "$ARGUS_ADDR/tasks/$TASK_ID" | jq '.findings[]'
```

### 预防

- 生产环境建议开启 `ARGUS_DEBUG=true`（调试产物随任务可查，不影响性能）
- 监控各阶段耗时指标 `argus_stage_duration_seconds`
- 配置 Octopus IM 推送（`ARGUS_OCTOPUS_URL`），终态自动通知

---

## 附录：环境变量速查

| 变量 | 默认值 | 说明 |
|---|---|---|
| `ARGUS_ADDR` | `:8899` | 监听地址 |
| `ARGUS_DB_DSN` | `argus.db` | 数据库 DSN（SQLite 路径或 MySQL 连接串） |
| `ARGUS_APITOKEN` | 空 | API Bearer token（空=拒绝鉴权路由） |
| `ARGUS_BOT_USERNAME` | `argus-bot` | Bot 用户名（防自审） |
| `ARGUS_SKIP_DRAFT` | `true` | 跳过 Draft PR |
| `ARGUS_MAX_CONCURRENT_TASKS` | `2` | 最大并发任务数 |
| `ARGUS_LLM_RATE_LIMIT` | `0` | LLM 请求速率限制（req/s，0=不限） |
| `ARGUS_LLM_RATE_BURST` | 等于并发数 | 限速器突发容量 |
| `ARGUS_TASK_RETENTION_DAYS` | `0` | 任务保留天数（0=永久） |
| `ARGUS_REPO_MONTHLY_TOKEN_BUDGET` | `0` | 仓库月度 token 预算（0=不限） |
| `ARGUS_DEBUG` | `false` | 留存阶段调试产物 |
| `ARGUS_POLL_INTERVAL` | `0` | 轮询间隔（0=关闭） |
| `ARGUS_POLL_REPOS` | 空 | 轮询仓库列表 |
| `OPENAI_BASE_URL` | 空 | LLM API 地址 |
| `OPENAI_API_KEY` | 空 | LLM API Key |
| `OPENAI_MODEL` | 空 | 模型名 |
| `OPENAI_MAX_TOKENS` | `0`（自动派生） | 单次生成上限 |
| `ARGUS_MODEL_CONTEXT_TOKENS` | `128000` | 模型上下文窗口 |
| `ARGUS_WEBHOOK_MAX_BODY` | `1048576` | Webhook 请求体上限（字节） |
