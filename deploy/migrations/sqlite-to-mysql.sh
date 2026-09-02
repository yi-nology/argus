#!/usr/bin/env bash
# sqlite-to-mysql.sh — Migrate Argus data from SQLite to MySQL 8.0.
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

dump_sqlite_table() {
    local table="$1"
    sqlite3 -separator $'\t' -nullvalue '\N' "$SQLITE_PATH" "SELECT * FROM ${table}"
}

# Escape tabs and newlines in field values for LOAD DATA safety.
# SQLite's .separator mode doesn't quote fields, so embedded tabs/newlines
# would corrupt the import. We use a Python one-liner to re-encode.
safe_export() {
    local table="$1"
    sqlite3 -json "$SQLITE_PATH" "SELECT * FROM ${table}" \
        | python3 -c "
import json, sys
for row in json.load(sys.stdin):
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
    safe_export tasks > "$tmpfile"
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
         @runner_id, @heartbeat_at, @created_at, @updated_at, @started_at, @finished_at)
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
            runner_id        = NULLIF(@runner_id, ''),
            heartbeat_at     = NULLIF(@heartbeat_at, '\\\\N'),
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
    safe_export finding_prints > "$tmpfile"
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
         @first_task_id, @last_task_id, times_seen,
         @first_seen_at, @last_seen_at, @created_at, @updated_at)
        SET
            file           = NULLIF(@file, ''),
            severity       = NULLIF(@severity, ''),
            comment        = NULLIF(@comment, ''),
            first_task_id  = NULLIF(@first_task_id, ''),
            last_task_id   = NULLIF(@last_task_id, ''),
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
    safe_export review_targets > "$tmpfile"
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
         last_summary_comment_id, @last_head_sha,
         @created_at, @updated_at)
        SET
            last_head_sha  = NULLIF(@last_head_sha, ''),
            created_at     = NULLIF(@created_at, '\\\\N'),
            updated_at     = NULLIF(@updated_at, '\\\\N')
    "
    log "  Imported ${rows} rows into review_targets"
}

# ── Verification ─────────────────────────────────────────────────────────────

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
