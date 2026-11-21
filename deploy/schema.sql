-- Canonical MySQL 8.0 schema for Argus.
-- Generated from GORM AutoMigrate models in internal/storage/store.go.
-- Column sizes, types, and indexes mirror the gorm struct tags exactly.
--
-- LONGTEXT columns: GORM generates TEXT (64 KB) on MySQL for `type:text`.
-- This schema uses LONGTEXT directly for columns that the application
-- upgrades via upgradeMySQLLongText(), avoiding the ALTER TABLE on every startup.
--
-- Usage:
--   mysql -u root -p argus < schema.sql
--   (tables are created with IF NOT EXISTS for idempotency)
--
-- NOTE: schema.sql 仅作初始化参考；应用启动时 GORM AutoMigrate 会自动补齐
-- 缺失列/表，两机制可共存。

-- ============================================================
-- tasks — async review tasks (webhook / CLI / TaskAPI)
-- ============================================================
CREATE TABLE IF NOT EXISTS `tasks` (
    `tenant_id`  VARCHAR(64)  NOT NULL DEFAULT 'default',
    `id`                 VARCHAR(40)   NOT NULL,
    `kind`               VARCHAR(32)   NOT NULL,
    `platform`           VARCHAR(16)   NOT NULL,
    `owner`              VARCHAR(128)  NOT NULL,
    `repo`               VARCHAR(128)  NOT NULL,
    `number`             VARCHAR(16)   NOT NULL,
    `head_sha`           VARCHAR(64)   DEFAULT NULL,
    `event_type`         VARCHAR(32)   DEFAULT NULL,
    `event_id`           VARCHAR(128)  DEFAULT NULL,
    `status`             VARCHAR(16)   NOT NULL DEFAULT 'pending',
    `verdict`            VARCHAR(32)   DEFAULT NULL,
    `report`             LONGTEXT,
    `result_json`        LONGTEXT,
    `fail_reason`        LONGTEXT,
    `tokens_used`        INT           NOT NULL DEFAULT 0,
    `summary_comment_id` BIGINT        NOT NULL DEFAULT 0,
    `artifacts_json`     LONGTEXT,
    `debug_json`         LONGTEXT,
    `runner_id`          VARCHAR(64)   DEFAULT NULL,
    `heartbeat_at`       DATETIME(3)   DEFAULT NULL,
    `attempt`            INT           NOT NULL DEFAULT 0,
    `next_retry_at`      DATETIME(3)   DEFAULT NULL,
    `created_at`         DATETIME(3)   DEFAULT NULL,
    `updated_at`         DATETIME(3)   DEFAULT NULL,
    `started_at`         DATETIME(3)   DEFAULT NULL,
    `finished_at`        DATETIME(3)   DEFAULT NULL,
    PRIMARY KEY (`id`),
    UNIQUE INDEX `idx_tasks_event_id` (`tenant_id`, `event_id`),
    INDEX `idx_tasks_status` (`status`),
    INDEX `idx_tasks_runner_id` (`runner_id`),
    INDEX `idx_tasks_head_sha` (`head_sha`),
    INDEX `idx_tasks_created_at` (`created_at`),
    INDEX `idx_tasks_platform_owner_repo_number` (`platform`, `owner`, `repo`, `number`),
    INDEX `idx_tasks_budget` (`platform`, `owner`, `repo`, `status`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- finding_prints — cross-round finding fingerprints
-- ============================================================
CREATE TABLE IF NOT EXISTS `finding_prints` (
    `tenant_id`  VARCHAR(64)  NOT NULL DEFAULT 'default',
    `id`            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `platform`      VARCHAR(16)     NOT NULL,
    `owner`         VARCHAR(128)    NOT NULL,
    `repo`          VARCHAR(128)    NOT NULL,
    `number`        VARCHAR(16)     NOT NULL,
    `print`         VARCHAR(80)     NOT NULL,
    `file`          VARCHAR(512)    DEFAULT NULL,
    `line`          INT             NOT NULL DEFAULT 0,
    `severity`      VARCHAR(16)     DEFAULT NULL,
    `comment`       LONGTEXT,
    `status`        VARCHAR(16)     NOT NULL DEFAULT 'open',
    `plugin`        VARCHAR(64)     DEFAULT NULL,
    `first_task_id` VARCHAR(40)     DEFAULT NULL,
    `last_task_id`  VARCHAR(40)     DEFAULT NULL,
    `times_seen`    INT             NOT NULL DEFAULT 0,
    `regret_count`  INT             NOT NULL DEFAULT 0,
    `first_seen_at` DATETIME(3)     DEFAULT NULL,
    `last_seen_at`  DATETIME(3)     DEFAULT NULL,
    `created_at`    DATETIME(3)     DEFAULT NULL,
    `updated_at`    DATETIME(3)     DEFAULT NULL,
    PRIMARY KEY (`id`),
    UNIQUE INDEX `idx_print` (`tenant_id`, `platform`, `owner`, `repo`, `number`, `print`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- pattern_signals — repo-level review pattern trust history
-- (regret learning loop; see docs/design/review-closed-loop-design.md)
-- ============================================================
CREATE TABLE IF NOT EXISTS `pattern_signals` (
    `tenant_id`  VARCHAR(64)  NOT NULL DEFAULT 'default',
    `id`            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `platform`      VARCHAR(16)     NOT NULL,
    `owner`         VARCHAR(128)    NOT NULL,
    `repo`          VARCHAR(128)    NOT NULL,
    `plugin`        VARCHAR(64)     NOT NULL,
    `pattern_key`   VARCHAR(32)     NOT NULL,
    `pattern_text`  VARCHAR(255)    DEFAULT NULL,
    `posted`        INT             NOT NULL DEFAULT 0,
    `regret`        INT             NOT NULL DEFAULT 0,
    `suppress`      INT             NOT NULL DEFAULT 0,
    `restore`       INT             NOT NULL DEFAULT 0,
    `first_seen_at` DATETIME(3)     DEFAULT NULL,
    `last_seen_at`  DATETIME(3)     DEFAULT NULL,
    `created_at`    DATETIME(3)     DEFAULT NULL,
    `updated_at`    DATETIME(3)     DEFAULT NULL,
    PRIMARY KEY (`id`),
    UNIQUE INDEX `idx_pattern` (`tenant_id`, `platform`, `owner`, `repo`, `plugin`, `pattern_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- review_targets — per-PR summary comment + last head SHA tracking
-- ============================================================
CREATE TABLE IF NOT EXISTS `review_targets` (
    `tenant_id`  VARCHAR(64)  NOT NULL DEFAULT 'default',
    `id`                     BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `platform`               VARCHAR(16)     NOT NULL,
    `owner`                  VARCHAR(128)    NOT NULL,
    `repo`                   VARCHAR(128)    NOT NULL,
    `number`                 VARCHAR(16)     NOT NULL,
    `last_summary_comment_id` BIGINT         NOT NULL DEFAULT 0,
    `known_comment_ids`      VARCHAR(256)    DEFAULT NULL,
    `last_head_sha`          VARCHAR(64)     DEFAULT NULL,
    `created_at`             DATETIME(3)     DEFAULT NULL,
    `updated_at`             DATETIME(3)     DEFAULT NULL,
    PRIMARY KEY (`id`),
    UNIQUE INDEX `idx_target` (`tenant_id`, `platform`, `owner`, `repo`, `number`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- print_aliases — fingerprint short-code / wording-drift aliases
-- (alias_fp of a new wording → print_fp of the canonical entry;
-- near-duplicate merge layer, see internal/storage/printalias.go)
-- ============================================================
CREATE TABLE IF NOT EXISTS `print_aliases` (
    `tenant_id`  VARCHAR(64)  NOT NULL DEFAULT 'default',
    `platform`   VARCHAR(16)  NOT NULL,
    `owner`      VARCHAR(128) NOT NULL,
    `repo`       VARCHAR(128) NOT NULL,
    `number`     VARCHAR(16)  NOT NULL,
    `alias_fp`   VARCHAR(64)  NOT NULL,
    `print_fp`   VARCHAR(64)  DEFAULT NULL,
    `updated_at` DATETIME(3)  DEFAULT NULL,
    PRIMARY KEY (`tenant_id`, `platform`, `owner`, `repo`, `number`, `alias_fp`),
    INDEX `idx_print_aliases_print_fp` (`print_fp`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- leader_leases — singleton leader election leases (multi-replica)
-- ============================================================
CREATE TABLE IF NOT EXISTS `leader_leases` (
    `lease_key`  VARCHAR(128) NOT NULL,
    `holder`     VARCHAR(128) DEFAULT NULL,
    `expires_at` DATETIME(3)  DEFAULT NULL,
    `updated_at` DATETIME(3)  DEFAULT NULL,
    PRIMARY KEY (`lease_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- qa_sessions — persisted Q&A conversation history per session key
-- (history_json is a LONGTEXT big-text column)
-- ============================================================
CREATE TABLE IF NOT EXISTS `qa_sessions` (
    `tenant_id`    VARCHAR(64)  NOT NULL DEFAULT 'default',
    `session_key`  VARCHAR(160) NOT NULL,
    `history_json` LONGTEXT,
    `summary`      LONGTEXT,
    `updated_at`   DATETIME(3)  DEFAULT NULL,
    PRIMARY KEY (`tenant_id`, `session_key`),
    INDEX `idx_qa_sessions_updated_at` (`updated_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- schema_migrations — applied data-migration sentinels
-- (e.g. fingerprint_v2; gates one-shot migrations on startup)
-- ============================================================
CREATE TABLE IF NOT EXISTS `schema_migrations` (
    `version`    VARCHAR(64) NOT NULL,
    `applied_at` DATETIME(3) DEFAULT NULL,
    PRIMARY KEY (`version`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- comment_deliveries — platform delivery idempotency for the
-- comment pipeline (dedupe re-delivered webhook events)
-- ============================================================
CREATE TABLE IF NOT EXISTS `comment_deliveries` (
    `tenant_id`  VARCHAR(64)  NOT NULL DEFAULT 'default',
    `delivery_id` VARCHAR(128) NOT NULL,
    `seen_at`     DATETIME(3)  DEFAULT NULL,
    PRIMARY KEY (`tenant_id`, `delivery_id`),
    INDEX `idx_comment_deliveries_seen_at` (`seen_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- repo_settings —— 服务侧按仓配置（v3.187.0，Phase1⑤ 补齐）
-- ============================================================
CREATE TABLE IF NOT EXISTS `repo_settings` (
    `tenant_id`  VARCHAR(64)  NOT NULL DEFAULT 'default',
    `platform`      VARCHAR(16)   NOT NULL,
    `owner`         VARCHAR(128)  NOT NULL,
    `repo`          VARCHAR(128)  NOT NULL,
    `settings_json` TEXT          NOT NULL,
    `updated_by`    VARCHAR(64)   DEFAULT NULL,
    `updated_at`    DATETIME(3)   DEFAULT NULL,
    PRIMARY KEY (`tenant_id`, `platform`, `owner`, `repo`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- event_outbox —— 进度事件 outbox（at-most-once 投递，Phase1⑤ 补齐）
-- ============================================================
CREATE TABLE IF NOT EXISTS `event_outbox` (
    `tenant_id`  VARCHAR(64)  NOT NULL DEFAULT 'default',
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
    `tenant_id`  VARCHAR(64)  NOT NULL DEFAULT 'default',
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
    UNIQUE INDEX `idx_ue_inv` (`tenant_id`, `invocation_id`),
    INDEX `idx_ue_task` (`task_id`),
    INDEX `idx_ue_budget` (`platform`, `owner`, `repo`, `status`, `occurred_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- budget_reservations —— 执行前预算预留（#32，Phase1⑤ 补齐）
-- ============================================================
CREATE TABLE IF NOT EXISTS `budget_reservations` (
    `tenant_id`  VARCHAR(64)  NOT NULL DEFAULT 'default',
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
    UNIQUE INDEX `idx_bres_task` (`tenant_id`, `task_id`),
    INDEX `idx_bres_repo` (`platform`, `owner`, `repo`),
    INDEX `idx_budget_reservations_status` (`status`),
    INDEX `idx_budget_reservations_expires_at` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- fp_case_buckets —— 组织级误报判例桶（批 E，仓无关 Pattern 投影）
-- ============================================================
CREATE TABLE IF NOT EXISTS `fp_case_buckets` (
    `tenant_id`  VARCHAR(64)  NOT NULL DEFAULT 'default',
    `id`           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `pattern`      VARCHAR(64)  NOT NULL,
    `kind`         VARCHAR(64)  DEFAULT NULL,
    `org`          VARCHAR(128) DEFAULT NULL,
    `sample_repo`  VARCHAR(256) DEFAULT NULL,
    `sample_print` VARCHAR(80)  DEFAULT NULL,
    `comment`      TEXT,
    `hit_count`    INT          NOT NULL DEFAULT 0,
    `status`       VARCHAR(16)  NOT NULL DEFAULT 'candidate',
    `created_at`   DATETIME(3)  DEFAULT NULL,
    `updated_at`   DATETIME(3)  DEFAULT NULL,
    PRIMARY KEY (`id`),
    UNIQUE INDEX `idx_fp_case_buckets_pattern` (`tenant_id`, `pattern`),
    INDEX `idx_fp_case_buckets_org` (`org`),
    INDEX `idx_fp_case_buckets_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- side_effect_release_audits —— 强制释放租约的持久化审计（#64，Phase1⑤ 补齐）
-- 审计行与释放同事务落库（释放发生则审计必在）；dry-run 不落行。
-- ============================================================
CREATE TABLE IF NOT EXISTS `side_effect_release_audits` (
    `tenant_id`  VARCHAR(64)  NOT NULL DEFAULT 'default',
    `id`               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `resource`         VARCHAR(255) NOT NULL,
    `holder`           VARCHAR(64)  DEFAULT NULL,
    `fence`            BIGINT       NOT NULL DEFAULT 0,
    `cleared_actions`  BIGINT       NOT NULL DEFAULT 0,
    `marked_uncertain` BIGINT       NOT NULL DEFAULT 0,
    `operator_fp`      VARCHAR(16)  DEFAULT NULL,
    `reason`           TEXT,
    `created_at`       DATETIME(3)  DEFAULT NULL,
    PRIMARY KEY (`id`),
    INDEX `idx_side_effect_release_audits_created` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- tenant_repos —— 仓库坐标 → 租户归属映射（#82/#103，多租户地基）
-- webhook 入站无 Bearer，租户归属从坐标反查；无映射 = default 租户。
-- 映射行由控制面 admin API 维护；一仓一租户（PK=坐标，登记即独占）。
-- ============================================================
CREATE TABLE IF NOT EXISTS `tenant_repos` (
    `tenant_id`  VARCHAR(64)  NOT NULL DEFAULT 'default',
    `platform`   VARCHAR(16)  NOT NULL,
    `owner`      VARCHAR(128) NOT NULL,
    `repo`       VARCHAR(128) NOT NULL,
    `created_at` DATETIME(3)  DEFAULT NULL,
    `updated_at` DATETIME(3)  DEFAULT NULL,
    PRIMARY KEY (`platform`, `owner`, `repo`),
    INDEX `idx_tenant_repos_tenant` (`tenant_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- share_revocations —— share 令牌撤销清单（#91）
-- 令牌无状态长存活（MR 评论设计约束），撤销走服务端拒绝清单——校验侧
-- 一次索引查命中即拒。subject = 任务 ID 或 "platform:owner/repo"。
-- ============================================================
CREATE TABLE IF NOT EXISTS `share_revocations` (
    `subject`    VARCHAR(255) NOT NULL,
    `reason`     VARCHAR(512) DEFAULT NULL,
    `revoked_by` VARCHAR(64)  DEFAULT NULL,
    `created_at` DATETIME(3)  DEFAULT NULL,
    PRIMARY KEY (`subject`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- merge_operations —— 受控合并候选/执行记录（T2 合并资格门）
-- preview 建 pending 绑定 expected_remote_head；confirm 经副作用闸
-- at-most-once 执行 MergeCR；uncertain 由读回对账定案。
-- ============================================================
CREATE TABLE IF NOT EXISTS `merge_operations` (
    `tenant_id`            VARCHAR(64)  NOT NULL DEFAULT 'default',
    `id`                   VARCHAR(40)  NOT NULL,
    `platform`             VARCHAR(16)  NOT NULL,
    `owner`                VARCHAR(128) NOT NULL,
    `repo`                 VARCHAR(128) NOT NULL,
    `number`               VARCHAR(16)  NOT NULL,
    `expected_remote_head` VARCHAR(64)  NOT NULL,
    `status`               VARCHAR(16)  NOT NULL DEFAULT 'pending',
    `gate_json`            LONGTEXT,
    `effect_key`           VARCHAR(255) NOT NULL,
    `result_json`          LONGTEXT,
    `reason`               TEXT,
    `confirmed_by_fp`      VARCHAR(32)  DEFAULT NULL,
    `created_at`           DATETIME(3)  DEFAULT NULL,
    `updated_at`           DATETIME(3)  DEFAULT NULL,
    `confirmed_at`         DATETIME(3)  DEFAULT NULL,
    `finished_at`          DATETIME(3)  DEFAULT NULL,
    PRIMARY KEY (`id`),
    UNIQUE INDEX `idx_merge_operations_effect_key` (`effect_key`),
    INDEX `idx_merge_operations_pr` (`platform`, `owner`, `repo`, `number`),
    INDEX `idx_merge_operations_status` (`status`),
    INDEX `idx_merge_operations_created_at` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- fix_approval_tickets —— 高危修复审批票（T3）
-- critical/high 意见按 (PR, fp, head) 建票；approved 才放行修复。
-- 新 head 重建票，旧 pending → stale。
-- ============================================================
CREATE TABLE IF NOT EXISTS `fix_approval_tickets` (
    `tenant_id`      VARCHAR(64)  NOT NULL DEFAULT 'default',
    `id`             VARCHAR(40)  NOT NULL,
    `platform`       VARCHAR(16)  NOT NULL,
    `owner`          VARCHAR(128) NOT NULL,
    `repo`           VARCHAR(128) NOT NULL,
    `number`         VARCHAR(16)  NOT NULL,
    `task_id`        VARCHAR(40)  DEFAULT NULL,
    `head_sha`       VARCHAR(64)  NOT NULL,
    `print`          VARCHAR(80)  NOT NULL,
    `file`           VARCHAR(512) DEFAULT NULL,
    `severity`       VARCHAR(16)  DEFAULT NULL,
    `comment`        TEXT,
    `status`         VARCHAR(16)  NOT NULL DEFAULT 'pending',
    `decided_by_fp`  VARCHAR(32)  DEFAULT NULL,
    `reason`         TEXT,
    `round`          VARCHAR(40)  DEFAULT NULL,
    `created_at`     DATETIME(3)  DEFAULT NULL,
    `updated_at`     DATETIME(3)  DEFAULT NULL,
    `decided_at`     DATETIME(3)  DEFAULT NULL,
    PRIMARY KEY (`id`),
    UNIQUE INDEX `idx_fix_approval_tickets_fp` (`tenant_id`,`platform`,`owner`,`repo`,`number`,`head_sha`,`print`),
    INDEX `idx_fix_approval_tickets_pr` (`platform`,`owner`,`repo`,`number`),
    INDEX `idx_fix_approval_tickets_head` (`head_sha`,`status`),
    INDEX `idx_fix_approval_tickets_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- users / invites / sessions —— 多用户最小集（T5）
-- 角色→scope：viewer=read operator=ci maintainer/admin=admin auditor=read
-- 邀请永不授予 admin（与 MergePilot 同纪律）；会话 token 只存哈希。
-- ============================================================
CREATE TABLE IF NOT EXISTS `users` (
    `tenant_id`     VARCHAR(64)  NOT NULL DEFAULT 'default',
    `id`            VARCHAR(40)  NOT NULL,
    `username`      VARCHAR(64)  NOT NULL,
    `role`          VARCHAR(16)  NOT NULL,
    `password_hash` VARCHAR(128) NOT NULL,
    `salt`          VARCHAR(32)  NOT NULL,
    `disabled`      TINYINT(1)   NOT NULL DEFAULT 0,
    `created_at`    DATETIME(3)  DEFAULT NULL,
    `updated_at`    DATETIME(3)  DEFAULT NULL,
    PRIMARY KEY (`id`),
    UNIQUE INDEX `idx_users_name` (`tenant_id`,`username`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `invites` (
    `tenant_id`   VARCHAR(64)  NOT NULL DEFAULT 'default',
    `id`          VARCHAR(40)  NOT NULL,
    `role`        VARCHAR(16)  NOT NULL,
    `token_hash`  VARCHAR(64)  NOT NULL,
    `username`    VARCHAR(64)  DEFAULT NULL,
    `created_by`  VARCHAR(40)  DEFAULT NULL,
    `expires_at`  DATETIME(3)  DEFAULT NULL,
    `used_at`     DATETIME(3)  DEFAULT NULL,
    `used_by`     VARCHAR(40)  DEFAULT NULL,
    `created_at`  DATETIME(3)  DEFAULT NULL,
    PRIMARY KEY (`id`),
    UNIQUE INDEX `idx_invites_token` (`token_hash`),
    INDEX `idx_invites_expires` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `sessions` (
    `tenant_id`   VARCHAR(64)  NOT NULL DEFAULT 'default',
    `id`          VARCHAR(40)  NOT NULL,
    `user_id`     VARCHAR(40)  NOT NULL,
    `token_hash`  VARCHAR(64)  NOT NULL,
    `role`        VARCHAR(16)  NOT NULL,
    `expires_at`  DATETIME(3)  DEFAULT NULL,
    `created_at`  DATETIME(3)  DEFAULT NULL,
    `revoked_at`  DATETIME(3)  DEFAULT NULL,
    PRIMARY KEY (`id`),
    UNIQUE INDEX `idx_sessions_token` (`token_hash`),
    INDEX `idx_sessions_user` (`user_id`),
    INDEX `idx_sessions_expires` (`expires_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
