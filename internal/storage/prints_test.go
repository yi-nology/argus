package storage

import (
	"context"
	"testing"
	"time"

	akseverity "git.example.com/enjoydream/agentkit/severity"

	"argus/internal/plugin"
)

// suppress → restore 全往返 + 负控（不存在前缀 / 前缀歧义）。
// restore 是 suppress 的反向操作：只把 suppressed 行置回 open，不误伤其他行。
func TestRestorePrint_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC()
	fp := "abcdef1234567890"
	f := plugin.Finding{File: "a.go", Line: 10, Severity: "warn", Comment: "问题甲"}

	// 首次 suppress 库中尚无该指纹：预插 suppressed，matched=false（文档语义）。
	matched, err := s.SuppressPrint(ctx, "gitea", "acme", "app", "12", fp, f, at)
	if err != nil || matched {
		t.Fatalf("首次 SuppressPrint 预插: matched=%v err=%v", matched, err)
	}
	st, err := s.GetPrintState(ctx, "gitea", "acme", "app", "12", fp)
	if err != nil || st == nil || st.Status != "suppressed" {
		t.Fatalf("suppress 后状态=%+v err=%v", st, err)
	}

	// 既有指纹再次 suppress：matched=true
	matched, err = s.SuppressPrint(ctx, "gitea", "acme", "app", "12", fp, f, at)
	if err != nil || !matched {
		t.Fatalf("二次 SuppressPrint: matched=%v err=%v", matched, err)
	}

	// 前缀命中 → 恢复 open
	matched, err = s.RestorePrint(ctx, "gitea", "acme", "app", 12, fp[:8])
	if err != nil || !matched {
		t.Fatalf("RestorePrint: matched=%v err=%v", matched, err)
	}
	st, err = s.GetPrintState(ctx, "gitea", "acme", "app", "12", fp)
	if err != nil || st == nil || st.Status != "open" {
		t.Fatalf("restore 后状态=%+v err=%v", st, err)
	}

	// 负控1：不存在的前缀不 matched
	if matched, _ := s.RestorePrint(ctx, "gitea", "acme", "app", 12, "deadbeef"); matched {
		t.Fatal("不存在的前缀不应 matched")
	}

	// 负控2：两条 suppressed 共享前缀 → 歧义不猜
	fp2 := fp[:8] + "99999999"
	if _, err := s.SuppressPrint(ctx, "gitea", "acme", "app", "12", fp2, f, at); err != nil {
		t.Fatal(err)
	}
	if matched, _ := s.RestorePrint(ctx, "gitea", "acme", "app", 12, fp[:8]); matched {
		t.Fatal("前缀歧义不应 matched")
	}
}

// ---- v3.7.0 指纹 v2 + 近重复匹配层 ----

// 措辞漂移：第二轮措辞微调（精确未命中）→ 近重复并入既有条目（TimesSeen 延续、
// 规范指纹不变、别名落库）；第三轮同一新措辞 → 别名精确命中。
func TestUpsertPrint_NearDuplicateWordingMerges(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	v2a := plugin.Fingerprint("a.go", "这里存在SQL注入风险", 10)

	// 第一轮：原始措辞
	if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "7", v2a,
		plugin.Finding{File: "a.go", Line: 10, Severity: "high", Comment: "这里存在SQL注入风险", By: "security"},
		"t1", at); err != nil {
		t.Fatal(err)
	}
	// 第二轮：措辞漂移（精确未命中 → 近重复并入）
	drifted := plugin.Fingerprint("a.go", "拼接 SQL 存在注入风险！", 11)
	suppressed, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "7", drifted,
		plugin.Finding{File: "a.go", Line: 11, Severity: "high", Comment: "拼接 SQL 存在注入风险！", By: "security"},
		"t2", at)
	if err != nil || suppressed {
		t.Fatalf("近重复应并入既有条目: suppressed=%v err=%v", suppressed, err)
	}
	st, err := s.GetPrintState(ctx, "gitea", "acme", "app", "7", v2a)
	if err != nil || st == nil || st.TimesSeen != 2 {
		t.Fatalf("既有条目计数应延续（TimesSeen=2），got %+v err=%v", st, err)
	}
	// 第三轮：漂移措辞经别名精确命中
	if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "7", drifted,
		plugin.Finding{File: "a.go", Line: 11, Severity: "high", Comment: "拼接 SQL 存在注入风险！", By: "security"},
		"t3", at); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.GetPrintState(ctx, "gitea", "acme", "app", "7", v2a); st == nil || st.TimesSeen != 3 {
		t.Fatalf("别名精确命中应继续累加（TimesSeen=3），got %+v", st)
	}
}

// 抑制经近重复延续：条目被抑制后，措辞漂移的复现同样被剔除（不复活）。
func TestUpsertPrint_SuppressedNearDuplicateStillDropped(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	fp := plugin.Fingerprint("a.go", "硬编码密钥", 20)
	if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "7", fp,
		plugin.Finding{File: "a.go", Line: 20, Severity: "high", Comment: "硬编码密钥"}, "t1", at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SuppressPrint(ctx, "gitea", "acme", "app", "7", fp,
		plugin.Finding{File: "a.go", Line: 20, Comment: "硬编码密钥"}, at); err != nil {
		t.Fatal(err)
	}
	drifted := plugin.Fingerprint("a.go", "密钥硬编码在源码中", 21)
	suppressed, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "7", drifted,
		plugin.Finding{File: "a.go", Line: 21, Severity: "high", Comment: "密钥硬编码在源码中"}, "t2", at)
	if err != nil || !suppressed {
		t.Fatalf("抑制条目的措辞漂移复现应保持剔除: suppressed=%v err=%v", suppressed, err)
	}
}

// SuppressPrintNear：file+原文 形态经近重复命中既有条目（API 无行号场景）。
func TestSuppressPrintNear_MatchesExisting(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "7", plugin.Fingerprint("a.go", "goroutine 泄漏未处理", 30),
		plugin.Finding{File: "a.go", Line: 30, Severity: "medium", Comment: "goroutine 泄漏未处理"}, "t1", at); err != nil {
		t.Fatal(err)
	}
	fp, matched, err := s.SuppressPrintNear(ctx, "gitea", "acme", "app", "7", "a.go", "goroutine 泄漏未处理", at)
	if err != nil || !matched || fp == "" {
		t.Fatalf("近重复抑制应命中: matched=%v fp=%q err=%v", matched, fp, err)
	}
}

// v3.8.0 回归：suppress 能经别名进来，restore 也必须能经别名出去——
// 抑制不得是"进得来、出不去"的单向门。
func TestRestorePrint_AliasFallback(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	orig := "goroutine 泄漏未处理"
	if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "9",
		plugin.Fingerprint("a.go", orig, 30),
		plugin.Finding{File: "a.go", Line: 30, Severity: "medium", Comment: orig}, "t1", at); err != nil {
		t.Fatal(err)
	}
	// 措辞漂移：新措辞并入既有条目，产生别名
	drifted := "goroutine 泄漏了却没人处理它"
	aliasFP := plugin.Fingerprint("a.go", drifted, 31)
	if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "9", aliasFP,
		plugin.Finding{File: "a.go", Line: 31, Severity: "medium", Comment: drifted}, "t2", at); err != nil {
		t.Fatal(err)
	}
	// 经别名 suppress（用别名短码直达）
	rec, ok := s.ResolvePrintRecord(ctx, "gitea", "acme", "app", "9", aliasFP[:8])
	if !ok || rec.Print == aliasFP {
		t.Fatalf("别名短码应解析回规范行: ok=%v print=%q", ok, rec.Print)
	}
	if rec.Comment != orig {
		t.Fatalf("ResolvePrintRecord 应回原始评语: got %q want %q", rec.Comment, orig)
	}
	if _, err := s.SuppressPrint(ctx, "gitea", "acme", "app", "9", rec.Print,
		plugin.Finding{File: rec.File, Comment: rec.Comment}, at); err != nil {
		t.Fatal(err)
	}
	// 用别名短码 restore（此前 RestorePrint 无别名回退，此处 matched=false 单向门）
	matched, err := s.RestorePrint(ctx, "gitea", "acme", "app", 9, aliasFP[:8])
	if err != nil || !matched {
		t.Fatalf("别名短码 restore 应命中: matched=%v err=%v", matched, err)
	}
	st, err := s.GetPrintState(ctx, "gitea", "acme", "app", "9", rec.Print)
	if err != nil || st == nil || st.Status != "open" {
		t.Fatalf("restore 后状态=%+v err=%v", st, err)
	}
}

// 存量迁移：v1 格式行按 file/line/comment 重算为 v2（幂等；第二次运行为零）。
func TestMigratePrintFingerprintsV2_RekeysLegacyRows(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	v1 := akseverity.Fingerprint("a.go", "存量误报意见") // 旧格式（无行号、无版本域）
	now := time.Now()
	if err := s.db.Create(&FindingPrint{
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "7", Print: v1,
		File: "a.go", Line: 42, Severity: "medium", Comment: "存量误报意见",
		Status: PrintSuppressed, FirstTaskID: "old", LastTaskID: "old",
		TimesSeen: 4, RegretCount: 2, FirstSeenAt: now, LastSeenAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	n, err := s.migratePrintFingerprintsV2(ctx)
	if err != nil || n != 1 {
		t.Fatalf("应迁移 1 行，got n=%d err=%v", n, err)
	}
	var row FindingPrint
	if err := s.db.Where("platform=? AND owner=? AND repo=? AND number=?", "gitea", "acme", "app", "7").
		First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if want := plugin.Fingerprint("a.go", "存量误报意见", 42); row.Print != want {
		t.Fatalf("应重算为 v2 指纹: got %s want %s", row.Print, want)
	}
	// 抑制状态与计数原地保留
	if row.Status != PrintSuppressed || row.TimesSeen != 4 || row.RegretCount != 2 {
		t.Fatalf("迁移不得丢状态/计数: %+v", row)
	}
	// 幂等：重跑为零
	if n, err := s.migratePrintFingerprintsV2(ctx); err != nil || n != 0 {
		t.Fatalf("迁移应幂等，got n=%d err=%v", n, err)
	}
}

// v3.8.0 回归：v1 时代 regret 幽灵行（空 file/comment，Print 为各自改写后身份）
// 重算 v2 后同指纹——迁移必须合并而不是撞唯一索引死循环。
func TestMigratePrintFingerprintsV2_CollisionMerges(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now()
	// 两条空内容行（模拟 v1 时代 RecordPrintRegret 幽灵行），Print 是任意互异的
	// 旧格式值——重算后都等于 Fingerprint("","",0) 同一常量。
	for _, old := range []string{"aaaa1111aaaa1111aaaa1111aaaa1111", "bbbb2222bbbb2222bbbb2222bbbb2222"} {
		if err := s.db.Create(&FindingPrint{
			Platform: "gitea", Owner: "acme", Repo: "app", Number: "8", Print: old,
			Status: PrintOpen, RegretCount: 1,
			FirstTaskID: "reflect", LastTaskID: "reflect",
			FirstSeenAt: now, LastSeenAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.migratePrintFingerprintsV2(ctx)
	if err != nil {
		t.Fatalf("碰撞组应合并成功而非唯一索引冲突: %v", err)
	}
	if n != 2 { // 计数口径：合并组删除 1 行 + 主行 print 回写 1 次
		t.Fatalf("应合并掉 1 行并回写主行（n=2），got n=%d", n)
	}
	var rows []FindingPrint
	if err := s.db.Where("platform=? AND owner=? AND repo=? AND number=?", "gitea", "acme", "app", "8").
		Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("应只剩 1 行，got %d", len(rows))
	}
	if rows[0].Print != plugin.Fingerprint("", "", 0) || rows[0].RegretCount != 1 {
		t.Fatalf("合并行指纹/regret 应保留: %+v", rows[0])
	}
}

// v3.8.0 回归：同 PR 推一版代码使行号整体位移 >±2 时，完全同评语的意见
// 仍并入既有条目（TimesSeen 延续、抑制不复活、行号跟随新值）——±2 近重复
// 窗口救不回的"行号漂移"由 exact 通道承接。
func TestUpsertPrint_LineDriftBeyondWindow_ExactCommentAbsorbed(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "10",
		plugin.Fingerprint("a.go", "资源释放缺失", 30),
		plugin.Finding{File: "a.go", Line: 30, Severity: "medium", Comment: "资源释放缺失"}, "t1", at); err != nil {
		t.Fatal(err)
	}
	// 抑制后行号大幅位移（30 → 40，>±2），同评语复现
	suppressed, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "10",
		plugin.Fingerprint("a.go", "资源释放缺失", 40),
		plugin.Finding{File: "a.go", Line: 40, Severity: "medium", Comment: "资源释放缺失"}, "t2", at)
	_ = suppressed
	if err != nil {
		t.Fatal(err)
	}
	var rows []FindingPrint
	if err := s.db.Where("platform=? AND owner=? AND repo=? AND number=?", "gitea", "acme", "app", "10").
		Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("行号漂移的完全同评语应并入既有行，got %d 行", len(rows))
	}
	if rows[0].TimesSeen != 2 || rows[0].Line != 40 {
		t.Fatalf("计数应延续、行号应跟随: %+v", rows[0])
	}
}

// v3.8.0 回归：SuppressPrintNear 未命中时预插的 line=0 suppressed 行，必须能被
// 后续真实意见（任意行号）经 exact 通道并回——"先抑制后见到"才真正生效。
func TestSuppressPrintNear_PreinsertRecoveredByExactComment(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	if _, matched, err := s.SuppressPrintNear(ctx, "gitea", "acme", "app", "11", "b.go", "并发写 map 未加锁", at); err != nil || matched {
		t.Fatalf("预插路径 matched 应为 false: %v %v", matched, err)
	}
	suppressed, err := s.UpsertPrint(ctx, "gitea", "acme", "app", "11",
		plugin.Fingerprint("b.go", "并发写 map 未加锁", 88),
		plugin.Finding{File: "b.go", Line: 88, Severity: "high", Comment: "并发写 map 未加锁"}, "t2", at)
	if err != nil {
		t.Fatal(err)
	}
	if !suppressed {
		t.Fatal("真实意见应并回预插的 suppressed 行（抑制生效）")
	}
	var rows []FindingPrint
	if err := s.db.Where("platform=? AND owner=? AND repo=? AND number=?", "gitea", "acme", "app", "11").
		Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Line != 88 || rows[0].Status != PrintSuppressed {
		t.Fatalf("应并回单行且行号跟随: %+v", rows)
	}
}

// v3.9.1 回归：alias_fp 全局唯一主键下，两个 PR 出现同款措辞漂移（同一新指纹）
// 时，第二个 PR 重复创建别名此前会撞主键、UpsertPrint 整体回滚（该意见此后
// 每轮 print_upsert_failed，抑制/计数/复现全部失效）。别名写入现已幂等。
func TestUpsertPrint_SameDriftAcrossPRsNoAliasConflict(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	base := "这里存在SQL注入风险"
	drift := "拼接 SQL 存在注入风险！"
	baseFP := plugin.Fingerprint("a.go", base, 10)
	driftFP := plugin.Fingerprint("a.go", drift, 11)

	for _, number := range []string{"7", "8"} {
		if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", number, baseFP,
			plugin.Finding{File: "a.go", Line: 10, Severity: "high", Comment: base, By: "security"},
			"t-base-"+number, at); err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", number, driftFP,
			plugin.Finding{File: "a.go", Line: 11, Severity: "high", Comment: drift, By: "security"},
			"t-drift-"+number, at); err != nil {
			t.Fatalf("PR %s 漂移并入不得因别名主键冲突失败: %v", number, err)
		}
		if st, err := s.GetPrintState(ctx, "gitea", "acme", "app", number, baseFP); err != nil || st == nil || st.TimesSeen != 2 {
			t.Fatalf("PR %s 既有条目计数应延续（TimesSeen=2），got %+v err=%v", number, st, err)
		}
		// v3.9.2：别名 PR 域化后，两个 PR 各自的漂移指纹都应解析到本 PR 的规范行
		if st, err := s.GetPrintState(ctx, "gitea", "acme", "app", number, driftFP); err != nil || st == nil || st.TimesSeen != 2 {
			t.Fatalf("PR %s 漂移指纹应经本 PR 别名解析到规范行，got %+v err=%v", number, st, err)
		}
	}
}

// v3.9.2 迁移回填：旧形态（alias_fp 单列主键、无 PR 列）→ PR 域复合主键。
// 旧行按 print_fp 反查 PR——多 PR 命中逐 PR 复制，孤儿（规范行已清理）丢弃。
func TestMigratePrintAliasPRScope(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Open() 已按新形态建表并跑过迁移（空表、marker 已写）；重建为旧形态造存量
	if err := s.db.Exec(`DROP TABLE print_aliases`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec(`CREATE TABLE print_aliases (alias_fp varchar(64) PRIMARY KEY, print_fp varchar(64), updated_at datetime)`).Error; err != nil {
		t.Fatal(err)
	}
	// 规范行：同指纹 Fx 在 PR 7 与 PR 8 各一条；孤儿别名指向的 Gx 不存在
	fx, gx, a1, a2 := "fx0000000001", "gx0000000001", "a10000000001", "a20000000002"
	for _, number := range []string{"7", "8"} {
		if err := s.db.Create(&FindingPrint{Platform: "gitea", Owner: "acme", Repo: "app",
			Number: number, Print: fx, File: "a.go", Line: 1, Severity: "high",
			Comment: "注入风险", Status: PrintOpen}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.Exec(`INSERT INTO print_aliases (alias_fp, print_fp, updated_at) VALUES ('` + a1 + `', '` + fx + `', CURRENT_TIMESTAMP), ('` + a2 + `', '` + gx + `', CURRENT_TIMESTAMP)`).Error; err != nil {
		t.Fatal(err)
	}

	n, err := s.migratePrintAliasPRScope(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("迁移后应剩 2 行（A1 复制到两 PR、孤儿 A2 丢弃），got %d", n)
	}
	// 各 PR 的 A1 都解析到本 PR 规范行（PrintState 不含指纹字段，以行内容佐证）
	for _, number := range []string{"7", "8"} {
		if st, err := s.GetPrintState(ctx, "gitea", "acme", "app", number, a1); err != nil || st == nil || st.Comment != "注入风险" {
			t.Fatalf("PR %s 应经别名解析到 Fx 行，got %+v err=%v", number, st, err)
		}
	}
}
