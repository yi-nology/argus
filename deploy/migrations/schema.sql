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
    UNIQUE INDEX `idx_tasks_event_id` (`event_id`),
    INDEX `idx_tasks_status` (`status`),
    INDEX `idx_tasks_runner_id` (`runner_id`),
    INDEX `idx_tasks_head_sha` (`head_sha`),
    INDEX `idx_tasks_created_at` (`created_at`),
    INDEX `idx_tasks_platform_owner_repo_number` (`platform`, `owner`, `repo`, `number`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- finding_prints — cross-round finding fingerprints
-- ============================================================
CREATE TABLE IF NOT EXISTS `finding_prints` (
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
    UNIQUE INDEX `idx_print` (`platform`, `owner`, `repo`, `number`, `print`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- pattern_signals — repo-level review pattern trust history
-- (regret learning loop; see docs/review-closed-loop-design.md)
-- ============================================================
CREATE TABLE IF NOT EXISTS `pattern_signals` (
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
    UNIQUE INDEX `idx_pattern` (`platform`, `owner`, `repo`, `plugin`, `pattern_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- review_targets — per-PR summary comment + last head SHA tracking
-- ============================================================
CREATE TABLE IF NOT EXISTS `review_targets` (
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
    UNIQUE INDEX `idx_target` (`platform`, `owner`, `repo`, `number`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ============================================================
-- print_aliases — fingerprint short-code / wording-drift aliases
-- (alias_fp of a new wording → print_fp of the canonical entry;
-- near-duplicate merge layer, see internal/storage/printalias.go)
-- ============================================================
CREATE TABLE IF NOT EXISTS `print_aliases` (
    `alias_fp`   VARCHAR(64) NOT NULL,
    `print_fp`   VARCHAR(64) DEFAULT NULL,
    `updated_at` DATETIME(3) DEFAULT NULL,
    PRIMARY KEY (`alias_fp`),
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
    `session_key`  VARCHAR(160) NOT NULL,
    `history_json` LONGTEXT,
    `updated_at`   DATETIME(3)  DEFAULT NULL,
    PRIMARY KEY (`session_key`),
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
    `delivery_id` VARCHAR(128) NOT NULL,
    `seen_at`     DATETIME(3)  DEFAULT NULL,
    PRIMARY KEY (`delivery_id`),
    INDEX `idx_comment_deliveries_seen_at` (`seen_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
