package argusctx

// 出站 PII 脱敏（v6.16.0）行为钉 + 误报评测护栏（合规优先拍板的代价面）。
// 护栏语义：代码评审内容里高发的数字串（日期/时间戳/订单号/十六进制）零命中；
// 真 PII（手机号/带校验位身份证）必须命中。gak pii 精度漂移会在此红。

import (
	"strings"
	"testing"

	"github.com/yi-nology/go-git-platform/provider"
	"github.com/yi-nology/goagent-kernel/pii"
)

// withPIIPolicy 装配脱敏策略并注册恢复（pii 是进程级全局策略，测试间必须还原）。
func withPIIPolicy(t *testing.T, on bool) {
	t.Helper()
	prevEnabled, prevSurfaces, prevStrategy := pii.Enabled(), pii.Surfaces(), pii.Strategy()
	t.Cleanup(func() {
		pii.SetEnabled(prevEnabled)
		pii.SetSurfaces(prevSurfaces)
		_ = pii.SetStrategy(prevStrategy)
	})
	pii.SetEnabled(on)
	bit, err := pii.SurfaceBit(pii.SurfaceOutput)
	if err != nil {
		t.Fatal(err)
	}
	pii.SetSurfaces(bit)
	if err := pii.SetStrategy(pii.StrategyMask); err != nil {
		t.Fatal(err)
	}
}

// validResidentID 生成带合法校验位的 18 位身份证（权重表国标 GB 11643）。
func validResidentID() string {
	const base = "11010519900307"
	var sum int
	weights := []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	digits := base + "123" // 17 位本体
	for i := 0; i < 17; i++ {
		sum += int(digits[i]-'0') * weights[i]
	}
	check := "10X98765432"[sum%11]
	return digits + string(check)
}

func TestMaskOutboundPII(t *testing.T) {
	withPIIPolicy(t, true)

	// 真手机号 + 真身份证：打码 + 透明脚注。
	hit := maskOutboundPII("联系 13812345678，证件 " + validResidentID() + " 请核实")
	if strings.Contains(hit, "13812345678") || strings.Contains(hit, validResidentID()) {
		t.Fatalf("PII 未打码: %s", hit)
	}
	if !strings.Contains(hit, piiMaskNote) {
		t.Fatalf("缺透明脚注: %s", hit)
	}

	// 干净内容：原样直通、无脚注。
	clean := maskOutboundPII("### 结论: ✅ 通过\n- `a.go:1` [HIGH] 空指针 — by correctness")
	if clean != "### 结论: ✅ 通过\n- `a.go:1` [HIGH] 空指针 — by correctness" {
		t.Fatalf("干净内容被误改: %s", clean)
	}

	// 幂等：重复打码不叠脚注。
	once := maskOutboundPII("手机 13812345678")
	twice := maskOutboundPII(once)
	if once != twice {
		t.Fatalf("不幂等:\n1=%q\n2=%q", once, twice)
	}

	// 策略关闭：直通。
	withPIIPolicy(t, false)
	off := maskOutboundPII("手机 13812345678")
	if !strings.Contains(off, "13812345678") {
		t.Fatalf("关闭后不应打码: %s", off)
	}
}

// TestMaskOutboundPIIFalsePositiveCorpus 误报护栏：代码评审内容高发的数字串
// 形态（日期/时间戳/长订单号/版本号/十六进制哈希）不得命中——命中即上游精度
// 回退或误报面扩大，合规收益不抵保真损失，必须红。
func TestMaskOutboundPIIFalsePositiveCorpus(t *testing.T) {
	withPIIPolicy(t, true)
	corpus := []string{
		"2026-11-12 15:53:55",
		"20261112155355",
		"订单号 20261112000123456789",
		"commit 3fc0bb0f924b33a97c4a1c498bd23ae29dff",
		"timestamp 1762925635",
		"trace_id 4bf92f3577b34da6a3ce929d0e0e4736",
		"端口 8899，超时 30s，重试 3 次",
		"身份证号 123456（6 位短号不命中）",
		"1.2.3.4 版本号 2026.11.12",
	}
	for _, s := range corpus {
		if got := maskOutboundPII(s); got != s {
			t.Fatalf("误报: %q → %q", s, got)
		}
	}
}

func TestMaskReviewOptions(t *testing.T) {
	withPIIPolicy(t, true)
	opts := maskReviewOptions(provider.CreateReviewOptions{
		Body: "总结：联系 13812345678",
		Comments: []provider.ReviewComment{
			{Path: "a.go", Body: "行内意见，证件 " + validResidentID()},
			{Path: "b.go", Body: "干净意见"},
		},
	})
	if strings.Contains(opts.Body, "13812345678") || !strings.Contains(opts.Body, piiMaskNote) {
		t.Fatalf("总评未脱敏: %s", opts.Body)
	}
	if strings.Contains(opts.Comments[0].Body, validResidentID()) {
		t.Fatalf("行内评论未脱敏: %s", opts.Comments[0].Body)
	}
	if opts.Comments[1].Body != "干净意见" {
		t.Fatalf("干净行内被误改: %s", opts.Comments[1].Body)
	}
}
