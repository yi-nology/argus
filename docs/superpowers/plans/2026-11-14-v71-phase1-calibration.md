# Argus v7.1 Phase 1（预算与语义修复）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复第五轮评审四个 P1 的工程闭环——预留额度真正约束任务执行、评测口径不再系统性高估、逐调用账本幂等键稳定、Helm 业务 Pod 缺省零 DDL。

**Architecture:** 单线串行八任务（Helm → schema → 预算接线 → 强制模式 → 幂等键 → 评测核心 → 评测消费面 → 基线重跑），全部 TDD 红→绿、每任务独立 commit。核心改动面：`internal/storage/budget_reserve.go` 回传授予值、`internal/runner` 闸提前重建 Budget、`internal/evalq` 口径三分、`deploy/helm` verify 注入。

**Tech Stack:** Go 1.x + GORM（三方言 sqlite/pg/mysql）+ eino callbacks + oagentkit/llm v0.21.9 + Helm 3 + GitHub Actions。

**Spec:** `docs/superpowers/specs/2026-11-14-v71-quality-calibration-design.md`（本计划论证依据；执行者需同时阅读）。

## Global Constraints

- **前置**：批 B（`docs/execution-13items.md` 批 B 状态列全勾、`docs/eval-baseline.md` 与 `eval/baseline/*`、`test/evalq/real_model_test.go` 的在途改动已提交）完成后才进入 Task 6——两批都动 evalq，串行保 A/B 对照有效。
- **禁动文件**：`docs/execution-13items.md`（活文档，批 B 在途）；本计划不修改它。
- **门禁**：每任务收尾 `make lint && make test && make arch`；涉评测任务加 `make eval-baseline`；Task 8 加 `make eval-baseline-real`。
- **commit 纪律**：`git add` 精确点名文件（禁 `git add -A`——并行会话在途文件被扫入的教训在案）；每任务一个 commit；**不 push、不打 tag、不部署 43**（push 收尾由用户决定）。
- **风格**：中文包注释与 commit message、gofmt、TDD 先红后绿。
- Phase 1 无新增 MCP 工具、无 IDL/Finding schema 字段变更——不需要四处契约同步与五词 grep 清扫（那是 Phase 2 EvidenceStatus 的事）。

## File Structure

| 文件 | 动作 | 职责 |
|---|---|---|
| `deploy/helm/argus/templates/deployment.yaml` | 修改 | verify 注入 + liveness /healthz |
| `deploy/helm/argus/values.yaml` | 修改 | 注释与实现对齐 |
| `.github/workflows/ci.yml` | 修改 | helm job 补两条断言 |
| `deploy/k8s43/50-deployment.yaml` | 修改 | 手工清单同步（不部署） |
| `deploy/migrations/schema.sql` | 修改 | 补 4 张表 DDL |
| `internal/storage/schema_sql_test.go` | 新建 | schema↔AutoMigrate 双向门禁 |
| `internal/storage/budget_reserve.go` | 修改 | `ReserveResult.GrantedTokens` |
| `internal/storage/budget_reserve_test.go` | 修改 | 授予值断言 |
| `internal/runner/runner_lifecycle.go` | 修改 | `budgetGate` 双返回 + hard 分支 |
| `internal/runner/runner.go` | 修改 | Execute 闸提前 + Budget 后建 + handleRunFailure nil 守卫 |
| `internal/runner/runner_budget_test.go` / `runner_knobs_test.go` | 修改 | 调用点签名 + granted 断言 |
| `internal/config/config.go` + `config_test.go` | 修改 | `ARGUS_BUDGET_ENFORCEMENT` |
| `internal/runner/runner.go` execContext / `runner_usage.go` | 修改 | WithCallCounter + 稳定键 |
| `internal/runner/runner_usage_test.go` | 修改 | 稳定键幂等用例 |
| `internal/evalq/evalq.go` + `evalq_test.go` | 修改 | Clean/Unlabeled + 新指标 |
| `internal/evalq/report.go` | 修改 | 报告口径三分渲染 |
| `internal/qconsole/qconsole.go` / `render.go` / 测试 | 修改 | 趋势投影新字段 |
| `internal/service/service_console.go` / 测试 | 修改 | /console 新卡 |
| `docs/eval-baseline.md` | 修改 | 指标表补行 + 口径切换点 |

---

### Task 1: Helm verify 注入 + 探针分离

**Files:**
- Modify: `deploy/helm/argus/templates/deployment.yaml`（env 块 :34-43、livenessProbe :56-61）
- Modify: `deploy/helm/argus/values.yaml`（probes 注释 :41、migrationJob 注释 :70-73）
- Modify: `.github/workflows/ci.yml`（helm job Template smoke 步骤 :67-70）
- Modify: `deploy/k8s43/50-deployment.yaml`（envFrom 后、livenessProbe :54-59）

**Interfaces:**
- Consumes: 无（Phase 1 首任务）
- Produces: Helm 缺省形态——`migrationJob.enabled=true`（values 缺省）时业务 Deployment 带 `ARGUS_MIGRATE_MODE=verify`；liveness 指 `/healthz`。后续任务不依赖。

- [ ] **Step 1: 写失败断言（ci.yml）**

`.github/workflows/ci.yml` 的 `Template smoke (default + restore drill)` 步骤替换为：

```yaml
      - name: Template smoke (default + restore drill + migrate/probes)
        run: |
          helm template argus deploy/helm/argus > /tmp/argus-default.yaml
          helm template argus deploy/helm/argus --set pgRestore.enabled=true > /dev/null
          # Phase1④：开启迁移 Job 时业务 Pod 必须注入 verify（只校验不执行 DDL）
          grep -B2 'value: "verify"' /tmp/argus-default.yaml | grep -q ARGUS_MIGRATE_MODE
          # Phase1④：liveness 走 /healthz（进程级），readiness 走 /readyz
          grep -A3 'livenessProbe:' /tmp/argus-default.yaml | grep -q '/healthz'
          grep -A3 'readinessProbe:' /tmp/argus-default.yaml | grep -q '/readyz'
          # 关闭迁移 Job 时不注入 verify（缺省模式保持历史行为）
          helm template argus deploy/helm/argus --set migrationJob.enabled=false > /tmp/argus-nojob.yaml
          if grep -q 'value: "verify"' /tmp/argus-nojob.yaml; then
            echo "migrationJob 关闭时不应注入 verify" >&2; exit 1
          fi
```

- [ ] **Step 2: 跑断言确认失败**

Run: `bash -c 'helm template argus deploy/helm/argus > /tmp/argus-default.yaml && grep -B2 "value: \"verify\"" /tmp/argus-default.yaml | grep -q ARGUS_MIGRATE_MODE'`
Expected: FAIL（当前 Deployment 无 verify 注入，grep 无命中）。

- [ ] **Step 3: deployment.yaml 注入 verify + liveness 切 /healthz**

`deploy/helm/argus/templates/deployment.yaml` 把 env 块：

```yaml
        envFrom:
        - secretRef:
            name: {{ .Values.existingSecret }}
        {{- if .Values.config }}
        env:
        {{- range $k, $v := .Values.config }}
        - name: {{ $k }}
          value: {{ $v | quote }}
        {{- end }}
        {{- end }}
```

替换为：

```yaml
        envFrom:
        - secretRef:
            name: {{ .Values.existingSecret }}
        {{- /* Phase1④（spec §2.4）：迁移 Job 先行执行 DDL 后，业务 Pod 只校验
             不迁移——开启 migrationJob 且用户未在 config 显式设置时自动注入
             verify（K8s env 后者覆盖前者，显式手配优先）。 */}}
        {{- $injectVerify := and .Values.migrationJob.enabled (not (hasKey .Values.config "ARGUS_MIGRATE_MODE")) }}
        {{- if or .Values.config $injectVerify }}
        env:
        {{- if $injectVerify }}
        - name: ARGUS_MIGRATE_MODE
          value: "verify"
        {{- end }}
        {{- range $k, $v := .Values.config }}
        - name: {{ $k }}
          value: {{ $v | quote }}
        {{- end }}
        {{- end }}
```

livenessProbe 的 `path: /readyz` 改为：

```yaml
        livenessProbe:
          httpGet:
            path: /healthz
            port: {{ .Values.service.port }}
```

（readinessProbe 保持 `/readyz` 不动。）

- [ ] **Step 4: values.yaml 注释对齐实现**

两处注释修改：

1. `probes` 段头注释 `# ---- 探活（/readyz 单面，与 k8s43 同参）----` 改为：
   `# ---- 探活（readiness=/readyz 依赖面；liveness=/healthz 进程级——Phase1④ 分离）----`
2. `migrationJob` 段注释（:70-73）中「副本侧 ARGUS_MIGRATE_MODE =verify 只校验不迁移」后补一句实锤：
   `（deployment 模板在 migrationJob.enabled 时自动注入，Phase1④ 已实装——此前仅为注释声明）`

- [ ] **Step 5: k8s43 手工清单同步（只改文件不部署）**

`deploy/k8s43/50-deployment.yaml`：

1. `envFrom:` 块之后加：

```yaml
        env:
        # Phase1④：迁移 Job（90-migration-job.yaml）先行后业务 Pod 只校验不执行 DDL
        - name: ARGUS_MIGRATE_MODE
          value: "verify"
```

2. livenessProbe `path: /readyz` 改为 `path: /healthz`（readiness 不动）。

- [ ] **Step 6: 跑断言确认通过 + helm lint**

Run: `helm lint deploy/helm/argus && helm template argus deploy/helm/argus > /tmp/t.yaml && grep -B2 'value: "verify"' /tmp/t.yaml | grep -q ARGUS_MIGRATE_MODE && grep -A3 'livenessProbe:' /tmp/t.yaml | grep -q '/healthz'`
Expected: PASS（lint ok + 两条 grep 命中）。

- [ ] **Step 7: 门禁 + Commit**

Run: `make lint && make test && make arch`
Expected: 全绿。

```bash
git add deploy/helm/argus/templates/deployment.yaml deploy/helm/argus/values.yaml \
  .github/workflows/ci.yml deploy/k8s43/50-deployment.yaml
git commit -m "fix(helm): Deployment 注入 ARGUS_MIGRATE_MODE=verify + liveness 切 /healthz（Phase1④）"
```

---

### Task 2: schema.sql 补四表 + 双向门禁

**Files:**
- Modify: `deploy/migrations/schema.sql`（文件尾追加四段 DDL）
- Create: `internal/storage/schema_sql_test.go`

**Interfaces:**
- Consumes: `autoMigrateModels()`（`internal/storage/store.go:268-272`，13 模型单源）
- Produces: 门禁测试 `TestSchemaSQLMatchesAutoMigrate`——schema.sql 表集合与 AutoMigrate 模型表集合**双向相等**；后续新增表漏同步即红。

背景：schema.sql 现有 9 表，缺 `repo_settings` / `event_outbox` / `usage_events` / `budget_reservations`（注意 OutboxEvent 的 `TableName()` 是 **event_outbox** 不是 outbox_events）。schema.sql 头注释约定：LONGTEXT 仅用于 `longtextColumns`（store.go:365-368）登记的列，其余 `type:text` 列用 TEXT 镜像 GORM 默认。

- [ ] **Step 1: 写失败门禁测试**

新建 `internal/storage/schema_sql_test.go`：

```go
// schema_sql_test.go —— schema.sql 与 AutoMigrate 模型清单防漂移门禁（Phase1⑤）：
// deploy/migrations/schema.sql 是 MySQL 初始化参考（ops-runbook 灌库入口），
// 表集合必须与 autoMigrateModels() 双向相等——新增表只登记 GORM 而漏写
// schema.sql（或反之）在此红。

package storage

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"

	"gorm.io/gorm/schema"
)

// tabler GORM 可选 TableName() 接口（有手写表名的模型优先手写名）。
type tabler interface{ TableName() string }

func TestSchemaSQLMatchesAutoMigrate(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "migrations", "schema.sql"))
	if err != nil {
		t.Fatalf("读 schema.sql 失败: %v", err)
	}
	// 解析 CREATE TABLE IF NOT EXISTS `name` 清单。
	re := regexp.MustCompile("CREATE TABLE IF NOT EXISTS `([a-z0-9_]+)`")
	inSQL := map[string]bool{}
	for _, m := range re.FindAllSubmatch(raw, -1) {
		inSQL[string(m[1])] = true
	}
	// AutoMigrate 模型清单（有 TableName() 用手写名，否则 GORM 命名策略）。
	ns := schema.NamingStrategy{}
	inModels := map[string]bool{}
	for _, m := range autoMigrateModels() {
		name := ""
		if tn, ok := m.(tabler); ok {
			name = tn.TableName()
		} else {
			name = ns.TableName(reflect.TypeOf(m).Elem().Name())
		}
		inModels[name] = true
	}
	for name := range inModels {
		if !inSQL[name] {
			t.Errorf("schema.sql 缺表 %q（autoMigrateModels 已登记）", name)
		}
	}
	for name := range inSQL {
		if !inModels[name] {
			t.Errorf("schema.sql 多表 %q（autoMigrateModels 无对应模型）", name)
		}
	}
	if len(inSQL) != len(inModels) {
		t.Fatalf("表数不一致: sql=%d models=%d", len(inSQL), len(inModels))
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/storage/ -run TestSchemaSQLMatchesAutoMigrate -count=1`
Expected: FAIL，报 `schema.sql 缺表 "repo_settings"`、`"event_outbox"`、`"usage_events"`、`"budget_reservations"` 四条。

- [ ] **Step 3: schema.sql 补四段 DDL**

`deploy/migrations/schema.sql` 文件尾追加（列类型严格对齐 GORM 标签：`size`→VARCHAR、`type:text`→TEXT（非 longtextColumns 登记列）、float64→DOUBLE、时间→DATETIME(3)；索引名对齐 GORM 生成名）：

```sql
-- ============================================================
-- repo_settings —— 服务侧按仓配置（v3.187.0，Phase1⑤ 补齐）
-- ============================================================
CREATE TABLE IF NOT EXISTS `repo_settings` (
    `platform`      VARCHAR(16)   NOT NULL,
    `owner`         VARCHAR(128)  NOT NULL,
    `repo`          VARCHAR(128)  NOT NULL,
    `settings_json` TEXT          NOT NULL,
    `updated_by`    VARCHAR(64)   DEFAULT NULL,
    `updated_at`    DATETIME(3)   DEFAULT NULL,
    PRIMARY KEY (`platform`, `owner`, `repo`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- event_outbox —— 进度事件 outbox（at-most-once 投递，Phase1⑤ 补齐）
-- ============================================================
CREATE TABLE IF NOT EXISTS `event_outbox` (
    `id`         BIGINT       NOT NULL AUTO_INCREMENT,
    `event_id`   VARCHAR(64)  DEFAULT NULL,
    `body`       TEXT,
    `platform`   VARCHAR(16)  DEFAULT NULL,
    `owner`      VARCHAR(128) DEFAULT NULL,
    `repo`       VARCHAR(128) DEFAULT NULL,
    `type`       VARCHAR(32)  DEFAULT NULL,
    `created_at` DATETIME(3)  DEFAULT NULL,
    PRIMARY KEY (`id`),
    INDEX `idx_event_outbox_event_id` (`event_id`),
    INDEX `idx_event_outbox_created_at` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- usage_events —— 逐调用实销账本（#32，Phase1⑤ 补齐）
-- ============================================================
CREATE TABLE IF NOT EXISTS `usage_events` (
    `id`             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `task_id`        VARCHAR(40)  NOT NULL,
    `attempt_id`     VARCHAR(32)  DEFAULT NULL,
    `invocation_id`  VARCHAR(80)  NOT NULL,
    `platform`       VARCHAR(16)  NOT NULL,
    `owner`          VARCHAR(128) NOT NULL,
    `repo`           VARCHAR(128) NOT NULL,
    `provider`       VARCHAR(64)  DEFAULT NULL,
    `model`          VARCHAR(128) DEFAULT NULL,
    `input_tokens`   INT          NOT NULL DEFAULT 0,
    `output_tokens`  INT          NOT NULL DEFAULT 0,
    `cached_tokens`  INT          NOT NULL DEFAULT 0,
    `estimated_cost` DOUBLE       NOT NULL DEFAULT 0,
    `status`         VARCHAR(16)  NOT NULL DEFAULT 'settled',
    `occurred_at`    DATETIME(3)  NOT NULL,
    `created_at`     DATETIME(3)  DEFAULT NULL,
    `updated_at`     DATETIME(3)  DEFAULT NULL,
    PRIMARY KEY (`id`),
    UNIQUE INDEX `idx_ue_inv` (`invocation_id`),
    INDEX `idx_ue_task` (`task_id`),
    INDEX `idx_ue_budget` (`platform`, `owner`, `repo`, `status`, `occurred_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- budget_reservations —— 执行前预算预留（#32，Phase1⑤ 补齐）
-- ============================================================
CREATE TABLE IF NOT EXISTS `budget_reservations` (
    `id`         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `task_id`    VARCHAR(40)  NOT NULL,
    `attempt_id` VARCHAR(32)  DEFAULT NULL,
    `platform`   VARCHAR(16)  NOT NULL,
    `owner`      VARCHAR(128) NOT NULL,
    `repo`       VARCHAR(128) NOT NULL,
    `tokens`     INT          NOT NULL DEFAULT 0,
    `status`     VARCHAR(16)  NOT NULL DEFAULT 'reserved',
    `expires_at` DATETIME(3)  DEFAULT NULL,
    `created_at` DATETIME(3)  DEFAULT NULL,
    `updated_at` DATETIME(3)  DEFAULT NULL,
    PRIMARY KEY (`id`),
    UNIQUE INDEX `idx_bres_task` (`task_id`),
    INDEX `idx_bres_repo` (`platform`, `owner`, `repo`),
    INDEX `idx_budget_reservations_status` (`status`),
    INDEX `idx_budget_reservations_expires_at` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/storage/ -run TestSchemaSQLMatchesAutoMigrate -count=1`
Expected: PASS。

- [ ] **Step 5: 门禁 + Commit**

Run: `make lint && make test && make arch`
Expected: 全绿（`make test` 含三方言矩阵，schema.sql 不参与运行时——零影响）。

```bash
git add deploy/migrations/schema.sql internal/storage/schema_sql_test.go
git commit -m "test(storage): schema.sql 补四表 + 与 autoMigrateModels 双向门禁（Phase1⑤）"
```

---

### Task 3: GrantedTokens 接线——预留值真正约束任务执行

**Files:**
- Modify: `internal/storage/budget_reserve.go`（ReserveResult :109-114、ReserveRepoBudget :120-192）
- Modify: `internal/storage/budget_reserve_test.go`（TestReserveRepoBudget_WithinBudget :43 起）
- Modify: `internal/runner/runner_lifecycle.go`（budgetGate :34-83）
- Modify: `internal/runner/runner.go`（Execute :227-279、handleRunFailure :108-141）
- Modify: `internal/runner/runner_budget_test.go`（6 处调用点）、`internal/runner/runner_knobs_test.go`（2 处调用点 :107,:117）

**Interfaces:**
- Consumes: `storage.ReserveRepoBudget`（既有）、`dispatch.NewTokenBudget(limit int) *llm.Budget`（dispatcher.go:48）
- Produces:
  - `storage.ReserveResult.GrantedTokens int64`——无需预留路径 = `in.TaskBudget` 原值（≤0 无界）；成功路径 = 落库的 hold；拒绝路径 = 0。
  - `(*Runner).budgetGate(ctx, task, knobs, realBudget) (intercepted bool, granted int64)`——拦截时 granted 无意义（0）；放行时 = 任务级 Budget 上限。
  - Execute 语义：**先闸后建**，`dispatch.NewTokenBudget(int(granted))`。

评审 P1 闭环目标：DB 承诺 5,000 → 任务 `Budget.Remaining()` 上限即 5,000（现状用原始 20,000 是 bug）。

- [ ] **Step 1: 写失败测试（storage 层授予值）**

`internal/storage/budget_reserve_test.go` 的 `TestReserveRepoBudget_WithinBudget` 中，在 `row.Tokens != 300` 断言之后追加：

```go
	if res.GrantedTokens != 300 {
		t.Fatalf("GrantedTokens 应与落库预留同值 300，got %d", res.GrantedTokens)
	}
```

同文件新增：

```go
// TestReserveRepoBudgetGrantedUnbounded 无界任务（TaskBudget≤0）不占预留，
// GrantedTokens 返回任务预算原值（≤0 = 无界语义交还调用方）。
func TestReserveRepoBudgetGrantedUnbounded(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	res, err := s.ReserveRepoBudget(ctx, reserveIn("t-unbounded", 0, 1000, 0))
	if err != nil {
		t.Fatalf("无界任务应直通: %v", err)
	}
	if res.Verdict != ReserveOK || res.GrantedTokens != 0 {
		t.Fatalf("无界任务授予值应为 0（=任务预算原值）: verdict=%v granted=%d",
			res.Verdict, res.GrantedTokens)
	}
	if getReservation(t, s, "t-unbounded") != nil {
		t.Fatal("无界任务不应落预留行")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/storage/ -run 'TestReserveRepoBudget_WithinBudget|TestReserveRepoBudgetGrantedUnbounded' -count=1`
Expected: FAIL——`GrantedTokens` 字段不存在（编译错）即为本任务红灯。

- [ ] **Step 3: storage 实现 GrantedTokens**

`budget_reserve.go` ReserveResult 改为：

```go
// ReserveResult 预留判定输出：Used 字段为拒绝时的已承诺额度（失败播报用）。
type ReserveResult struct {
	Verdict   ReserveVerdict
	QuotaUsed int64 // 配额账已承诺（done 聚合 + 活跃预留）
	RealUsed  int64 // 真实账已承诺（实销 + 活跃预留）
	// GrantedTokens 授予额度（Phase1①，spec §2.1）：截断后的 hold 原样回传——
	// 调用方（runner 预算闸）据此创建任务级 LLM Budget，闭合「账面承诺 5,000
	// 实跑 20,000」的脱节。无需预留路径（双账本全关/任务预算非正）= in.TaskBudget
	// 原值（≤0 表示无界）；拒绝路径 = 0（调用方已拦截，不消费本值）。
	GrantedTokens int64
}
```

`ReserveRepoBudget` 三处赋值：

1. 双账本全关早返回（:122-124）改为：

```go
	if in.QuotaBudget <= 0 && in.RealBudget <= 0 {
		out.GrantedTokens = int64(in.TaskBudget)
		return out, nil // 双账本全关：无额度可超，也无须占承诺
	}
```

2. 计算 hold 之后（:185 `if hold <= 0` 之前）插入一行：

```go
	out.GrantedTokens = int64(hold)
	if hold <= 0 {
		return out, nil // 任务预算非正（无界任务）：不占预留，行为同旧闸直通
	}
```

（拒绝路径不赋值，GrantedTokens 保持 0，与注释契约一致。）

- [ ] **Step 4: 跑 storage 测试确认通过**

Run: `go test ./internal/storage/ -run 'TestReserveRepoBudget' -count=1`
Expected: PASS。

- [ ] **Step 5: 写失败测试（runner 闸 granted 断言）**

`internal/runner/runner_budget_test.go`——`TestBudgetGateReservesBeforeRun` 三个调用点改为捕获 granted 并断言（这同时是签名变更的编译红）：

```go
	t1 := budgetTask(t, store, "t1")
	if intercepted, granted := r.budgetGate(ctx, t1, knobs, 0); intercepted {
		t.Fatal("t1 额度内不应拦截")
	} else if granted != 400 {
		t.Fatalf("t1 授予值应 400（任务预算未被钳制），got %d", granted)
	}
```

```go
	t2 := budgetTask(t, store, "t2")
	if intercepted, granted := r.budgetGate(ctx, t2, knobs, 0); intercepted {
		t.Fatal("t2 剩余 100 内不应拦截（按剩余钳制放行）")
	} else if granted != 100 {
		t.Fatalf("t2 授予值应钳到剩余 100（评审脱节场景），got %d", granted)
	}
```

```go
	t3 := budgetTask(t, store, "t3")
	if intercepted, _ := r.budgetGate(ctx, t3, knobs, 0); !intercepted {
		t.Fatal("t3 已承诺 500/500 应拦截")
	}
```

- [ ] **Step 6: 跑测试确认失败**

Run: `go test ./internal/runner/ -run TestBudgetGateReservesBeforeRun -count=1`
Expected: 编译 FAIL——`budgetGate` 仍返回单值，多值接收不匹配。

- [ ] **Step 7: budgetGate 改双返回**

`internal/runner/runner_lifecycle.go`——函数签名与三个 return 点整体替换（:48-83）：

```go
// budgetGate 执行前预算闸（v3.1/v6.9.0 双闸 → #32 预留判定单临界区 → Phase1①
// 授予值回传）：配额账（done 口径）与真实消费账（usage_event 实销）各自把
// 「他任务活跃预留」计入已承诺额度，本任务预留额度 = min(任务预算, 各自剩余额度)
// ——判定与落预留行在 storage.ReserveRepoBudget 的租约临界区内原子完成。
// 返回 (intercepted, granted)：intercepted=true 已走失败终态（granted 无意义）；
// 放行时 granted = 授予额度（≤0 = 无界/双账本关），调用方必须用它创建任务级
// LLM Budget——账面承诺与执行上限从此一致（Phase1① 修「DB 承诺 5,000 实跑
// 20,000」脱节）。语义对齐旧双闸：
//   - 任一账本剩余额度 ≤0 → failed 终态 + start marker + 播报（预算路径不重试）；
//   - 判定不可得（存储/租约故障）→ fail-open 放行（Phase1② hard 模式收紧见下）；
//   - 双账本全关（budget<=0）→ 零开销直通不落预留，granted = 任务预算原值。
func (r *Runner) budgetGate(ctx context.Context, task *storage.Task, knobs taskKnobs, realBudget int) (bool, int64) {
	if knobs.monthlyBudget <= 0 && realBudget <= 0 {
		return false, int64(knobs.taskTokenBudget)
	}
	now := time.Now()
	res, err := r.Store.ReserveRepoBudget(ctx, storage.ReserveInput{
		TaskID: task.ID, AttemptID: strconv.Itoa(task.Attempt),
		Platform: task.Platform, Owner: task.Owner, Repo: task.Repo,
		TaskBudget:  knobs.taskTokenBudget,
		QuotaBudget: knobs.monthlyBudget, RealBudget: realBudget,
		Since: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()),
	})
	if err != nil {
		r.Log.Warn("argus.runner.budget_reserve_failed", "task", task.ID, "error", err.Error())
		return false, int64(knobs.taskTokenBudget)
	}
	switch res.Verdict {
	case storage.ReserveQuotaExceeded:
		obs.IncrBudgetExceeded("quota")
		r.Log.Warn("argus.runner.repo_budget_exceeded", "task", task.ID,
			"repo", task.Owner+"/"+task.Repo, "used", res.QuotaUsed, "budget", knobs.monthlyBudget)
		r.rejectByBudget(ctx, task,
			fmt.Sprintf("仓库本月 token 预算已耗尽（used=%d, budget=%d）", res.QuotaUsed, knobs.monthlyBudget),
			"下月预算重置后 push 新提交可重新触发审查。")
		return true, 0
	case storage.ReserveRealExceeded:
		obs.IncrBudgetExceeded("real")
		r.Log.Warn("argus.runner.real_budget_exceeded", "task", task.ID,
			"repo", task.Owner+"/"+task.Repo, "used", res.RealUsed, "budget", realBudget)
		r.rejectByBudget(ctx, task,
			fmt.Sprintf("仓库本月真实 token 实销+预留已达上限（used=%d, budget=%d）", res.RealUsed, realBudget),
			"下月重置后 push 新提交可重新触发审查；如需立即恢复请调整 ARGUS_REAL_TOKEN_BUDGET。")
		return true, 0
	}
	// 防御兜底：判定通过时授予值应 >0（任务预算非正的无界边界回落原值）。
	if res.GrantedTokens <= 0 {
		return false, int64(knobs.taskTokenBudget)
	}
	return false, res.GrantedTokens
}
```

（Task 4 会在 `err != nil` 分支插入 hard 模式——本任务先保持 fail-open 原样。）

- [ ] **Step 8: Execute 闸提前 + Budget 后建 + handleRunFailure nil 守卫**

`internal/runner/runner.go`——`Execute` 函数整体替换（:227-279）：

```go
// Execute 执行一个已 claim（running）的任务。正常路径始终终结任务状态（done/failed/stale）；
// 停机硬取消例外（保持 running，心跳过期后重置回 pending）。
// Phase1①：预算闸提到 Budget 创建之前——任务级 LLM Budget 用闸的授予值
// （granted）而非原始 knobs.taskTokenBudget，账面预留与执行上限一致。
func (r *Runner) Execute(ctx context.Context, taskID string) error {
	task, err := r.Store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("runner: 任务 %s 不存在: %w", taskID, err)
	}
	// 按仓旋钮单次加载（v3.187.0）：repo_settings 覆盖 → 全局默认，预算闸/
	// 任务预算/diff 上限三处消费全程共用（runner_knobs.go）。
	knobs := r.loadTaskKnobs(ctx, task)
	taskCost := llm.NewCostTracker()
	// 任务级 MCP 调用元数据：随 ctx 流入该任务全部工具调用的 _meta，server 侧
	// 可关联调用来源（哪次评审在调我）；Q&A 等非任务路径无注入。
	ctx = toolpool.WithCallMeta(ctx, map[string]any{
		"task_id": taskID,
		"repo":    task.Owner + "/" + task.Repo,
		"mr":      task.Number,
	})
	// Budget 闸后创建（Phase1①）：可能仍为 nil（panic 发生在闸前），失败收尾
	// 路径必须 nil 守卫。
	var budget *llm.Budget
	// v3.185.4：任务体统一 panic 出口——上游 queue/worker 的 panic 隔离只记
	// 日志不留终态（心跳过期复位重跑），对确定性 panic 是「复位→再炸」死循环
	// （live 实测：影响面注入 nil map panic 后任务卡 running）；argus 的
	// failed-retry 退避是重试正道，panic 必须经 handleRunFailure 落 failed 终态。
	if err = guardPanic(r.Log, task.ID, func() error {
		start := time.Now()
		// 审查开始留痕：在 MR 上发一条"审查中"评论，让用户知道 Argus 已接管。
		r.postStartMarker(ctx, task)

		// Phase1①：先闸后建——intercepted 时 Budget 尚未创建（也不需要）。
		intercepted, granted := r.budgetGate(ctx, task, knobs, r.Cfg.RealTokenBudget)
		if intercepted {
			return nil
		}
		budget = dispatch.NewTokenBudget(int(granted))
		ctx = r.execContext(ctx, task, budget, taskCost)
		wf := r.wireWorkflow(ctx, task, budget)
		r.publish(progress.EventTaskRunning, task, "", "")

		if rerr := r.run(ctx, task, wf, budget, start, taskCost, knobs); rerr != nil {
			r.handleRunFailure(ctx, task, budget, rerr)
		}
		return nil
	}); err != nil {
		// 守卫唯一上抛 = 任务体 panic：此时 run 已中断，handleRunFailure 未及
		// 执行——在守卫外补一次失败终态（其自身若再炸仅日志，终态缺失由心跳
		// 复位兜底，但确定性 panic 已从「无限复位重炸」变为 failed-retry 退避）
		defer func() {
			if rec := recover(); rec != nil {
				r.Log.Error("argus.runner.finalize_panic", "task", task.ID,
					"panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
			}
		}()
		r.handleRunFailure(ctx, task, budget, err)
		return err
	}
	return nil
}
```

`handleRunFailure`（:116）的 `budget.Used()` 加 nil 守卫：

```go
	used := 0
	if budget != nil { // Phase1①：panic 可能发生在 Budget 创建前
		used = budget.Used()
	}
	if fenced, ferr := r.Store.FinishTaskFenced(ctx, task.ID, storage.StatusFailed, "", "", "", errMsg, used, 0); ferr != nil {
```

- [ ] **Step 9: 更新剩余 budgetGate 调用点（机械改签名）**

单值接收改双值，模式统一为 `if intercepted, _ := r.budgetGate(...); intercepted {`：

- `internal/runner/runner_budget_test.go:104`（RealLedgerRejects）、`:124`（BothLedgersOff）、`:140`（FailOpenOnStorageError）
- `internal/runner/runner_knobs_test.go:107`、`:117`

例（:140）：

```go
	if intercepted, _ := r.budgetGate(ctx, task, taskKnobs{monthlyBudget: 100, taskTokenBudget: 400_000}, 100); !intercepted {
```

`runner.go:257` 的调用点已随 Step 8 整体替换，无需再改。

- [ ] **Step 10: 跑 runner 测试确认通过**

Run: `go test ./internal/runner/ -run 'TestBudgetGate|TestRunnerKnobs|Knobs' -count=1 && go test ./internal/runner/ -count=1`
Expected: PASS（先跑闸相关，再全包防 Execute 重排回归）。

- [ ] **Step 11: 门禁 + Commit**

Run: `make lint && make test && make arch`
Expected: 全绿。

```bash
git add internal/storage/budget_reserve.go internal/storage/budget_reserve_test.go \
  internal/runner/runner_lifecycle.go internal/runner/runner.go \
  internal/runner/runner_budget_test.go internal/runner/runner_knobs_test.go
git commit -m "fix(budget): 预留授予值接入任务级 Budget——GrantedTokens 闭环（Phase1①）"
```

---

### Task 4: ARGUS_BUDGET_ENFORCEMENT=soft|hard

**Files:**
- Modify: `internal/config/config.go`（Config struct RealTokenBudget 后 :133、Load :534 附近、helper 区 fixPushMode 后 :666）
- Modify: `internal/config/config_test.go`
- Modify: `internal/runner/runner_lifecycle.go`（budgetGate err 分支）

**Interfaces:**
- Consumes: Task 3 的 `budgetGate(ctx, task, knobs, realBudget) (bool, int64)` 双返回签名
- Produces: `config.Config.BudgetEnforcement string`——词表 `soft`（缺省）| `hard`，由 `budgetEnforcement(v string) string` 归一（非法值=soft）；hard 分支语义 = 判定不可得时 fail-closed 拒绝。

澄清（spec §0 纠偏）：额度不足两模式都 fail-closed（既有）；本开关只管「判定不可得」（存储/租约故障）窗口——soft=历史 fail-open，hard=拒绝新计费调用。

- [ ] **Step 1: 写失败测试**

`internal/config/config_test.go` 新增：

```go
// TestBudgetEnforcementParse ARGUS_BUDGET_ENFORCEMENT 归一（Phase1①）：
// 仅 hard（大小写不敏感）为 hard，其余（空/soft/非法值）一律 soft——非法值
// 不阻断启动，缺省可用性优先。
func TestBudgetEnforcementParse(t *testing.T) {
	cases := map[string]string{
		"": "soft", "soft": "soft", "SOFT": "soft",
		"hard": "hard", "HARD": "hard", " Hard ": "hard",
		"banana": "soft", "1": "soft",
	}
	for in, want := range cases {
		if got := budgetEnforcement(in); got != want {
			t.Errorf("budgetEnforcement(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLoadBudgetEnforcement env 装配面。
func TestLoadBudgetEnforcement(t *testing.T) {
	t.Setenv("ARGUS_BUDGET_ENFORCEMENT", "hard")
	if got := Load().BudgetEnforcement; got != "hard" {
		t.Fatalf("Load 后应 hard，got %q", got)
	}
	t.Setenv("ARGUS_BUDGET_ENFORCEMENT", "junk")
	if got := Load().BudgetEnforcement; got != "soft" {
		t.Fatalf("非法值应归一 soft，got %q", got)
	}
}
```

`internal/runner/runner_budget_test.go` 新增：

```go
// TestBudgetGateHardRejectsOnStorageError hard 模式（Phase1①）：判定不可得
// （存储故障）不再 fail-open，闸直接拦截——成本敏感环境「账本不可用就不开
// 新计费调用」。库已关故只断言拦截行为（终态写入是 best-effort，与既有
// FailOpen 用例同 harness 形态）；soft 侧由 TestBudgetGateFailOpenOnStorageError
// 保持回归。
func TestBudgetGateHardRejectsOnStorageError(t *testing.T) {
	r, store, ctx := budgetTestRunner(t)
	r.Cfg.BudgetEnforcement = "hard"
	knobs := taskKnobs{monthlyBudget: 100, taskTokenBudget: 400_000}
	task := budgetTask(t, store, "t-hard")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if intercepted, _ := r.budgetGate(ctx, task, knobs, 0); !intercepted {
		t.Fatal("hard 模式存储故障应拦截（fail-closed）")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/config/ -run BudgetEnforcement -count=1 && go test ./internal/runner/ -run TestBudgetGateHardRejectsOnStorageError -count=1`
Expected: FAIL——`budgetEnforcement` 未定义、`BudgetEnforcement` 字段不存在、hard 分支未实现（拦截断言失败）。

- [ ] **Step 3: config 实现**

`config.go` Config struct，`RealTokenBudget int`（:133）之后加：

```go
	// BudgetEnforcement 预算「判定不可得」强制模式（Phase1①，spec §2.1）：
	// soft（缺省）= 存储/租约故障时 fail-open 放行（历史可用性语义）；
	// hard = 同窗口 fail-closed，拒绝启动新计费调用（成本敏感环境）。
	// 额度不足两模式均 fail-closed（既有语义，与本开关无关）。
	// 词表归一见 budgetEnforcement()。
	BudgetEnforcement string
```

`Load()` 中 `RealTokenBudget:` 行（:534）之后加：

```go
		BudgetEnforcement:      budgetEnforcement(envStr("ARGUS_BUDGET_ENFORCEMENT", "soft")),
```

`fixPushEnabled`（:669）之后加 helper：

```go
// budgetEnforcement 预算强制模式归一：仅 "hard"（大小写不敏感、去空白）为
// hard，其余（含空/非法值）一律 soft——非法值不阻断启动，缺省可用性优先。
func budgetEnforcement(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), "hard") {
		return "hard"
	}
	return "soft"
}
```

- [ ] **Step 4: budgetGate 插入 hard 分支**

`runner_lifecycle.go` budgetGate 的 `err != nil` 分支（Task 3 Step 7 版本）改为：

```go
	if err != nil {
		r.Log.Warn("argus.runner.budget_reserve_failed", "task", task.ID, "error", err.Error())
		// Phase1①：hard 模式判定不可得 fail-closed——账本不可用就不开新计费
		// 调用；soft 保持历史 fail-open（可用性优先）。
		if r.Cfg != nil && r.Cfg.BudgetEnforcement == "hard" {
			r.rejectByBudget(ctx, task,
				"预算账本不可用（预留判定失败），ARGUS_BUDGET_ENFORCEMENT=hard 拒绝放行",
				"检查数据库与 leader_leases 后 push 新提交重试；可用性优先可切回 soft。")
			return true, 0
		}
		return false, int64(knobs.taskTokenBudget)
	}
```

同步把 budgetGate 头注释第三条改为：

```go
//   - 判定不可得（存储/租约故障）→ soft: fail-open 放行（历史语义）；
//     hard（ARGUS_BUDGET_ENFORCEMENT）: fail-closed 拒绝（Phase1①）；
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/config/ -count=1 && go test ./internal/runner/ -run 'TestBudgetGate' -count=1`
Expected: PASS（含既有 `TestBudgetGateFailOpenOnStorageError` soft 回归——其 Runner 构造时 `BudgetEnforcement` 零值 "" ≠ "hard"，走 fail-open 不变）。

- [ ] **Step 6: 门禁 + Commit**

Run: `make lint && make test && make arch`
Expected: 全绿。

```bash
git add internal/config/config.go internal/config/config_test.go \
  internal/runner/runner_lifecycle.go internal/runner/runner_budget_test.go
git commit -m "feat(budget): ARGUS_BUDGET_ENFORCEMENT=soft|hard 判定不可得强制模式（Phase1①）"
```

---

### Task 5: 稳定 invocation_id——任务作用域计数器贯穿落账

**Files:**
- Modify: `internal/runner/runner.go`（execContext :131 起）
- Modify: `internal/runner/runner_usage.go`（recordUsageEvent :33-57、头注释 :26-29）
- Modify: `internal/runner/runner_usage_test.go`

**Interfaces:**
- Consumes: 上游 `llm.WithCallCounter(ctx) context.Context`（oagentkit/llm@v0.21.9 usage.go:67-69）与 `UsageRecord.Iteration int64`（:32，「本作用域第几次 LLM 调用，1 起；无计数器为 0」；OnEnd 内 `rec.Iteration = c.n.Add(1)` :167）。
- Produces: 稳定幂等键格式 **`u-<taskID>-<attempt>-<iteration>`**（≤80 字符，`invocation_id` 列上限 80；taskID `t-`+16hex+10digit ≈33 字符）；`Iteration=0`（计数器缺席的旁路）退回 `"u-"+uuid`（与旧语义相同）。

评审 P1 闭环目标：同一物理调用重复投递（回调重放/写库重试）得到**同一个** invocation_id → 撞 `idx_ue_inv` 唯一索引收敛，端到端幂等成立；现状每次新铸 uuid 永不相撞。

**探针风险（spec §7）**：主路成立的前提是计数器值能到达 eino 回调链。Step 1 的测试就是探针——若注入后 `Iteration` 恒为 0，测试红，按 Step 6 的兜底分支处理。

- [ ] **Step 1: 写失败测试（稳定键 + 计数器接线探针）**

`internal/runner/runner_usage_test.go` 新增：

```go
// TestRecordUsageEventStableInvocationKey 稳定幂等键（Phase1③）：同一物理调用
// （同 task/attempt/Iteration）重复投递 → 同一 invocation_id → 撞唯一索引
// 收敛为一笔——端到端「不可重复计费」。Iteration=0（计数器缺席）退回新 uuid
// （旧语义，两次投递两行——由 uuid 唯一性保证不撞）。
func TestRecordUsageEventStableInvocationKey(t *testing.T) {
	r, store := usageTestRunner(t)
	ctx := context.Background()
	task := usageTestTask(t, store, "t-stable")

	rec := llm.UsageRecord{Model: "m", PromptTokens: 50, Iteration: 3}
	r.recordUsageEvent(ctx, task, rec)
	r.recordUsageEvent(ctx, task, rec) // 同一物理调用重复投递
	r.recordUsageEvent(ctx, task, rec)

	events, err := store.TaskUsageEvents(ctx, "t-stable")
	if err != nil || len(events) != 1 {
		t.Fatalf("同 Iteration 三次投递应收敛 1 行: n=%d err=%v", len(events), err)
	}
	want := "u-t-stable-0-3"
	if events[0].InvocationID != want {
		t.Fatalf("稳定键失真: got %q want %q", events[0].InvocationID, want)
	}
	total, _ := store.RepoMonthlyTokensReal(ctx, "gitea", "acme", "app", time.Now().Add(-time.Hour))
	if total != 50 {
		t.Fatalf("重复投递不应重复计费，got %d", total)
	}

	// Iteration=0 兜底：两次投递两行（uuid 键，不收敛——旧语义保持）。
	task2 := usageTestTask(t, store, "t-fallback")
	r.recordUsageEvent(ctx, task2, llm.UsageRecord{Model: "m", PromptTokens: 1, Iteration: 0})
	r.recordUsageEvent(ctx, task2, llm.UsageRecord{Model: "m", PromptTokens: 1, Iteration: 0})
	evs2, _ := store.TaskUsageEvents(ctx, "t-fallback")
	if len(evs2) != 2 {
		t.Fatalf("Iteration=0 应退回 uuid 两行: n=%d", len(evs2))
	}
	for _, e := range evs2 {
		if len(e.InvocationID) < 3 || e.InvocationID[:2] != "u-" {
			t.Fatalf("兜底键应 u- 前缀: %q", e.InvocationID)
		}
	}
}

// TestExecContextInjectsCallCounter 计数器接线探针（Phase1③ 主路前提）：
// execContext 注入 llm.WithCallCounter 后，两次模拟模型回调的 UsageRecord
// 应携带递增 Iteration（1、2）——若恒为 0 说明计数器未达回调链，主路死、
// 走 uuid 兜底（见本任务 Step 6）。
func TestExecContextInjectsCallCounter(t *testing.T) {
	r, store := usageTestRunner(t)
	ctx := context.Background()
	task := usageTestTask(t, store, "t-counter")

	budget := dispatch.NewTokenBudget(0)
	taskCost := llm.NewCostTracker()
	wctx := r.execContext(ctx, task, budget, taskCost)

	var got []string
	wctx = callbacks.EnsureRunInfo(wctx, "test-model", components.ComponentOfChatModel)
	for i := 0; i < 2; i++ {
		wctx = callbacks.OnStart(wctx, &model.CallbackInput{})
		_ = callbacks.OnEnd(wctx, &model.CallbackOutput{
			TokenUsage: &model.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		})
	}
	events, err := store.TaskUsageEvents(ctx, "t-counter")
	if err != nil || len(events) != 2 {
		t.Fatalf("两次回调应落 2 行: n=%d err=%v", len(events), err)
	}
	got = []string{events[0].InvocationID, events[1].InvocationID}
	if got[0] != "u-t-counter-0-1" || got[1] != "u-t-counter-0-2" {
		t.Fatalf("计数器应贯穿成稳定递增键（Iteration 主路探针）: %v", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/runner/ -run 'TestRecordUsageEventStableInvocationKey|TestExecContextInjectsCallCounter' -count=1`
Expected: FAIL——稳定键测试得到 uuid（键不含 Iteration）；接线探针 `Iteration` 缺席（当前 execContext 未注入计数器，Iteration=0 → 兜底 uuid → 断言 `u-t-counter-0-1` 失败）。

- [ ] **Step 3: execContext 注入计数器**

`internal/runner/runner.go` execContext 函数体第一行（`obsOpt := ...` 之前）加：

```go
	// Phase1③：任务作用域 LLM 调用计数器——rec.Iteration 随 UsageRecord 进入
	// recordUsageEvent，铸造稳定 invocation_id（同物理调用重复投递撞唯一索引
	// 收敛）。注入必须在 InitCallbacks 之前，使 eino 回调链 ctx 可见。
	ctx = llm.WithCallCounter(ctx)
```

- [ ] **Step 4: recordUsageEvent 稳定键 + 修自相矛盾注释**

`internal/runner/runner_usage.go`——头注释纪律段（:26-29）替换：

```go
// 纪律：
//   - invocation_id 稳定键（Phase1③）：任务作用域计数器（execContext 注入
//     llm.WithCallCounter）的 Iteration 铸 u-<taskID>-<attempt>-<iteration>——
//     同一物理调用重复投递（回调重放/写库重试）得同键，由 usage_events
//     唯一索引幂等收敛；Iteration=0（计数器缺席的旁路）退回新 uuid（旧语义），
//     「不可重复计费」单源仍在存储层；
```

`recordUsageEvent` 内 ev 构造（:40-42）替换：

```go
	inv := "u-" + uuid.NewString()
	if rec.Iteration > 0 {
		inv = fmt.Sprintf("u-%s-%d-%d", task.ID, task.Attempt, rec.Iteration)
	}
	ev := &storage.UsageEvent{
		TaskID: task.ID, AttemptID: strconv.Itoa(task.Attempt),
		InvocationID: inv,
```

（import 加 `"fmt"`。）

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/runner/ -run 'Usage|ExecContext' -count=1`
Expected: PASS（含既有四例：WritesLedger / BestEffort / WiresUsageRecording / IdempotentByInvocation——最后这例的「同键重放」语义不变）。
**若 `TestExecContextInjectsCallCounter` 仍红（Iteration 恒 0）**：停下，记录「计数器未达回调链」结论到 commit message，删除该探针测试中稳定键断言、保留 uuid 兜底路径（Step 4 的 `if rec.Iteration > 0` 分支天然兼容），并在 `runner_usage.go` 头注释注明「端到端幂等依赖上游 Iteration 贯穿，已提 oagentkit issue 跟进」——这是 spec §2.3 预定的兜底分支，不算任务失败。

- [ ] **Step 6: 门禁 + Commit**

Run: `make lint && make test && make arch`
Expected: 全绿。

```bash
git add internal/runner/runner.go internal/runner/runner_usage.go internal/runner/runner_usage_test.go
git commit -m "fix(usage): 稳定 invocation_id——任务作用域计数器贯穿落账（Phase1③）"
```

---

### Task 6: 评测口径核心——Unlabeled 显式化 + 干净样例误报率

**前置检查（不可跳过）**：`docs/execution-13items.md` 批 B 状态列全勾、批 B 相关改动（`docs/eval-baseline.md`、`eval/baseline/*.yaml`、`test/evalq/real_model_test.go`）已 commit。未满足则停下向用户报告——本任务与批 B 都动 evalq，抢跑会污染 A/B 对照。

**Files:**
- Modify: `internal/evalq/evalq.go`（CaseResult :97-113、Metrics :124-139、ScoreResult :154-204、Score :207-233、包文档 :12-16）
- Modify: `internal/evalq/evalq_test.go`（TestScoreResultFindingMatching :23、TestScoreAggregate :100）

**Interfaces:**
- Consumes: 无（evalq 是 L0 零 internal 依赖包）
- Produces（Task 7 消费）:
  - `CaseResult.Clean bool`（json `clean,omitempty`）
  - `CaseResult.Unlabeled []Finding`（json `unlabeled,omitempty`）+ `CaseResult.UnlabeledKinds []string`（json `unlabeled_kinds,omitempty`）
  - `Metrics.Unlabeled int`（json `unlabeled_count`）
  - `Metrics.CleanCaseFPRate float64`（json `clean_case_fp_rate`）

口径原则（spec §2.2）：**不把 unlabeled 塞进 FP（未判定 ≠ 误报），但绝不静默丢弃**；`fp_rate` 旧公式保留标 deprecated；Precision 改称受限标注口径。

- [ ] **Step 1: 写失败测试**

`internal/evalq/evalq_test.go`——`TestScoreResultFindingMatching` 中 `len(res.FPs) != 0` 断言之后追加：

```go
	if len(res.Unlabeled) != 1 || res.Unlabeled[0].Kind != "style" || len(res.UnlabeledKinds) != 1 {
		t.Fatalf("非 clean 多报应进 Unlabeled 显式化（不丢弃不计 FP）: %+v", res.Unlabeled)
	}
	if !res.Clean {
		t.Fatal("must_find 非空样例 Clean 应 false")
	}
```

`TestScoreAggregate` 的 results 构造与断言替换为（s2/s3 显式标 Clean，新增 s5 验 Unlabeled 聚合）：

```go
	results := []evalq.CaseResult{
		{CaseID: "s1", VerdictHit: true, TPs: []int{0}, FNs: []int{1}},
		{CaseID: "s2", Clean: true, VerdictHit: true},
		{CaseID: "s3", Clean: true, VerdictHit: false, FPs: []evalq.Finding{{Kind: "style"}}},
		{CaseID: "s4", VerdictHit: true, TPs: []int{0}},
		{CaseID: "s5", Unlabeled: []evalq.Finding{{Kind: "nit"}}, UnlabeledKinds: []string{"nit"}},
	}
	m := evalq.Score(results)
	if m.Cases != 5 || m.VerdictHit != 3 {
		t.Fatalf("cases/verdict = %d/%d, want 5/3", m.Cases, m.VerdictHit)
	}
	if m.Confusion.TP != 2 || m.Confusion.FN != 1 || m.Confusion.FP != 1 || m.Confusion.TN != 1 {
		t.Fatalf("混淆矩阵: %+v", m.Confusion)
	}
	if abs(m.Precision-2.0/3.0) > 1e-9 {
		t.Fatalf("Precision = %f, want %f", m.Precision, 2.0/3.0)
	}
	if abs(m.Recall-2.0/3.0) > 1e-9 {
		t.Fatalf("Recall = %f", m.Recall)
	}
	if abs(m.FPRate-0.5) > 1e-9 {
		t.Fatalf("FPRate = %f, want 0.5", m.FPRate)
	}
	// Phase1②：干净样例误报发生率 = 有 FP 的干净样例 / 干净样例 = s3/s2,s3 = 1/2
	// （样例单位，修复旧 fp_rate 分子分母单位混用）。
	if abs(m.CleanCaseFPRate-0.5) > 1e-9 {
		t.Fatalf("CleanCaseFPRate = %f, want 0.5", m.CleanCaseFPRate)
	}
	// Unlabeled 聚合：s5 一条，不入 FP。
	if m.Unlabeled != 1 {
		t.Fatalf("Unlabeled = %d, want 1", m.Unlabeled)
	}
	if abs(m.MissRate-1.0/3.0) > 1e-9 {
		t.Fatalf("MissRate = %f, want 1/3", m.MissRate)
	}
	if abs(m.VerdictAccuracy-0.75) > 1e-9 {
		t.Fatalf("VerdictAccuracy = %f, want 0.75", m.VerdictAccuracy)
	}
	if m.F1 == 0 {
		t.Fatal("F1 应非零")
	}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/evalq/ -count=1`
Expected: 编译 FAIL——`Unlabeled`/`Clean`/`CleanCaseFPRate` 字段不存在。

- [ ] **Step 3: evalq.go 实现**

1. `CaseResult` 加字段（FPKinds 之后）：

```go
	// Clean 是否干净样例（must_find 空）——ScoreResult 落结果时判定，
	// Score 聚合干净样例误报率用（Phase1② 口径三分）。
	Clean bool `json:"clean,omitempty"`
	// Unlabeled 非 clean 样例上未匹配、非 must_not_find 的多报意见（Phase1②）：
	// 不入 FP（未判定 ≠ 误报）、不静默丢弃——受限标注口径的显式缺口，待人工
	// 标注后判 FP 或有效新发现。
	Unlabeled []Finding `json:"unlabeled,omitempty"`
	// UnlabeledKinds 上述多报的类别词（钻取面，与 FPKinds 同形）。
	UnlabeledKinds []string `json:"unlabeled_kinds,omitempty"`
```

2. `Metrics` 加字段 + 改两处注释：

```go
	// Precision TP/(TP+FP)——**受限标注口径**（Phase1②）：非 clean 样例的
	// 未标注多报不入 FP（见 Unlabeled），不是可跨标注集对比的标准 Precision；
	// 完整口径待真实 PR 全量标注基准（Phase2）。分母 0 时置 0。
	Precision float64 `json:"precision"`
	// Recall TP/(TP+FN)。
	Recall float64 `json:"recall"`
	// FPRate FP/(FP+TN)——历史口径 deprecated（Phase1②）：分子是意见条数、
	// 分母 TN 是样例数，单位不一致；新口径看 CleanCaseFPRate。保留字段供
	// /console 历史趋势曲线不断裂，下个基线周期淘汰。
	FPRate float64 `json:"fp_rate"`
	// Unlabeled 未标注多报意见总条数（json unlabeled_count，Phase1②）。
	Unlabeled int `json:"unlabeled_count"`
	// CleanCaseFPRate 干净样例误报发生率 = 有 FP 的干净样例数 / 干净样例数
	// （json clean_case_fp_rate，Phase1②）：样例单位的真「干净 PR 误报率」；
	// 出错样例不入分母（评测失败 ≠ 误报行为证据）。
	CleanCaseFPRate float64 `json:"clean_case_fp_rate"`
```

3. `ScoreResult`：早返回前落 Clean，FP 分支补 else：

```go
func ScoreResult(c Case, out Outcome, err error) CaseResult {
	res := CaseResult{CaseID: c.ID, Language: c.Language, Label: c.Label,
		Clean: len(c.Expect.MustFind) == 0}
	if err != nil {
		... // 既有出错保守计分不变（res.Clean 已带入）
```

FP 循环（:187-202）的 `if clean || forbidden { ... }` 改为：

```go
		if clean || forbidden {
			res.FPs = append(res.FPs, f)
			res.FPKinds = append(res.FPKinds, f.Kind)
		} else {
			// Phase1②：非 clean 未匹配且非禁报 → 显式未标注（不入 FP 不丢弃）。
			res.Unlabeled = append(res.Unlabeled, f)
			res.UnlabeledKinds = append(res.UnlabeledKinds, f.Kind)
		}
```

（FP 循环上文的 `clean := len(c.Expect.MustFind) == 0` 局部变量删除，循环改用 `res.Clean`，保持单源。）

4. `Score` 聚合改三处——既有 FP 循环内加 Unlabeled 计数、TN 循环改用显式 Clean、新增 CleanCaseFPRate 循环：

```go
		m.Confusion.FP += len(r.FPs)
		m.Unlabeled += len(r.Unlabeled) // Phase1②
	}
	// TN：干净基准（must_find 空）且实际上报为零的样例数——Phase1② 起用 Clean
	// 显式判定（历史实现靠「非 clean 必有 TP/FN」隐含成立，字面构造的结果不再成立）。
	for _, r := range results {
		if r.Err == "" && r.Clean && len(r.FPs) == 0 && len(r.TPs) == 0 && len(r.FNs) == 0 {
			m.Confusion.TN++
		}
	}
	// CleanCaseFPRate：干净样例误报发生率（样例单位）；出错样例不入分母。
	cleanTotal, cleanWithFP := 0, 0
	for _, r := range results {
		if r.Err != "" || !r.Clean {
			continue
		}
		cleanTotal++
		if len(r.FPs) > 0 {
			cleanWithFP++
		}
	}
	m.CleanCaseFPRate = ratio(cleanWithFP, cleanTotal)
```

（若其他既有测试用字面 `CaseResult` 调 `Score` 并断言 TN，按其本意给干净样例补 `Clean: true`——跑测试即暴露。）

5. 包文档口径段（:12-16）更新为：

```go
// 指标口径（与 golden eval 的 verdict 命中率互补，issue #25 审计原文；
// Phase1② 口径三分修订）：
//   - finding 级混淆矩阵：must_find 命中=TP，未命中=FN（漏检）；
//   - clean 基准（must_find 空）上报的意见全部计 FP（误报，golden 北极星口径）；
//   - must_not_find 命中的实际上报意见亦计 FP；
//   - 非 clean 样例未匹配且非禁报的多报 → Unlabeled 显式化（不入 FP 不丢弃），
//     Precision 因此是「受限标注口径」而非标准 Precision；
//   - CleanCaseFPRate = 有 FP 的干净样例数 / 干净样例数（样例单位的干净
//     PR 误报发生率）；旧 fp_rate 分子分母单位不一致，deprecated 保留；
//   - 裁决稳定性：k 轮同输入裁决的逐案一致率均值。
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/evalq/ -count=1 && make eval-baseline`
Expected: PASS——`make eval-baseline`（打桩 44 案）出新字段样例报告（JSON 经 struct tag 自动携带 `clean_case_fp_rate`/`unlabeled_count`）；TestReportRender 既有断言此时仍绿——报告 Markdown 渲染在 Task 7 才改，本任务 Step 5 只扩 JSON 断言。

- [ ] **Step 5: 扩 TestReportRender JSON 断言**

`internal/evalq/evalq_test.go` TestReportRender 的 JSON 断言列表扩为：

```go
	for _, want := range []string{`"fp_rate"`, `"stability"`, `"by_language"`,
		`"clean_case_fp_rate"`, `"unlabeled_count"`} {
```

（Markdown 断言**本任务不动**——其期望文案依赖 Task 7 的 report.go 渲染改动，与 JSON 断言分离，防本任务收尾 `go test ./internal/evalq` 假红。）

- [ ] **Step 6: 跑全包确认通过**

Run: `go test ./internal/evalq/ -count=1 && go test ./test/evalq/ -count=1`
Expected: PASS（test/evalq 是打桩管道回归——若 `TestEvalPipelineStub` 因旧字段名断言变红，在其断言列表补 `"clean_case_fp_rate"`、`"unlabeled_count"` 两项即绿）。

- [ ] **Step 7: 门禁 + Commit**

Run: `make lint && make test && make arch && make eval-baseline`
Expected: 全绿。

```bash
git add internal/evalq/evalq.go internal/evalq/evalq_test.go
git commit -m "feat(evalq): 口径三分——Unlabeled 显式化 + 干净样例误报率（Phase1②）"
```

---

### Task 7: 评测消费面同步——报告 / 趋势面 / 文档

**Files:**
- Modify: `internal/evalq/report.go`（WriteMarkdown :129-200）
- Modify: `internal/qconsole/qconsole.go`（EvalTrendMets :63-70、EvalLangMets :72-76）
- Modify: `internal/qconsole/render.go`（质量趋势表）
- Modify: `internal/service/service_console.go`（renderConsoleTrend :197-237）
- Modify: `internal/service/service_console_test.go`（:120-145 趋势渲染测试）
- Modify: `internal/qconsole/qconsole_test.go`（:144-166 快照渲染测试）
- Modify: `docs/eval-baseline.md`（指标表 :13-21）

**Interfaces:**
- Consumes: Task 6 的 `Metrics.CleanCaseFPRate`（json `clean_case_fp_rate`）、`Metrics.Unlabeled`（json `unlabeled_count`）、`Metrics.Precision`（json `precision` 不变）
- Produces: 报告 Markdown/JSON、`/console` 卡片、qconsole 表、eval-baseline 文档全部带新口径；旧 `fp_rate` 标注历史口径。

- [ ] **Step 1: 更新消费面测试断言（先红）**

`internal/service/service_console_test.go` 合成基线 raw JSON（:122-125）补新字段：

```go
		raw := `{"generated_at":"2026-11-13T18:53:37+08:00","model":"mimo-v2.6-pro",` +
			`"stability":0.952,"overall":{"fp_rate":0.727,"miss_rate":0.0,` +
			`"precision":0.805,"recall":1.0,"clean_case_fp_rate":0.25,"unlabeled_count":3},` +
			`"by_language":[{"language":"go","fp_rate":1.0,"miss_rate":0.0,"clean_case_fp_rate":0.5}]}`
```

期望列表（:134-138）加：

```go
		for _, want := range []string{
			"mimo-v2.6-pro", "72.7%", "100.0%", "95.2%", "分语言", "go",
			"干净误报率", "25.0%", "精确率（受限）", "未标注多报 3 条",
		} {
```

`internal/qconsole/qconsole_test.go` 快照 EvalTrend（:144-147）补：

```go
	snap.EvalTrend = &qconsole.EvalTrend{
		Model: "mimo-v2", Stability: 0.93,
		Overall:    qconsole.EvalTrendMets{FP: 0.12, Miss: 0.08, P: 0.85, R: 0.91, CFP: 0.05, Unlabeled: 2},
		ByLanguage: []qconsole.EvalLangMets{{Language: "go", FP: 0.10, Miss: 0.06, CFP: 0.04}},
	}
```

HTML 断言列表（:154-156）加 `"干净误报率"`、`"Precision(受限)"`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/service/ ./internal/qconsole/ -count=1`
Expected: FAIL——编译错（`CFP`/`Unlabeled` 字段不存在）即红；字段补上后仍会红在渲染断言（新文案未产出）。

- [ ] **Step 3: report.go 总指标表口径三分**

`WriteMarkdown` 的 head 模板（:139-154）总指标表替换为：

```markdown
## 总指标

| 指标 | 值 |
|---|---|
| Verdict 命中率 | %.1f%% |
| Precision（受限标注口径） | %.3f |
| Recall | %.3f |
| 干净样例误报率（Clean FPRate） | %.3f |
| 误报率（FP Rate，历史口径 deprecated） | %.3f |
| 漏检率（Miss Rate） | %.3f |
| 未标注多报（Unlabeled） | %d |
| F1 | %.3f |
| 裁决稳定性（%d 轮） | %.3f |

混淆矩阵（finding 级）：TP=%d FP=%d FN=%d TN=%d
```

对应 `fmt.Sprintf` 实参（:156-160）改为：

```go
		rep.GeneratedAt.Format(time.RFC3339), rep.Model, paramsWord(rep.Params),
			rep.Cases, rep.Errored, erroredNote(rep.Errored),
			100*o.VerdictAccuracy, o.Precision, o.Recall, o.CleanCaseFPRate, o.FPRate,
			o.MissRate, o.Unlabeled, o.F1,
			max(rep.Runs, 1), rep.Stability,
			o.Confusion.TP, o.Confusion.FP, o.Confusion.FN, o.Confusion.TN,
```

分语言表头（:153）与行（:167-168）加一列干净误报率：

```go
// 表头
| 语言 | 样例 | Verdict 命中 | Precision | Recall | 干净误报率 | 误报率(历史) | 漏检率 |
|---|---|---|---|---|---|---|---|
// 行
line := fmt.Sprintf("| %s | %d | %.1f%% | %.3f | %.3f | %.3f | %.3f | %.3f |\n",
	lr.Language, m.Cases, 100*m.VerdictAccuracy, m.Precision, m.Recall,
	m.CleanCaseFPRate, m.FPRate, m.MissRate)
```

逐案明细表（:187-193）表头加「未标注」列、行加 `len(r.Unlabeled)`：

```go
// 表头
"\n## 逐案明细\n\n| 样例 | 语言 | 标签 | Verdict 命中 | TP | FN | FP | 未标注 | 漏检类别 | 误报类别 | 错误 |\n|---|---|---|---|---|---|---|---|---|---|---|\n"
// 行
line := fmt.Sprintf("| %s | %s | %s | %t | %d | %d | %d | %d | %s | %s | %s |\n",
	r.CaseID, r.Language, r.Label, r.VerdictHit, len(r.TPs), len(r.FNs), len(r.FPs),
	len(r.Unlabeled),
	mdCell(strings.Join(r.MissedKinds, ", ")), mdCell(strings.Join(r.FPKinds, ", ")), mdCell(r.Err))
```

尾注（:198）替换：

```go
	_, err := fmt.Fprint(w, "\n> 口径（Phase1② 三分）：clean 基准上报意见全部计误报（golden 北极星口径）；must_find 未命中计漏检；\n> 非 clean 样例未匹配多报计 Unlabeled（不入 FP、待人工标注）——Precision 为受限标注口径，\n> 非跨标注集可比的标准 Precision；干净样例误报率 = 有 FP 的干净样例 / 干净样例（样例单位）；\n> 旧 FP Rate 分子分母单位不一致，deprecated 保留；裁决稳定性 = k 轮同输入裁决的逐案一致率均值。\n")
```

同批更新 `internal/evalq/evalq_test.go` TestReportRender 的 **Markdown** 断言（依赖上述渲染改动；JSON 断言已在 Task 6 Step 5 扩好，此处只换 Markdown 列表）：

```go
	for _, want := range []string{"# 基准集质量评测报告", "| Precision（受限标注口径） |",
		"| go |", "## 逐案明细", "llm timeout", "干净样例误报率", "未标注多报"} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown 报告缺 %q", want)
		}
	}
```

- [ ] **Step 4: qconsole 投影与渲染**

`internal/qconsole/qconsole.go`——`EvalTrendMets` 加两字段：

```go
type EvalTrendMets struct {
	FP   float64 `json:"fp_rate"`
	Miss float64 `json:"miss_rate"`
	P    float64 `json:"precision"`
	R    float64 `json:"recall"`
	// Phase1②：新口径投影（json tag 对齐 evalq.Metrics）。
	CFP        float64 `json:"clean_case_fp_rate"`
	Unlabeled  int     `json:"unlabeled_count"`
}
```

`EvalLangMets` 加 `CFP float64 \`json:"clean_case_fp_rate"\``（字段对齐分语言 Metrics）。

`internal/qconsole/render.go` 质量趋势表（:63-67）替换：

```html
<table><tr><th>维度</th><th>Precision(受限)</th><th>Recall</th><th>干净误报率</th><th>误报率(历史)</th><th>漏检率</th><th>未标注</th></tr>
<tr><td>总体</td><td>{{printf "%.3f" .EvalTrend.Overall.P}}</td><td>{{printf "%.3f" .EvalTrend.Overall.R}}</td><td>{{printf "%.3f" .EvalTrend.Overall.CFP}}</td><td>{{printf "%.3f" .EvalTrend.Overall.FP}}</td><td>{{printf "%.3f" .EvalTrend.Overall.Miss}}</td><td>{{.EvalTrend.Overall.Unlabeled}}</td></tr>
{{range .EvalTrend.ByLanguage}}<tr><td>{{.Language}}</td><td>—</td><td>—</td><td>{{printf "%.3f" .CFP}}</td><td>{{printf "%.3f" .FP}}</td><td>{{printf "%.3f" .Miss}}</td><td>—</td></tr>{{end}}
```

- [ ] **Step 5: /console 卡片**

`internal/service/service_console.go` renderConsoleTrend 卡片区（:216-222）替换：

```go
	b.WriteString("<div class=\"grid\">")
	card(b, "干净误报率", fmt.Sprintf("%.1f%%", t.Overall.CFP*100),
		tone(1-t.Overall.CFP, 0.8, 0.5))
	card(b, "误报率（历史）", fmt.Sprintf("%.1f%%", t.Overall.FP*100),
		tone(1-t.Overall.FP, 0.8, 0.5))
	card(b, "漏检率（Miss）", fmt.Sprintf("%.1f%%", t.Overall.Miss*100),
		tone(1-t.Overall.Miss, 0.9, 0.7))
	card(b, "精确率（受限）", fmt.Sprintf("%.1f%%", t.Overall.P*100), tone(t.Overall.P, 0.8, 0.6))
	card(b, "召回率", fmt.Sprintf("%.1f%%", t.Overall.R*100), tone(t.Overall.R, 0.9, 0.7))
	card(b, "裁决稳定性", fmt.Sprintf("%.1f%%", t.Stability*100), tone(t.Stability, 0.9, 0.7))
	b.WriteString("</div>")
	if t.Overall.Unlabeled > 0 {
		fmt.Fprintf(b, "<p class=\"muted\">未标注多报 %d 条（受限标注口径，待人工标注判 FP 或有效新发现）</p>",
			t.Overall.Unlabeled)
	}
```

分语言表（:224-231）表头/行同步加「干净误报率」列：

```go
	b.WriteString("<p class=\"muted\">分语言</p><table><tr><th>语言</th><th class=\"num\">干净误报率</th>" +
		"<th class=\"num\">误报率(历史)</th><th class=\"num\">漏检率</th></tr>")
	for _, l := range t.ByLanguage {
		fmt.Fprintf(b, "<tr><td>%s</td><td class=\"num\">%.1f%%</td><td class=\"num\">%.1f%%</td><td class=\"num\">%.1f%%</td></tr>",
			html.EscapeString(orDash(l.Language)), l.CFP*100, l.FP*100, l.Miss*100)
	}
```

- [ ] **Step 6: docs/eval-baseline.md 指标表补行 + 口径切换点**

指标表（:15-17 区域）替换为：

```markdown
| Precision（受限标注口径） | TP/(TP+FP) | finding 级；非 clean 样例的未标注多报不入 FP（Phase1②），**非跨标注集可比的标准 Precision** |
| Recall | TP/(TP+FN) | finding 级 |
| 干净样例误报率 CleanCaseFPRate | 有 FP 的干净样例数 / 干净样例数 | 样例单位的干净 PR 误报发生率（Phase1② 新增）；出错样例不入分母 |
| 误报率 FPRate（deprecated） | FP/(FP+TN) | **历史口径**：分子意见条数、分母样例数单位不一致（Phase1② 定性）；保留供旧趋势曲线，下个基线周期淘汰 |
| 未标注多报 Unlabeled | 非 clean 未匹配且非 must_not_find 的意见条数 | 不入 FP、不静默丢弃（Phase1②）；待人工标注判 FP 或有效新发现 |
```

在指标表之后的匹配规则段（:23-25）末尾追加一段：

```markdown
**口径切换点（Phase1②，2026-11-14）**：本表自本次起为三分口径——此前归档的
治理前后对照数字（P=1.000 等）均为旧口径，**与切换后数字不可直接比较**；
切换后首跑基线见下节「新口径基线」。
```

- [ ] **Step 7: 跑测试确认通过**

Run: `go test ./internal/evalq/ ./internal/qconsole/ ./internal/service/ -count=1 && make lint && make arch`
Expected: 全绿（`make test` 全量一并跑）。

- [ ] **Step 8: Commit**

```bash
git add internal/evalq/report.go internal/qconsole/qconsole.go internal/qconsole/render.go \
  internal/qconsole/qconsole_test.go internal/service/service_console.go \
  internal/service/service_console_test.go docs/eval-baseline.md
git commit -m "feat(console): 评测新口径消费面同步——报告/趋势面/文档（Phase1②）"
```

---

### Task 8: 新口径基线重跑 + Phase 1 收尾

**Files:**
- Modify: `docs/eval-baseline.md`（基线节追加新口径首跑）
- 本地 artifact：`eval/results/baseline-latest.*`（gitignored）+ archive 复制

**Interfaces:**
- Consumes: Task 1-7 全部产出；`make eval-baseline-real`（`ARGUS_EVAL=1` + lacus/.env OPENAI_* 网关，k=3，约 21 分钟）
- Produces: Phase 1 验收证据——新口径基线归档 + 四门禁全绿。

- [ ] **Step 1: 全量门禁（不含真实跑批）**

Run: `make lint && make test && make arch && make eval-baseline`
Expected: 全绿。任何红先修复再继续。

- [ ] **Step 2: 真实模型新口径基线**

Run: `make eval-baseline-real`
Expected: PASS，产出 `eval/results/baseline-latest.json/.md`（约 21 分钟；网关超时导致个别样例出错属正常——出错保守计分已在报告单列）。**跑批前确认网关可达**（lacus/.env 的 OPENAI_* 指向内部 9080 网关）。

- [ ] **Step 3: 归档 + 验证新字段落盘**

```bash
mkdir -p eval/results/archive
cp eval/results/baseline-latest.json "eval/results/archive/baseline-newmetric-$(date +%Y%m%d).json"
cp eval/results/baseline-latest.md "eval/results/archive/baseline-newmetric-$(date +%Y%m%d).md"
grep -q '"clean_case_fp_rate"' eval/results/baseline-latest.json && grep -q '"unlabeled_count"' eval/results/baseline-latest.json
```

Expected: 两条 grep 命中（新字段真的在产物里——否则消费面读到的全是零值）。
注意：`eval/results/` gitignored，archive 为本地留存（与批 B 归档同约定，不 commit）。

- [ ] **Step 4: docs 回写新口径首跑**

`docs/eval-baseline.md` 基线列表（「官方终基线」条目之后）追加：

```markdown
- **新口径首跑基线（Phase1②，2026-11-14，`make eval-baseline-real`，mimo-v2.6-pro，k=3）**：
  口径切换后首个基线——**与上方治理前后数字（旧口径）不可直接比较**。
  指标含 `clean_case_fp_rate` / `unlabeled_count` 两新面；Precision 自本节起为
  受限标注口径。数值以 `eval/results/archive/baseline-newmetric-*.json` 为准
  （本地归档，results 目录 gitignored）。
```

把 Step 2 跑出的实际数字补进该条目（P/R/CleanFPRate/Unlabeled/F1/verdict/稳定性——从 baseline-latest.json 的 overall 段抄录）。

- [ ] **Step 5: 收尾门禁复验 + Commit**

Run: `make lint && make test && make arch && make eval-baseline`
Expected: 全绿（docs 改动不影响测试，但按纪律复验）。

```bash
git add docs/eval-baseline.md
git commit -m "docs(eval): 新口径首跑基线归档 + 口径切换点声明（Phase1 收尾）"
```

- [ ] **Step 6: 汇报（不 push）**

向用户汇报：八个 commit 清单、四门禁状态、新口径基线数字、`WithCallCounter` 探针结论（主路成立 / 走了 uuid 兜底）、以及「push 双 remote + tag 由用户决定」。Phase 1 验收三条对照 spec §2.5 逐条自证：

1. 预算不能被在途任务突破 → Task 3 测试（granted 钳制 400→100 断言）；
2. 评测指标不再系统性高估 → Task 6/7（Unlabeled 显式 + 受限口径改名 + CleanCaseFPRate）；
3. 业务 Pod 缺省零 DDL → Task 1（helm 断言 verify 注入 + 关 Job 不注入）。
