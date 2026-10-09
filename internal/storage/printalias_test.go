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
