package runner

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oekit/logx"

	"argus/internal/plugin"
	"argus/internal/storage"
)

// v3.52.0 跨轮一致性：prevFindingsSection 只取 open 且有 file/comment 的指纹，
// 渲染为 file:line [SEVERITY] 评语清单；suppressed 与空历史不出现。
func TestPrevFindingsSection(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	at := time.Now().UTC()
	pr := &plugin.PRContext{Platform: "gitlab", Owner: "o", Repo: "r", Number: "1"}
	f := func(comment string) plugin.Finding {
		return plugin.Finding{File: "src/a.go", Line: 10, Severity: "medium", Comment: comment}
	}
	for i, c := range []string{"意见一", "意见二"} {
		fp := "fp000" + string(rune('a'+i)) + "bbbb"
		if _, err := store.UpsertPrint(ctx, "gitlab", "o", "r", "1", fp, f(c), "t1", at); err != nil {
			t.Fatal(err)
		}
	}
	// 被抑制意见放不同文件（src/b.go）：v3.133.0 起 suppress 家族传播一键全压，
	// 同文件同行号的近重复措辞会被连带抑制，夹具必须与之解耦才能保住「open 保留」断言。
	sup := plugin.Finding{File: "src/b.go", Line: 20, Severity: "low", Comment: "已抑制意见"}
	if _, err := store.UpsertPrint(ctx, "gitlab", "o", "r", "1", "fp0009zzzz", sup, "t1", at); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SuppressPrint(ctx, "gitlab", "o", "r", "1", "fp0009zzzz", sup, at); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Store: store, Log: logx.Nop()}
	out := r.prevFindingsSection(ctx, pr)
	if !strings.Contains(out, "src/a.go:10 [MEDIUM] 意见一") || !strings.Contains(out, "意见二") {
		t.Fatalf("应含两条 open 意见: %q", out)
	}
	if strings.Contains(out, "已抑制") {
		t.Fatalf("suppressed 不应出现: %q", out)
	}
	if out := r.prevFindingsSection(ctx, &plugin.PRContext{Platform: "gitlab", Owner: "o", Repo: "r", Number: "2"}); out != "" {
		t.Fatalf("无历史应返回空: %q", out)
	}
}
