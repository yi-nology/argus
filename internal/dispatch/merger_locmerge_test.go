package dispatch

import (
	"context"
	"strings"
	"testing"

	"time"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
)

// 位置合并兜底：不同专家对同一 file:line 用不同措辞描述同一问题时（指纹必不同），
// 应归并为一条——severity 取高、by 并列、意见拼接保留双方视角。
// 场景原型：PR#11 实测 correctness(HIGH) 与 security(MEDIUM) 各报一条相同的
// os.Args[1][:10] 切片越界，旧逻辑按 file+comment 精确指纹去重拦不住。
func TestMergeGroupsSameLocationAcrossPlugins(t *testing.T) {
	m := NewMerger(nil, logx.Nop())
	mf, err := m.Merge(context.Background(), regretTestPR(), "t1",
		[]Outcome{
			{Name: "correctness", Result: &plugin.Result{Findings: []domain.Finding{
				{File: "main.go", Line: 19, Severity: plugin.SeverityHigh,
					Comment: "os.Args[1][:10] 切片越界 panic"},
			}}},
			{Name: "security", Result: &plugin.Result{Findings: []domain.Finding{
				{File: "main.go", Line: 19, Severity: plugin.SeverityMedium,
					Comment: "参数长度未校验，短参数将触发 slice bounds out of range"},
			}}},
		}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(mf.Findings) != 1 {
		t.Fatalf("同位置应合并为 1 条，实际 %d: %+v", len(mf.Findings), mf.Findings)
	}
	f := mf.Findings[0]
	if f.Severity != plugin.SeverityHigh {
		t.Fatalf("severity 应取高(high)，实际 %s", f.Severity)
	}
	if !strings.Contains(f.By, "correctness") || !strings.Contains(f.By, "security") {
		t.Fatalf("by 应并列双方插件，实际 %q", f.By)
	}
	if !strings.Contains(f.Comment, "切片越界") || !strings.Contains(f.Comment, "参数长度未校验") {
		t.Fatalf("意见应保留双方视角: %q", f.Comment)
	}
}

// 控制组：同文件不同行号不合并（相邻行常是两个真实的不同问题）；
// 不同文件同行也不合并。
func TestMergeKeepsDistinctLocationsSeparate(t *testing.T) {
	m := NewMerger(nil, logx.Nop())
	mf, err := m.Merge(context.Background(), regretTestPR(), "t1",
		[]Outcome{
			{Name: "correctness", Result: &plugin.Result{Findings: []domain.Finding{
				{File: "a.go", Line: 31, Severity: plugin.SeverityMedium, Comment: "问题甲"},
				{File: "b.go", Line: 19, Severity: plugin.SeverityMedium, Comment: "问题乙"},
			}}},
			{Name: "security", Result: &plugin.Result{Findings: []domain.Finding{
				{File: "a.go", Line: 32, Severity: plugin.SeverityLow, Comment: "问题甲的变体"},
			}}},
		}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(mf.Findings) != 3 {
		t.Fatalf("不同位置应保持独立，实际 %d: %+v", len(mf.Findings), mf.Findings)
	}
}
