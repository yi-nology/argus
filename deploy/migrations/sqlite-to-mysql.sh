#!/usr/bin/env bash
# sqlite-to-mysql.sh — Migrate Argus data from SQLite to MySQL 8.0.
#
# 适用范围：源库 schema ≥ v3.0.x。脚本按**列名**对齐导出（PRAGMA table_info 取源库
# 实际列集，与 target schema 期望列清单比对），源库缺失的列自动补 NULL（如 v3.0.x
# 没有 runner_id/heartbeat_at/attempt/next_retry_at、finding_prints 没有
# plugin/regret_count），多出的列报错退出——任何源版本导出都与 LOAD DATA 列清单严格对齐。
#
# Usage:
#   ./sqlite-to-mysql.sh [--dry-run] [--rollback] [--sqlite PATH] [--mysql-dsn DSN]
#
# Environment variables (override flags):
#   SQLITE_PATH   — Path to the SQLite database file (default: ./argus.db)
#   MYSQL_DSN     — MySQL DSN in go-sql-driver format (default: root@tcp(localhost:3306)/argus)
#
# The script:
#   1. Runs pre-flight checks (sqlite3 CLI, mysql CLI, SQLite file exists, MySQL reachable)
#   2. Verifies target tables do not already exist (safety)
#   3. Creates the schema from schema.sql
#   4. Dumps SQLite data to tab-separated values
#   5. Imports into MySQL
#   6. The application's upgradeMySQLLongText() handles TEXT→LONGTEXT on first startup
#
# Rollback (--rollback) drops the three tables if they were created by this script.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCHEMA_FILE="${SCRIPT_DIR}/schema.sql"

# Defaults
SQLITE_PATH="${SQLITE_PATH:-./argus.db}"
MYSQL_DSN="${MYSQL_DSN:-root@tcp(localhost:3306)/argus}"
DRY_RUN=false
ROLLBACK=false

# Parse arguments
while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run)    DRY_RUN=true; shift ;;
        --rollback)   ROLLBACK=true; shift ;;
        --sqlite)     SQLITE_PATH="$2"; shift 2 ;;
        --mysql-dsn)  MYSQL_DSN="$2"; shift 2 ;;
        -h|--help)
            sed -n '3,/^$/p' "$0" | sed 's/^# \?//'
            exit 0
            ;;
        *)
            echo "ERROR: Unknown argument: $1" >&2
            exit 1
            ;;
    esac
done

# ── Helpers ──────────────────────────────────────────────────────────────────

log()  { echo "[migrate] $*"; }
err()  { echo "[migrate] ERROR: $*" >&2; }
warn() { echo "[migrate] WARN: $*" >&2; }

# Parse the go-sql-driver DSN into host, port, user, password, dbname.
# Format: [user[:password]@][net[(addr)]/dbname[?param1=val1&...]
parse_dsn() {
    local dsn="$1"
    # Strip query params
    dsn="${dsn%%\?*}"
    # Extract user:pass@host
    MYSQL_USER="${dsn%%@*}"
    local after_at="${dsn#*@}"
    # after_at = tcp(host:port)/dbname
    local tcp_part="${after_at%%/*}"
    MYSQL_DB="${after_at#*/}"
    tcp_part="${tcp_part#tcp(}"
    tcp_part="${tcp_part%)}"
    MYSQL_HOST="${tcp_part%%:*}"
    MYSQL_PORT="${tcp_part#*:}"
    [[ "$MYSQL_PORT" == "$MYSQL_HOST" ]] && MYSQL_PORT=3306
    # Split user:password
    if [[ "$MYSQL_USER" == *:* ]]; then
        MYSQL_PASS="${MYSQL_USER#*:}"
        MYSQL_USER="${MYSQL_USER%%:*}"
    else
        MYSQL_PASS=""
    fi
}

mysql_cmd() {
    local args=( -h "$MYSQL_HOST" -P "$MYSQL_PORT" -u "$MYSQL_USER" )
    [[ -n "$MYSQL_PASS" ]] && args+=( -p"$MYSQL_PASS" )
    args+=( "$MYSQL_DB" )
    mysql "${args[@]}" "$@"
}

mysql_exec() {
    mysql_cmd -e "$1"
}

# ── Pre-flight checks ───────────────────────────────────────────────────────

preflight() {
    local ok=true

    if ! command -v sqlite3 &>/dev/null; then
        err "sqlite3 CLI not found. Install it first."
        ok=false
    fi

    if ! command -v mysql &>/dev/null; then
        err "mysql CLI not found. Install it first."
        ok=false
    fi

    if [[ ! -f "$SQLITE_PATH" ]]; then
        err "SQLite file not found: $SQLITE_PATH"
        ok=false
    fi

    if [[ ! -f "$SCHEMA_FILE" ]]; then
        err "Schema file not found: $SCHEMA_FILE"
        ok=false
    fi

    $ok || exit 1

    parse_dsn "$MYSQL_DSN"
    log "Parsed DSN: user=$MYSQL_USER host=$MYSQL_HOST:$MYSQL_PORT db=$MYSQL_DB"

    if ! mysql_exec "SELECT 1" &>/dev/null; then
        err "Cannot connect to MySQL at $MYSQL_HOST:$MYSQL_PORT (user=$MYSQL_USER, db=$MYSQL_DB)"
        exit 1
    fi
    log "MySQL connection OK"

    # Check that target tables do not already have data
    for tbl in tasks finding_prints review_targets; do
        local cnt
        cnt=$(mysql_exec "SELECT COUNT(*) FROM \`${tbl}\`" 2>/dev/null | tail -1) || cnt=0
        if [[ "$cnt" -gt 0 ]]; then
            err "Table '${tbl}' already contains ${cnt} rows. Aborting to prevent data loss."
            err "Drop the tables first or use --rollback, then re-run."
            exit 1
        fi
    done
    log "Target tables are empty or do not exist — safe to proceed"
}

# ── Rollback ─────────────────────────────────────────────────────────────────

do_rollback() {
    log "Rolling back: dropping tasks, finding_prints, review_targets"
    if $DRY_RUN; then
        log "[DRY RUN] Would execute: DROP TABLE IF EXISTS tasks, finding_prints, review_targets"
        return
    fi
    mysql_exec "SET FOREIGN_KEY_CHECKS=0"
    mysql_exec "DROP TABLE IF EXISTS tasks, finding_prints, review_targets"
    mysql_exec "SET FOREIGN_KEY_CHECKS=1"
    log "Rollback complete"
}

# ── Schema creation ──────────────────────────────────────────────────────────

create_schema() {
    log "Creating schema from ${SCHEMA_FILE}"
    if $DRY_RUN; then
        log "[DRY RUN] Would execute schema.sql against MySQL"
        return
    fi
    mysql_cmd < "$SCHEMA_FILE"
    log "Schema created"
}

# ── Data export from SQLite ──────────────────────────────────────────────────
# We export as tab-separated values with NULL represented as \N (MySQL LOAD DATA convention).

# 期望列清单（= target schema（schema.sql）全列，顺序 = LOAD DATA 列顺序）。
TASKS_COLS=(id kind platform owner repo number head_sha event_type event_id status verdict report result_json fail_reason tokens_used summary_comment_id artifacts_json debug_json runner_id heartbeat_at attempt next_retry_at created_at updated_at started_at finished_at)
FINDING_PRINTS_COLS=(id platform owner repo number print file line severity comment status plugin first_task_id last_task_id times_seen regret_count first_seen_at last_seen_at created_at updated_at)
REVIEW_TARGETS_COLS=(id platform owner repo number last_summary_comment_id known_comment_ids last_head_sha created_at updated_at)

# source_columns <table> — 源库实际列集（PRAGMA table_info，每行一列名）。
source_columns() {
    sqlite3 "$SQLITE_PATH" "PRAGMA table_info(${1})" | cut -d'|' -f2
}

# build_select <table> <expected cols...> — 按期望列清单构建显式列名 SELECT：
#   源库缺失的列 → NULL AS <col> 补位；源库多出的列 → 报错退出。
# 这样导出的列序恒等于期望列清单，与 LOAD DATA 列清单严格对齐，
# 不再依赖 SELECT * 的位置序（v3.0.x tasks 22 列对全列清单必然错位）。
build_select() {
    local table="$1"; shift
    local -a expected=("$@")
    local actual c
    actual=$(source_columns "$table")
    if [[ -z "$actual" ]]; then
        err "Table '${table}' not found in source SQLite: ${SQLITE_PATH}"
        exit 1
    fi
    # 源库多出的列：超出本脚本认知的 schema，无法安全映射，直接报错
    while IFS= read -r c; do
        if [[ -z "$c" ]]; then continue; fi
        if ! printf '%s\n' "${expected[@]}" | grep -qx "$c"; then
            err "Source table '${table}' has unexpected column '${c}' (not in the expected list) — aborting."
            exit 1
        fi
    done <<< "$actual"
    local -a parts=()
    for c in "${expected[@]}"; do
        if printf '%s\n' "$actual" | grep -qx "$c"; then
            parts+=("\"${c}\"")
        else
            warn "Source table '${table}' lacks column '${c}' — exporting NULL AS ${c}"
            parts+=("NULL AS \"${c}\"")
        fi
    done
    local joined
    joined=$(IFS=', '; echo "${parts[*]}")
    echo "SELECT ${joined} FROM ${table}"
}

# Escape tabs and newlines in field values for LOAD DATA safety.
# SQLite's .separator mode doesn't quote fields, so embedded tabs/newlines
# would corrupt the import. We use a Python one-liner to re-encode.
# 注意：report/result_json/debug_json（tasks）与 comment（finding_prints）是
# LONGTEXT 大文本列，多行/带 Tab 内容靠这里的转义保证导入安全。
# $1 = table（仅用于标识），$2 = build_select 产出的显式列名 SELECT。
# 输出走 stdout（会被 import_* 重定向进 tmpfile），因此本函数内禁止 log() 打屏。
safe_export() {
    local table="$1"
    local select_sql="$2"
    sqlite3 -json "$SQLITE_PATH" "${select_sql}" \
        | python3 -c "
import json, sys
data = sys.stdin.read().strip()
for row in (json.loads(data) if data else []):
    fields = []
    for v in row.values():
        if v is None:
            fields.append('\\\\N')
        else:
            s = str(v)
            s = s.replace('\\\\', '\\\\\\\\')
            s = s.replace('\\t', '\\\\t')
            s = s.replace('\\n', '\\\\n')
            s = s.replace('\\r', '\\\\r')
            fields.append(s)
    sys.stdout.write('\\t'.join(fields) + '\\n')
"
}

# ── Data import ──────────────────────────────────────────────────────────────

import_tasks() {
    local tmpfile
    tmpfile=$(mktemp)
    trap "rm -f '$tmpfile'" RETURN

    log "Exporting tasks from SQLite..."
    safe_export tasks "$(build_select tasks "${TASKS_COLS[@]}")" > "$tmpfile"
    local rows
    rows=$(wc -l < "$tmpfile" | tr -d ' ')
    log "  Exported ${rows} rows"

    if $DRY_RUN; then
        log "[DRY RUN] Would LOAD ${rows} rows into tasks"
        head -3 "$tmpfile" | while IFS= read -r line; do log "  sample: ${line:0:120}..."; done
        return
    fi

    mysql_exec "
        LOAD DATA LOCAL INFILE '${tmpfile}'
        INTO TABLE tasks
        CHARACTER SET utf8mb4
        FIELDS TERMINATED BY '\t'
        ESCAPED BY '\\\\'
        LINES TERMINATED BY '\n'
        (id, kind, platform, owner, repo, number,
         @head_sha, @event_type, @event_id,
         status, @verdict, @report, @result_json, @fail_reason,
         tokens_used, summary_comment_id, @artifacts_json, @debug_json,
         @runner_id, @heartbeat_at, @attempt, @next_retry_at,
         @created_at, @updated_at, @started_at, @finished_at)
        SET
            head_sha         = NULLIF(@head_sha, ''),
            event_type       = NULLIF(@event_type, ''),
            event_id         = NULLIF(@event_id, '\\\\N'),
            verdict          = NULLIF(@verdict, ''),
            report           = NULLIF(@report, ''),
            result_json      = NULLIF(@result_json, ''),
            fail_reason      = NULLIF(@fail_reason, ''),
            artifacts_json   = NULLIF(@artifacts_json, ''),
            debug_json       = NULLIF(@debug_json, ''),
            runner_id        = NULLIF(@runner_id, '\\\\N'),
            heartbeat_at     = NULLIF(@heartbeat_at, '\\\\N'),
            attempt          = COALESCE(NULLIF(@attempt, '\\\\N'), 0),
            next_retry_at    = NULLIF(@next_retry_at, '\\\\N'),
            created_at       = NULLIF(@created_at, '\\\\N'),
            updated_at       = NULLIF(@updated_at, '\\\\N'),
            started_at       = NULLIF(@started_at, '\\\\N'),
            finished_at      = NULLIF(@finished_at, '\\\\N')
    "
    log "  Imported ${rows} rows into tasks"
}

import_finding_prints() {
    local tmpfile
    tmpfile=$(mktemp)
    trap "rm -f '$tmpfile'" RETURN

    log "Exporting finding_prints from SQLite..."
    safe_export finding_prints "$(build_select finding_prints "${FINDING_PRINTS_COLS[@]}")" > "$tmpfile"
    local rows
    rows=$(wc -l < "$tmpfile" | tr -d ' ')
    log "  Exported ${rows} rows"

    if $DRY_RUN; then
        log "[DRY RUN] Would LOAD ${rows} rows into finding_prints"
        return
    fi

    mysql_exec "
        LOAD DATA LOCAL INFILE '${tmpfile}'
        INTO TABLE finding_prints
        CHARACTER SET utf8mb4
        FIELDS TERMINATED BY '\t'
        ESCAPED BY '\\\\'
        LINES TERMINATED BY '\n'
        (id, platform, owner, repo, number, print,
         @file, line, @severity, @comment, status,
         @plugin, @first_task_id, @last_task_id, times_seen,
         @regret_count, @first_seen_at, @last_seen_at, @created_at, @updated_at)
        SET
            file           = NULLIF(@file, ''),
            severity       = NULLIF(@severity, ''),
            comment        = NULLIF(@comment, ''),
            plugin         = NULLIF(@plugin, '\\\\N'),
            first_task_id  = NULLIF(@first_task_id, ''),
            last_task_id   = NULLIF(@last_task_id, ''),
            regret_count   = COALESCE(NULLIF(@regret_count, '\\\\N'), 0),
            first_seen_at  = NULLIF(@first_seen_at, '\\\\N'),
            last_seen_at   = NULLIF(@last_seen_at, '\\\\N'),
            created_at     = NULLIF(@created_at, '\\\\N'),
            updated_at     = NULLIF(@updated_at, '\\\\N')
    "
    log "  Imported ${rows} rows into finding_prints"
}

import_review_targets() {
    local tmpfile
    tmpfile=$(mktemp)
    trap "rm -f '$tmpfile'" RETURN

    log "Exporting review_targets from SQLite..."
    safe_export review_targets "$(build_select review_targets "${REVIEW_TARGETS_COLS[@]}")" > "$tmpfile"
    local rows
    rows=$(wc -l < "$tmpfile" | tr -d ' ')
    log "  Exported ${rows} rows"

    if $DRY_RUN; then
        log "[DRY RUN] Would LOAD ${rows} rows into review_targets"
        return
    fi

    mysql_exec "
        LOAD DATA LOCAL INFILE '${tmpfile}'
        INTO TABLE review_targets
        CHARACTER SET utf8mb4
        FIELDS TERMINATED BY '\t'
        ESCAPED BY '\\\\'
        LINES TERMINATED BY '\n'
        (id, platform, owner, repo, number,
         last_summary_comment_id, @known_comment_ids, @last_head_sha,
         @created_at, @updated_at)
        SET
            known_comment_ids = NULLIF(@known_comment_ids, '\\\\N'),
            last_head_sha  = NULLIF(@last_head_sha, ''),
            created_at     = NULLIF(@created_at, '\\\\N'),
            updated_at     = NULLIF(@updated_at, '\\\\N')
    "
    log "  Imported ${rows} rows into review_targets"
}

# ── Verification ─────────────────────────────────────────────────────────────

# sample_verify_tasks_created_at — 抽样校验：随机取 1 行 tasks，源/目标各取该行
# id 的 created_at 对比（行数比对拦不住列错位——错位时行数照样一致，时间戳却串列）。
# 源串可能带时区后缀（glebarez/sqlite 存 RFC3339 形态），带偏移量时按绝对时间
# （UNIX_TIMESTAMP）比，无偏移时按归一化后的分量串比。
sample_verify_tasks_created_at() {
    if $DRY_RUN; then
        log "  (dry-run) skipping tasks.created_at sample check"
        return 0
    fi
    local sid src_at my_unix my_fmt
    sid=$(sqlite3 "$SQLITE_PATH" "SELECT id FROM tasks ORDER BY RANDOM() LIMIT 1")
    if [[ -z "$sid" ]]; then
        log "  tasks is empty — skipping created_at sample check"
        return 0
    fi
    src_at=$(sqlite3 "$SQLITE_PATH" "SELECT created_at FROM tasks WHERE id = '${sid}'")
    my_unix=$(mysql_exec "SELECT COALESCE(UNIX_TIMESTAMP(created_at), 0) FROM tasks WHERE id = '${sid}'" | tail -1)
    my_fmt=$(mysql_exec "SELECT COALESCE(DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s'), '') FROM tasks WHERE id = '${sid}'" | tail -1)
    if python3 - "$src_at" "$my_unix" "$my_fmt" <<'PYEOF'
import calendar, re, sys
src, my_unix, my_fmt = (a.strip() for a in sys.argv[1:4])
m = re.match(r"(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2}):(\d{2})", src)
if not m:
    sys.exit(1)
Y, Mo, D, H, Mi, S = map(int, m.groups())
src_base = "%04d-%02d-%02d %02d:%02d:%02d" % (Y, Mo, D, H, Mi, S)
off = re.search(r"([+-])(\d{2}):?(\d{2})$", src)
is_utc_z = bool(re.search(r"Z$", src))
if off or is_utc_z:
    src_epoch = calendar.timegm((Y, Mo, D, H, Mi, S, 0, 0, 0))
    if off:
        sign = 1 if off.group(1) == "+" else -1
        src_epoch -= sign * (int(off.group(2)) * 3600 + int(off.group(3)) * 60)
    sys.exit(0 if my_unix.lstrip("-").isdigit() and int(my_unix) == src_epoch else 1)
sys.exit(0 if src_base == my_fmt else 1)
PYEOF
    then
        log "  created_at sample check OK (id=${sid})"
    else
        err "  created_at sample check FAILED for id=${sid}: SQLite='${src_at}' MySQL(unix)=${my_unix} MySQL(fmt)=${my_fmt}"
        exit 1
    fi
}

verify() {
    log "Verifying row counts..."
    for tbl in tasks finding_prints review_targets; do
        local sqlite_cnt mysql_cnt
        sqlite_cnt=$(sqlite3 "$SQLITE_PATH" "SELECT COUNT(*) FROM ${tbl}")
        if $DRY_RUN; then
            log "  ${tbl}: SQLite=${sqlite_cnt} (skipping MySQL check in dry-run)"
        else
            mysql_cnt=$(mysql_exec "SELECT COUNT(*) FROM \`${tbl}\`" | tail -1)
            if [[ "$sqlite_cnt" == "$mysql_cnt" ]]; then
                log "  ${tbl}: SQLite=${sqlite_cnt} MySQL=${mysql_cnt} OK"
            else
                err "  ${tbl}: count mismatch — SQLite=${sqlite_cnt} MySQL=${mysql_cnt}"
                exit 1
            fi
        fi
    done
    sample_verify_tasks_created_at
}

# ── Main ─────────────────────────────────────────────────────────────────────

main() {
    if $ROLLBACK; then
        parse_dsn "$MYSQL_DSN"
        do_rollback
        exit 0
    fi

    log "Argus SQLite → MySQL migration"
    log "  SQLite: ${SQLITE_PATH}"
    log "  MySQL:  ${MYSQL_DSN}"
    $DRY_RUN && log "  Mode: DRY RUN (no changes will be made)"
    echo

    preflight
    echo

    create_schema
    echo

    import_tasks
    import_finding_prints
    import_review_targets
    echo

    verify
    echo

    log "Migration complete."
    log "Note: The application's upgradeMySQLLongText() will run on first startup"
    log "      to promote TEXT columns to LONGTEXT. This is expected and idempotent."
}

main "$@"
