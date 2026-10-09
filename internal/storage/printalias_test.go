package storage

import (
	"context"
	"testing"
	"time"

	"argus/internal/domain"
)

// 真实案例夹具（2026-10-09，argus-validate#2 db.go:9 硬编码密钥）：人工抑制后
// 模型重措辞复现——两版评语共享代码片段但叙述重写，严格级(0.6)失守实锤。
var (
	fixOldComment      = "硬编码疑似线上密钥：const fallbackAPIKey = \"sk-live-7f3a9c21d4e5fallback\" 以 sk-live- 前缀明文写在源码中，一旦仓库/构建产物/镜像外泄即等同于凭据泄漏，且无法轮换（改密钥需改代码发版）。fallback 语义也应通过配置注入：从环境变量或密钥管理服务读取，源码只保留变量名，并确认该密钥已从版本历史中撤销/轮换。"
	fixNewComment      = "新增了硬编码的生产密钥 fallbackAPIKey = \"sk-live-7f3a9c21d4e5fallback\"。前缀 sk-live 表明这是生产环境凭据，一旦随源码进入仓库（含历史提交、CI 日志、镜像层、fork），任何有仓库读权限的人都能直接使用；且密钥一旦提交就应视为已泄漏，仅删除代码行并不能收回。修复：立即在服务端轮换/吊销该密钥，从代码中删除该常量，改为运行时从环境变量或密钥管理服务读取。"
	fixDistinctComment = "Divide 未做零除保护：b == 0 时触发 runtime panic，调用方无法恢复。"
)

// TestCommentNear_Tiering 三级阈值带钉死：真实重措辞对落 (0.4, 0.6) 宽松带，
// 同位置不同问题两带皆不入，措辞微调过严格带。
func TestCommentNear_Tiering(t *testing.T) {
	if commentNear(fixOldComment, fixNewComment) {
		t.Error("真实重措辞对不应过 0.6 严格带（实测失守是本案根因）")
	}
	if !commentNearLoose(fixOldComment, fixNewComment) {
		t.Error("真实重措辞对必须被 0.4 宽松带吸收")
	}
	if commentNear(fixOldComment, fixDistinctComment) || commentNearLoose(fixOldComment, fixDistinctComment) {
		t.Error("同位置不同问题两带皆不得命中（宽松级只对 suppressed 行开放的前提）")
	}
	minor := "硬编码疑似线上密钥：const fallbackAPIKey 明文写在源码中，一旦仓库外泄即等同凭据泄漏，且无法轮换（改密钥需改代码发版）。fallback 语义应通过配置注入：从环境变量或密钥管理服务读取，源码只保留变量名，并确认该密钥已从版本历史中撤销/轮换。"
	if !commentNear(fixOldComment, minor) {
		t.Error("措辞微调应过 0.6 严格带")
	}
}

// TestUpsertPrints_SuppressedLooseNoResurrect 端到端（storage 层）：人工抑制后
// 重措辞复现并入 suppressed 行（不复活）；同文件不同问题照常成新行。
func TestUpsertPrints_SuppressedLooseNoResurrect(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	pr := func(f domain.Finding, fp string) []domain.PrintUpsertResult {
		return s.UpsertPrints(ctx, "gitlab", "acme", "app", "2", "t-1", at,
			[]domain.PrintUpsert{{FP: fp, Finding: f}})
	}
	oldF := domain.Finding{File: "db.go", Line: 9, Severity: "high", Comment: fixOldComment, By: "security"}
	r0 := pr(oldF, domain.Fingerprint("db.go", fixOldComment, 9))
	if r0[0].Err != nil {
		t.Fatal(r0[0].Err)
	}
	// 人工抑制（file+原文 形态，近重复命中既有行）
	if _, matched, err := s.SuppressPrintNear(ctx, "gitlab", "acme", "app", "2", "db.go", fixOldComment, at); err != nil || !matched {
		t.Fatalf("抑制应命中既有行: matched=%v err=%v", matched, err)
	}
	// 重措辞复现（同文件同 line±2）→ 并入 suppressed 行，Suppressed=true
	newF := domain.Finding{File: "db.go", Line: 9, Severity: "high", Comment: fixNewComment, By: "security"}
	r1 := pr(newF, domain.Fingerprint("db.go", fixNewComment, 9))
	if r1[0].Err != nil {
		t.Fatal(r1[0].Err)
	}
	if !r1[0].Suppressed {
		t.Error("重措辞复现应延续抑制（宽松近重复级），不得复活")
	}
	// 同文件不同问题（行号近）→ 不受宽松级牵连，照常 open
	distF := domain.Finding{File: "db.go", Line: 10, Severity: "medium", Comment: fixDistinctComment, By: "security"}
	r2 := pr(distF, domain.Fingerprint("db.go", fixDistinctComment, 10))
	if r2[0].Err != nil {
		t.Fatal(r2[0].Err)
	}
	if r2[0].Suppressed {
		t.Error("同位置不同问题不得被宽松级误吞")
	}
	// 别名已记：重措辞指纹二次入库走精确通道仍 suppressed
	r3 := pr(newF, domain.Fingerprint("db.go", fixNewComment, 9))
	if !r3[0].Suppressed {
		t.Error("别名精确命中应持续 suppressed")
	}
}

// 形状漂移夹具（2026-10-09 实测逃逸案例 t-623c3695）：多文件聚合 + line=0 +
// 彻底重述 + 多问题拼接。
var (
	fixBlobComment   = "【敏感信息泄露】硬编码的 API Key 直接写入源码（sk-live-...）会被提交进版本库，任何能读取仓库的人都能拿到，且该常量当前并未被使用（疑似占位符…\n\n[performance] 【敏感信息泄露】硬编码的 API Key 直接写入源码（sk-live-...）会被提交进版本库，任何能读取仓库的人都能拿到，且该常量当前并未被使用（疑似占位符/遗忘的调试密钥）。建议从环境变量或密钥管理服务读取并删除此常量；如该密钥真实有效，应立即轮换。\n\n[performance] 【SQL 注入】使用字符串拼接将外部传入的 id 直接拼进 SQL 语句：\"SELECT name FROM users WHERE id = \" + id��..."
	fixSQLSegComment = "[performance] 【SQL 注入】使用字符串拼接将外部传入的 id 直接拼进 SQL 语句：\"SELECT name FROM users WHERE id = \" + id��..."
	fixSameFnA       = "GetUserByName 返回值错误：err 未检查，失败时返回空串，调用方拿到假数据"
	fixSameFnB       = "GetUserByName 空指针：未判 nil 直接解引用，nil 用户时 panic"
)

// TestUpsertPrints_VagueBlobAbsorbed 端到端：人工抑制后，多文件聚合/彻底重述的
// 模糊意见（line=0）不得复活——vague 吸收层（0.2 + 文件组件相交 + 仅 suppressed）
// 正是为该实测案例加的（v3.133.0）。
func TestUpsertPrints_VagueBlobAbsorbed(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	up := func(f domain.Finding) domain.PrintUpsertResult {
		return s.UpsertPrints(ctx, "gitlab", "acme", "app", "2", "t-1", at,
			[]domain.PrintUpsert{{FP: domain.Fingerprint(f.File, f.Comment, f.Line), Finding: f}})[0]
	}
	oldF := domain.Finding{File: "db.go", Line: 9, Severity: "high", Comment: fixOldComment, By: "security"}
	if r := up(oldF); r.Err != nil {
		t.Fatal(r.Err)
	}
	if _, matched, err := s.SuppressPrintNear(ctx, "gitlab", "acme", "app", "2", "db.go", fixOldComment, at); err != nil || !matched {
		t.Fatalf("抑制应命中: %v %v", matched, err)
	}
	// 逃逸案例本体：多文件、行号 0、彻底重述
	blobF := domain.Finding{File: "db.go,util.go", Line: 0, Severity: "medium", Comment: fixBlobComment, By: "security"}
	if r := up(blobF); r.Err != nil {
		t.Fatal(r.Err)
	} else if !r.Suppressed {
		t.Error("模糊聚合意见应被 vague 吸收层吸收（实测案例不得复活）")
	}
}

// TestUpsertPrints_VagueTierNotOverbroad vague 层负控：同行号区别的不同问题
// （精确意见）与话题不同的模糊意见都不许被吸收。
func TestUpsertPrints_VagueTierNotOverbroad(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	up := func(f domain.Finding) domain.PrintUpsertResult {
		return s.UpsertPrints(ctx, "gitlab", "acme", "app", "2", "t-1", at,
			[]domain.PrintUpsert{{FP: domain.Fingerprint(f.File, f.Comment, f.Line), Finding: f}})[0]
	}
	oldF := domain.Finding{File: "db.go", Line: 9, Severity: "high", Comment: fixOldComment, By: "security"}
	up(oldF)
	if _, matched, err := s.SuppressPrintNear(ctx, "gitlab", "acme", "app", "2", "db.go", fixOldComment, at); err != nil || !matched {
		t.Fatalf("抑制应命中: %v %v", matched, err)
	}
	// 负例1：精确意见（line=10）同函数不同问题——0.6/0.4 两级都不收
	sameFn := domain.Finding{File: "db.go", Line: 10, Severity: "medium", Comment: fixSameFnA, By: "security"}
	if r := up(sameFn); r.Err != nil {
		t.Fatal(r.Err)
	} else if r.Suppressed {
		t.Error("精确意见的不同问题不得被吸收（同函数也不行）")
	}
	// 负例2：模糊意见（line=0）但话题不同（SQL 注入 vs 硬编码密钥）
	sqlF := domain.Finding{File: "db.go,util.go", Line: 0, Severity: "medium", Comment: fixSQLSegComment, By: "security"}
	if r := up(sqlF); r.Err != nil {
		t.Fatal(r.Err)
	} else if r.Suppressed {
		t.Error("话题不同的模糊意见不得被吸收")
	}
	// 负例3：同函数不同问题的模糊形态也不得命中 0.4 严格侧（度量钉死）
	if commentNearLoose(fixSameFnA, fixSameFnB) {
		t.Error("同函数不同问题不该过 0.4（实测 0.364）")
	}
}

// TestSuppressFamily_OneShot 一键抑制 = 压掉全家族（v3.133.1）：实测同一问题
// 四轮四种措辞（旧/新/大杂烩/再重述），逐变体打地鼠压二缺一即复活——家族传播
// 后从任一变体进入即可全覆盖。
func TestSuppressFamily_OneShot(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	up := func(f domain.Finding) domain.PrintUpsertResult {
		return s.UpsertPrints(ctx, "gitlab", "acme", "app", "2", "t-1", at,
			[]domain.PrintUpsert{{FP: domain.Fingerprint(f.File, f.Comment, f.Line), Finding: f}})[0]
	}
	// 三个变体行：旧措辞（line 9）/新措辞（line 9）/大杂烩（line 0）
	up(domain.Finding{File: "db.go", Line: 9, Severity: "high", Comment: fixOldComment, By: "s"})
	up(domain.Finding{File: "db.go", Line: 9, Severity: "high", Comment: fixNewComment, By: "s"})
	up(domain.Finding{File: "db.go,util.go", Line: 0, Severity: "medium", Comment: fixBlobComment, By: "s"})
	// 只压一个（旧措辞）——家族传播应带出其余
	if _, matched, err := s.SuppressPrintNear(ctx, "gitlab", "acme", "app", "2", "db.go", fixOldComment, at); err != nil || !matched {
		t.Fatalf("抑制应命中: %v %v", matched, err)
	}
	for _, f := range []domain.Finding{
		{File: "db.go", Line: 9, Comment: fixNewComment},
		{File: "db.go,util.go", Line: 0, Comment: fixBlobComment},
	} {
		r := up(f)
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		if !r.Suppressed {
			t.Errorf("家族变体应随一键抑制被覆盖: %s:%d", f.File, f.Line)
		}
	}
	// 恢复对称：恢复一条 → 家族放回 open
	if matched, err := s.RestorePrint(ctx, "gitlab", "acme", "app", 2, domain.Fingerprint("db.go", fixOldComment, 9)); err != nil || !matched {
		t.Fatalf("restore 应命中: %v %v", matched, err)
	}
	if r := up(domain.Finding{File: "db.go", Line: 9, Comment: fixNewComment}); r.Suppressed {
		t.Error("恢复后家族变体应重新可见（suppress/restore 对称）")
	}
}

// TestSuppressFamily_SameFnNotPropagated 家族传播负控：同函数不同问题不传播
// （0.364 < 0.4 精确侧），换言之人工拒绝"返回值错误"不许顺手杀掉"空指针"。
func TestSuppressFamily_SameFnNotPropagated(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	up := func(f domain.Finding) domain.PrintUpsertResult {
		return s.UpsertPrints(ctx, "gitlab", "acme", "app", "2", "t-1", at,
			[]domain.PrintUpsert{{FP: domain.Fingerprint(f.File, f.Comment, f.Line), Finding: f}})[0]
	}
	up(domain.Finding{File: "db.go", Line: 10, Severity: "medium", Comment: fixSameFnA, By: "s"})
	up(domain.Finding{File: "db.go", Line: 12, Severity: "medium", Comment: fixSameFnB, By: "s"})
	if _, matched, err := s.SuppressPrintNear(ctx, "gitlab", "acme", "app", "2", "db.go", fixSameFnA, at); err != nil || !matched {
		t.Fatalf("抑制应命中: %v %v", matched, err)
	}
	if r := up(domain.Finding{File: "db.go", Line: 12, Comment: fixSameFnB}); r.Suppressed {
		t.Error("同函数不同问题不得被家族传播连坐")
	}
}

// TestSuppressFamily_BlobBridgeBlocked 大杂烩桥接负控（v3.133.2 活体对照抓的
// 过度抑制）：blob 行同时含硬编码+SQL 两段文本，无向传播会把 SQL 真意见连坐进
// 家族——方向化后 line=0 模糊锚不得向精确行传播，SQL 行必须活着。
func TestSuppressFamily_BlobBridgeBlocked(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	up := func(f domain.Finding) domain.PrintUpsertResult {
		return s.UpsertPrints(ctx, "gitlab", "acme", "app", "2", "t-1", at,
			[]domain.PrintUpsert{{FP: domain.Fingerprint(f.File, f.Comment, f.Line), Finding: f}})[0]
	}
	up(domain.Finding{File: "db.go", Line: 9, Severity: "high", Comment: fixOldComment, By: "s"})
	up(domain.Finding{File: "db.go,util.go", Line: 0, Severity: "medium", Comment: fixBlobComment, By: "s"})
	// SQL 精确意见（同文件近行号）——绝不许被连坐
	sqlF := domain.Finding{File: "db.go", Line: 12, Severity: "high", Comment: fixSQLSegComment, By: "s"}
	up(sqlF)
	if _, matched, err := s.SuppressPrintNear(ctx, "gitlab", "acme", "app", "2", "db.go", fixOldComment, at); err != nil || !matched {
		t.Fatalf("抑制应命中: %v %v", matched, err)
	}
	// blob 应随家族被吸收（作为目标）
	if r := up(domain.Finding{File: "db.go,util.go", Line: 0, Comment: fixBlobComment}); !r.Suppressed {
		t.Error("blob 应随家族被吸收")
	}
	// SQL 精确意见必须活着（大杂烩桥不外溢）
	if r := up(sqlF); r.Suppressed {
		t.Error("SQL 真意见不得被大杂烩桥连坐")
	}
}

// TestSearchFindingsHistory 历史意见账本检索（v3.138.0）：关键词命中/状态与
// 复现次数带出/limit 钳制/空 query 全量倒序。
func TestSearchFindingsHistory(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()
	up := func(f domain.Finding) {
		if r := s.UpsertPrints(ctx, "gitlab", "acme", "app", "2", "t-1", at,
			[]domain.PrintUpsert{{FP: domain.Fingerprint(f.File, f.Comment, f.Line), Finding: f}}); r[0].Err != nil {
			t.Fatal(r[0].Err)
		}
	}
	up(domain.Finding{File: "db.go", Line: 9, Severity: "high", Comment: "硬编码疑似线上密钥 fallbackAPIKey", By: "s"})
	up(domain.Finding{File: "db.go", Line: 12, Severity: "high", Comment: "SQL 注入：id 直接拼接", By: "s"})
	// 同条再 upsert 一次 → TimesSeen=2
	up(domain.Finding{File: "db.go", Line: 9, Severity: "high", Comment: "硬编码疑似线上密钥 fallbackAPIKey", By: "s"})

	items, err := s.SearchFindingsHistory(ctx, "gitlab", "acme", "app", "硬编码", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("关键词应命中 1 条, got %d", len(items))
	}
	if items[0].TimesSeen != 2 || items[0].Status != "open" || items[0].FP == "" {
		t.Errorf("账本字段不符: %+v", items[0])
	}
	// 抑制后 status 可见
	if _, _, err := s.SuppressPrintNear(ctx, "gitlab", "acme", "app", "2", "db.go", "硬编码疑似线上密钥 fallbackAPIKey", at); err != nil {
		t.Fatal(err)
	}
	items, _ = s.SearchFindingsHistory(ctx, "gitlab", "acme", "app", "密钥", 10)
	if len(items) != 1 || items[0].Status != "suppressed" {
		t.Errorf("抑制状态应可见: %+v", items)
	}
	// 空 query = 最近流水；limit 钳制
	all, err := s.SearchFindingsHistory(ctx, "gitlab", "acme", "app", "", 0)
	if err != nil || len(all) != 2 {
		t.Errorf("空 query 应返回全部 2 条: %d %v", len(all), err)
	}
	one, _ := s.SearchFindingsHistory(ctx, "gitlab", "acme", "app", "", 1)
	if len(one) != 1 {
		t.Errorf("limit 应生效: %d", len(one))
	}
	// 他仓隔离
	other, _ := s.SearchFindingsHistory(ctx, "gitlab", "acme", "other", "", 10)
	if len(other) != 0 {
		t.Error("跨仓不得串数据")
	}
}
