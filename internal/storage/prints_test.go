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
