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
    `last_head_sha`          VARCHAR(64)     DEFAULT NULL,
    `created_at`             DATETIME(3)     DEFAULT NULL,
    `updated_at`             DATETIME(3)     DEFAULT NULL,
    PRIMARY KEY (`id`),
    UNIQUE INDEX `idx_target` (`platform`, `owner`, `repo`, `number`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
