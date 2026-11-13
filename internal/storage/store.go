// store.go —— Store 生命周期（Open/Close/Ping）与包级小工具（包文档见 doc.go）。

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	// fallbackID 迁移锁 holder 的兜底身份（评估 P0 修复）：RunnerID 未装配
	//（测试/工具直连）时进程内惰性生成唯一 ID。holder 相同会让 TryAcquire 的
	// `holder = ? OR expires_at < ?` 条件互相"续约"抢锁（双实例并发首启同跑迁移）。
	fallbackOnce sync.Once
	fallbackID   string
	// inFlightGuardActive 在飞唯一防线建立结果（v6.12 P1 readyz 探针用；
	// warnInFlightGuard 在 Open 时写入，正常启动后即为真实值）。
	inFlightGuardActive bool
}

// instanceID 本实例唯一身份：RunnerID 优先，未装配时惰性生成进程内唯一 ID
// （同进程多 Store 实例也能区分 holder）。
func (s *Store) instanceID() string {
	if s.RunnerID != "" {
		return s.RunnerID
	}
	s.fallbackOnce.Do(func() { s.fallbackID = "fallback-" + uuid.NewString()[:16] })
	return s.fallbackID
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
	ctx := context.Background()
	if err := s.migrateSchema(ctx); err != nil {
		return nil, err
	}
	if err := s.migrateData(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// migrateSchema schema 迁移（AutoMigrate + MySQL LONGTEXT 升级），pg/MySQL 下
// 纳入迁移租约串行执行（P0-04，#5）：多副本并发首启时各副本并发 DDL（加列/
// 加索引）在 MySQL 可能死锁或索引名冲突。租约时序——
//  1. 先单表 AutoMigrate(LeaderLease)：租约机制的载体表必须先存在；CREATE
//     TABLE IF NOT EXISTS 并发安全（pg 行锁串行 / MySQL 原子 DDL / SQLite 文件锁），
//     相比全表 AutoMigrate 的并发窗口缩小到单语句。
//  2. 抢迁移租约（key=schema_migration:automigrate，holder=实例唯一）：拿不到 =
//     他副本正在迁移，让位轮询等待（2s 间隔，上界 TTL+2min）——多副本首启在此
//     串行化，后续副本 AutoMigrate 无变更零成本。超时 fail-closed 拒绝启动
//     （带并发 DDL 风险的带病启动比启动失败更危险）。
//  3. 持锁执行全表 AutoMigrate + LONGTEXT 升级，完成后释放。
//
// SQLite 单文件单写者无并发首启问题，直接执行（与 EnsureSameSHAGuard 同理）。
func (s *Store) migrateSchema(ctx context.Context) error {
	if s.isSQLite() {
		if err := s.autoMigrateAll(); err != nil {
			return err
		}
		return s.warnInFlightGuard()
	}
	if err := s.ensureLeaseTable(); err != nil {
		return err
	}
	release, err := s.acquireMigrationLease(ctx, automigrateLeaseKey)
	if err != nil {
		return err
	}
	defer release()
	if err := s.autoMigrateAll(); err != nil {
		return err
	}
	return s.warnInFlightGuard()
}

// warnInFlightGuard 在飞唯一防线建立（v6.8.0 P1-03，#8 三方言）+ 告警面：在迁移
// 租约内执行（建索引/加列是 DDL，多副本必须串行）。失败语义沿 v3.9.1 决策——
// 真实执行错误与历史在飞重复均只告警不阻断启动（防线退化为事务内检查，数据
// 收敛后重启自愈）。
//
// v6.12 P1（外部评估实锤）：默认告警降级，但 ARGUS_STRICT_GUARDS=1（生产严格
// 模式）时防线缺失拒绝启动（fail-closed）；InFlightGuardActive 供 /readyz
// 在严格模式下对防线存在性做启动后复核。
func (s *Store) warnInFlightGuard() error {
	active, err := s.EnsureInFlightGuard()
	s.inFlightGuardActive = active
	switch {
	case err != nil:
		s.db.Logger.Warn(context.Background(), "argus.boot 在飞唯一防线建立失败（双跑防线退化为事务内检查）: %v", err)
		if os.Getenv("ARGUS_STRICT_GUARDS") == "1" {
			return fmt.Errorf("storage: 在飞唯一防线建立失败（ARGUS_STRICT_GUARDS=1 严格模式拒绝启动）: %w", err)
		}
	case !active:
		s.db.Logger.Warn(context.Background(), "argus.boot tasks 表存在历史在飞重复，唯一索引未建立；清理重复任务后重启自愈")
		if os.Getenv("ARGUS_STRICT_GUARDS") == "1" {
			return fmt.Errorf("storage: 在飞唯一防线未建立（历史在飞重复；ARGUS_STRICT_GUARDS=1 严格模式拒绝启动，清理重复任务后重启）")
		}
	}
	return nil
}

// InFlightGuardActive 防线存在性（v6.12 readyz 探针用；未跑过 EnsureInFlightGuard
// 时为 false——storage.Open 必跑，正常启动后即为真实值）。
func (s *Store) InFlightGuardActive() bool { return s.inFlightGuardActive }

// ensureLeaseTable 租约载体表引导（P0-04）：GORM AutoMigrate 的 check-then-create
// 在两副本并发首启时撞数据库 catalog（MySQL 1050 table already exists / PG
// pg_type_typname_nsp_index 23505，CI 双方言矩阵实锤）——改 Migrator 判定 +
// 原生 CREATE TABLE IF NOT EXISTS（服务端幂等），DDL 竞态失败再复核一次表存在
// （他副本先建成功 = 本次放行，类型对齐全量 AutoMigrate 兜）。
func (s *Store) ensureLeaseTable() error {
	if s.db.Migrator().HasTable(&LeaderLease{}) {
		return nil
	}
	ddl := "CREATE TABLE IF NOT EXISTS leader_leases (" +
		"lease_key varchar(128) PRIMARY KEY, holder varchar(128), " +
		"expires_at "
	if s.isMySQL() {
		ddl += "datetime(3), updated_at datetime(3)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"
	} else {
		ddl += "timestamptz, updated_at timestamptz)"
	}
	if err := s.db.Exec(ddl).Error; err != nil {
		if s.db.Migrator().HasTable(&LeaderLease{}) {
			return nil // 并发竞态：他副本已建（PG pg_type 撞型形态），表在即放行
		}
		return fmt.Errorf("storage: leader_leases 引导失败: %w", err)
	}
	return nil
}

// autoMigrateAll 全表 AutoMigrate + MySQL LONGTEXT 升级（迁移租约内执行）。
func (s *Store) autoMigrateAll() error {
	if err := s.db.AutoMigrate(&Task{}, &FindingPrint{}, &PrintAlias{}, &ReviewTarget{}, &LeaderLease{}, &QASession{}, &PatternSignal{}, &SchemaMigration{}, &CommentDelivery{}, &RepoSetting{}, &OutboxEvent{}, &UsageEvent{}, &BudgetReservation{}); err != nil {
		return fmt.Errorf("storage: 自动迁移失败: %w", err)
	}
	if s.isMySQL() {
		if err := s.upgradeMySQLLongText(); err != nil {
			return err
		}
	}
	return nil
}

// migrateData 一次性数据回写迁移（P0-01，#2 fail-closed 化）：关键回写迁移
// 失败默认中止启动——半迁移态库继续服务会让存量行按旧形态被下游混读
// （alias_fp 全局主键 → PR 域复合主键切换期间），根因性失败（方言差异/权限
// 不足）也会陷入「启动重试-失败-告警」循环白烧全表扫描。显式 env 逃生门
// ARGUS_MIGRATION_FAIL_OPEN=1 降级回告警继续（可用性优先的显式选择，留痕告警、
// 下次启动重试）。生产库升级建议拆独立 Migration Job 执行（deploy/k8s43，
// 与 P3-04 部署规范联动）。
func (s *Store) migrateData(ctx context.Context) error {
	if err := s.runOneTimeMigration(ctx, "fingerprint_v2", "指纹 v2", s.migratePrintFingerprintsV2); err != nil {
		return err
	}
	if err := s.runOneTimeMigration(ctx, "print_alias_pr_scope", "print_aliases PR 域", s.migratePrintAliasPRScope); err != nil {
		return err
	}
	// #32 历史实销回填（启动期对账，刻意无哨兵 marker）：滚动发布期间旧二进制
	// 终态的任务、以及终态差额兜底漏网的任务，下次启动自愈；NOT EXISTS 让已有
	// 事件的任务零成本跳过，invocation_id 唯一键兜底幂等。失败语义沿 P0-01
	// fail-closed（ARGUS_MIGRATION_FAIL_OPEN=1 显式降级）——实销断档会让硬预算
	// 低估历史消费。
	n, err := s.ReconcileUsageEvents(ctx)
	if err != nil {
		if migrationFailOpen() {
			s.db.Logger.Warn(ctx, "历史实销回填失败（ARGUS_MIGRATION_FAIL_OPEN=1 fail-open，留待下次启动重试）: %v", err)
			return nil
		}
		return fmt.Errorf("storage: 历史实销回填失败（fail-closed；ARGUS_MIGRATION_FAIL_OPEN=1 可显式降级）: %w", err)
	}
	if n > 0 {
		s.db.Logger.Info(ctx, "历史实销回填完成: %d 行", n)
	}
	return nil
}

// runOneTimeMigration 单笔一次性迁移的哨兵执行 + fail-closed 语义收口
// （v3.7.0 起由 SchemaMigration 哨兵门控：成功一次后不再每次启动全表扫）。
func (s *Store) runOneTimeMigration(ctx context.Context, version, desc string, fn func(ctx context.Context) (int, error)) error {
	n, err := s.ensureMigrationApplied(ctx, version, fn)
	if err == nil {
		if n > 0 {
			s.db.Logger.Info(ctx, "%s迁移完成: %d 行", desc, n)
		}
		return nil
	}
	if migrationFailOpen() {
		s.db.Logger.Warn(ctx, "%s迁移失败（ARGUS_MIGRATION_FAIL_OPEN=1 fail-open，留待下次启动重试）: %v", desc, err)
		return nil
	}
	return fmt.Errorf("storage: %s迁移失败（fail-closed；确认可带旧形态数据服务时以 ARGUS_MIGRATION_FAIL_OPEN=1 显式降级）: %w", desc, err)
}

// migrationFailOpen 迁移失败逃生门（P0-01）：仅显式 env=1 才降级 warn-only，
// 缺省 fail-closed——生产静默 fail-open 是半迁移态带病服务的根。
func migrationFailOpen() bool {
	return os.Getenv("ARGUS_MIGRATION_FAIL_OPEN") == "1"
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
