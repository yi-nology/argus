// Package storage 持久化（design §5.2）：GORM + 多方言（SQLite 单文件起步 /
// MySQL / PostgreSQL，v5.4.8 生产切 pg）。模型定义见 models.go，操作面按域
// 归位（store_task/prints/printalias/reviewtarget/...），本文件只留 Store
// 生命周期（Open/Close/Ping）与包级小工具。
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Store 存储门面。SQLite 下用连接数=1 + 写锁实现"查询与落库同一事务"的串行语义
// （design §5.2：SQLite 单写串行下单事务即闭环）；MySQL/pg 下并发正确性由行锁/
// SKIP LOCKED/唯一索引承担，进程级互斥只制造吞吐天花板，写锁仅 SQLite 生效
// （v3.53.0 writeLock/writeUnlock）。
type Store struct {
	db      *gorm.DB
	writeMu sync.Mutex
	dialect Dialect // 方言单源（v6.2.0）：写门/行锁/迁移 DDL 分流全看它
	// RunnerID 本实例标识（Open 后由 main 赋值；claim/心跳用它归属 running 任务）
	RunnerID string
}

// isSQLite/isMySQL 方言判定（全包唯一出处——此前 Store.sqlite/Store.mysql 双
// bool 与 isSQLite(tx) 三源并存，见 dialect.go 头注释）。
func (s *Store) isSQLite() bool { return s.dialect == DialectSQLite }
func (s *Store) isMySQL() bool  { return s.dialect == DialectMySQL }

// writeLock/writeUnlock 进程内写串行门（v3.53.0 SQLite 感知）：writeMu 为 SQLite
// 单写者语义而生。MySQL 路径各写点的并发正确性均有 DB 侧机制承担——claim 走
// SKIP LOCKED 事务、webhook 建任务对 target 行 FOR UPDATE、幂等靠唯一索引；
// 单任务执行体唯一，attempt 读改写与 checkpoint 合并无同任务并发。多副本
// MySQL 横向扩容前的必要解锁。
func (s *Store) writeLock() {
	if s.isSQLite() {
		s.writeMu.Lock()
	}
}

func (s *Store) writeUnlock() {
	if s.isSQLite() {
		s.writeMu.Unlock()
	}
}

// withWrite 写门内执行 fn（v6.2.0 结构化封装）：取锁与 defer 解锁闭合在本函数，
// 「忘 defer 解锁 → SQLite 写锁永久持有」从纪律问题变成语法上不可能。此前
// writeLock/defer writeUnlock 两行配对散落 30 处，全靠人肉对齐。多返回值站点
// 以具名结果在闭包内赋值；嵌套调用带写门的方法会死锁（sync.Mutex 不可重入），
// 调用方须自查（现状 30 处无嵌套，收编不改变锁覆盖范围）。
func (s *Store) withWrite(fn func() error) error {
	s.writeLock()
	defer s.writeUnlock()
	return fn()
}

// ErrNotFound 查询无结果的边界哨兵：上层判 404/无此对象以此为准，不再感知
// gorm.ErrRecordNotFound——ORM 错误语义不出 storage 包。
var ErrNotFound = errors.New("storage: record not found")

// gormPlugin 可选 GORM 插件（v3.163.0 oekit/tracing 接入缝）：组合根在 Open 前
// 经 SetGormPlugin 注入（gormotel），Open 后 db.Use 挂载——DB 查询成 OTel span。
// nil = 不挂（缺省，零开销）。
var gormPlugin gorm.Plugin

// SetGormPlugin 注入 GORM 插件（须在 Open 前调用；后调用者生效）。
func SetGormPlugin(p gorm.Plugin) { gormPlugin = p }

// Open 打开数据库并自动迁移。DSN 路由（v5.4.8 三态）：postgres://|postgresql://
// 前缀 → PostgreSQL；"mysql://" 前缀或含 "@tcp("（go-sql-driver 形态）→ MySQL；
// 否则 SQLite——不做子串 "mysql" 判断：路径含 mysql 的 SQLite 文件会误判。
func Open(dsn string) (*Store, error) {
	s := &Store{}
	if strings.HasPrefix(strings.ToLower(dsn), "postgres://") || strings.HasPrefix(strings.ToLower(dsn), "postgresql://") {
		db, err := gorm.Open(postgresOpen(dsn), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Warn),
		})
		if err != nil {
			return nil, fmt.Errorf("storage: 打开 postgres 失败: %w", err)
		}
		sqlDB, _ := db.DB()
		sqlDB.SetConnMaxLifetime(time.Hour)
		s.db, s.dialect = db, DialectPostgres
	} else if strings.HasPrefix(strings.ToLower(dsn), "mysql://") || strings.Contains(dsn, "@tcp(") {
		// mysql:// 是便利前缀（go-sql-driver 不认识），大小写不敏感剥离
		if len(dsn) >= 8 && strings.EqualFold(dsn[:8], "mysql://") {
			dsn = dsn[8:]
		}
		db, err := gorm.Open(mysqlOpen(dsn), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Warn),
		})
		if err != nil {
			return nil, fmt.Errorf("storage: 打开 mysql 失败: %w", err)
		}
		sqlDB, _ := db.DB()
		sqlDB.SetConnMaxLifetime(time.Hour)
		s.db, s.dialect = db, DialectMySQL
	} else {
		// glebarez/sqlite 纯 Go 驱动；缺省注入 busy_timeout + WAL（§5.2 并发注意）。
		// DSN 已带 query 时以 & 追加（v3.9.1：此前含 ? 的 DSN 会静默丢失
		// busy_timeout/WAL，并发写行为与默认路径漂移）。
		if strings.Contains(dsn, "?") {
			dsn += "&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
		} else {
			dsn += "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
		}
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Warn),
		})
		if err != nil {
			return nil, fmt.Errorf("storage: 打开 sqlite 失败: %w", err)
		}
		sqlDB, _ := db.DB()
		// WAL 下读写可并发（单写多读），连接数 1 会让大查询/大 DELETE 队头阻塞
		// 心跳读，错过 stale 判定窗口触发误重置双跑（深挖修复）。写-写竞争由
		// 进程内 writeMu + busy_timeout(10s) 兜底；幂等/去重正确性来自唯一索引
		// 与事务，不依赖连接数=1。
		sqlDB.SetMaxOpenConns(4)
		sqlDB.SetMaxIdleConns(4) // idle=max-open 避免连接反复开关的 WAL 开销
		// SQLite 不设 ConnMaxLifetime：单文件单连接，文件替换由 OS 语义保证一致性
		s.db, s.dialect = db, DialectSQLite
	}
	// OTel 追踪插件（可选）：挂在 AutoMigrate 之前——迁移查询也留 span。
	if gormPlugin != nil {
		if uerr := s.db.Use(gormPlugin); uerr != nil {
			return nil, fmt.Errorf("storage: 挂载 gorm 插件失败: %w", uerr)
		}
	}
	if err := s.db.AutoMigrate(&Task{}, &FindingPrint{}, &PrintAlias{}, &ReviewTarget{}, &LeaderLease{}, &QASession{}, &PatternSignal{}, &SchemaMigration{}, &CommentDelivery{}, &RepoSetting{}); err != nil {
		return nil, fmt.Errorf("storage: 自动迁移失败: %w", err)
	}
	if s.isMySQL() {
		if err := s.upgradeMySQLLongText(); err != nil {
			return nil, err
		}
	}
	// 指纹 v2 存量迁移（v3.7.0）：v1 行按存量 file/line/comment 重算回写。
	// v3.8.0 起由 SchemaMigration 哨兵门控（成功一次后不再每次启动全表扫）；
	// 失败不阻断启动（留待下次重试），只告警。
	if n, merr := s.ensureMigrationApplied(context.Background(), "fingerprint_v2", s.migratePrintFingerprintsV2); merr != nil {
		s.db.Logger.Warn(context.Background(), "指纹 v2 迁移失败（下次启动重试）: %v", merr)
	} else if n > 0 {
		s.db.Logger.Info(context.Background(), "指纹 v2 迁移完成: %d 行", n)
	}
	// print_aliases PR 域迁移（v3.9.2）：alias_fp 全局主键 → (PR, alias_fp) 复合
	// 主键——两个 PR 同款措辞漂移不再撞主键回滚；存量按 print_fp 反查 PR 回填。
	if n, merr := s.ensureMigrationApplied(context.Background(), "print_alias_pr_scope", s.migratePrintAliasPRScope); merr != nil {
		s.db.Logger.Warn(context.Background(), "print_aliases PR 域迁移失败（下次启动重试）: %v", merr)
	} else if n > 0 {
		s.db.Logger.Info(context.Background(), "print_aliases PR 域迁移完成: %d 行", n)
	}
	return s, nil
}

// longtextColumns MySQL 下需要 LONGTEXT 的大文本列。GORM `type:text` 在 MySQL 生成
// TEXT（64KB 上限）：大报告/调试产物写入失败 → 任务卡 running → 重启重置重跑 →
// 再失败，每次重启白烧一遍 LLM。AutoMigrate 之后幂等升级。
var longtextColumns = map[string][]string{
	"tasks":          {"report", "result_json", "fail_reason", "artifacts_json", "debug_json"},
	"finding_prints": {"comment"},
}

func (s *Store) upgradeMySQLLongText() error {
	for table, cols := range longtextColumns {
		for _, col := range cols {
			var cnt int64
			// 探测失败必须暴露：静默跳过 ALTER 会让 64KB 死循环修复无声失效
			if err := s.db.Raw("SELECT COUNT(*) FROM information_schema.COLUMNS "+
				"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ? AND DATA_TYPE = 'text'",
				table, col).Scan(&cnt).Error; err != nil {
				return fmt.Errorf("storage: 探测 %s.%s 列类型失败: %w", table, col, err)
			}
			if cnt == 0 {
				continue
			}
			if err := s.db.Exec(fmt.Sprintf("ALTER TABLE `%s` MODIFY `%s` LONGTEXT", table, col)).Error; err != nil {
				return fmt.Errorf("storage: %s.%s 升级 LONGTEXT 失败: %w", table, col, err)
			}
		}
	}
	return nil
}

// Close 关闭底层连接。
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Ping 健康检查（readyz）。
func (s *Store) Ping(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return sqlDB.PingContext(cctx)
}

// NewTaskID 生成任务 ID（service/poller 共用单一出处）。
func NewTaskID() string {
	return "t-" + strings.ToLower(strings.ReplaceAll(uuid.NewString(), "-", ""))[:16] + time.Now().Format("01021504")
}

// DecodeJSONMap 解码 Task 的 JSON 对象列（artifacts/debug/stage_artifacts）：
// 空串/坏数据安全返回空表。runner 写入与 service/MCP 读取共用单源（v3.99.0，
// 此前两端各持一份同形解码，字段契约无钉）。
func DecodeJSONMap(s string) map[string]string {
	m := map[string]string{}
	if s == "" {
		return m
	}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

// wherePR PR 四元组定位条件（storage 各表通用单源，v3.99.0：8 处同形
// Where 字符串收敛）。
func wherePR(q *gorm.DB, platform, owner, repo, number string) *gorm.DB {
	return q.Where("platform = ? AND owner = ? AND repo = ? AND number = ?", platform, owner, repo, number)
}

// quietDB 预期内"查无记录"的路径专用：GORM 默认把 ErrRecordNotFound 打成红色错误日志，
// worker 每 500ms 空轮询一次会刷屏掩盖真实告警——这些路径静默。
func (s *Store) quietDB() *gorm.DB {
	return s.db.Session(&gorm.Session{Logger: logger.Discard})
}

// retryBackoff 第 attempt 次失败的退避：base×3^(attempt-1)，封顶 1h。
func retryBackoff(base time.Duration, attempt int) time.Duration {
	d := base
	for i := 1; i < attempt && d < time.Hour; i++ {
		d *= 3
	}
	if d > time.Hour {
		d = time.Hour
	}
	return d
}
