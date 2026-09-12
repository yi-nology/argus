package storage

import (
	"context"
	"testing"
	"time"

	"git.n.xiaomi.com/argus/argus/internal/plugin"
)

// suppress → restore 全往返 + 负控（不存在前缀 / 前缀歧义）。
// restore 是 suppress 的反向操作：只把 suppressed 行置回 open，不误伤其他行。
func TestRestorePrint_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC()
	fp := "abcdef1234567890"
	f := plugin.Finding{File: "a.go", Line: 10, Severity: "warn", Comment: "问题甲"}

	matched, err := s.SuppressPrint(ctx, "gitea", "acme", "app", "12", fp, f, at)
	if err != nil || !matched {
		t.Fatalf("SuppressPrint: matched=%v err=%v", matched, err)
	}
	st, err := s.GetPrintState(ctx, "gitea", "acme", "app", "12", fp)
	if err != nil || st == nil || st.Status != "suppressed" {
		t.Fatalf("suppress 后状态=%+v err=%v", st, err)
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
