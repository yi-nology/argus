package evalq_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/evalq"
)

// baselineDir 仓库内基准集目录（四语言一目录，装载器单源消费）。
const baselineDir = "../../eval/baseline"

// minCasesPerLanguage 每语言最少样例数（issue #28 验收口径）。
const minCasesPerLanguage = 10

// wantLanguages 基准集语言清单（issue #28：Go/Java/Python/JS-TS）。
var wantLanguages = []string{"go", "java", "python", "jsts"}

// TestBaselineDataset 四语言基准集完整性门禁：
//   - 语言面齐（go/java/python/jsts）；
//   - 每语言 ≥10 条已裁决样例；
//   - 每语言含已知漏检（known_miss）与误报（known_fp）正例各 ≥1；
//   - 装载器全量校验过（schema/标签结构在 LoadDir 内硬断言）。
func TestBaselineDataset(t *testing.T) {
	ds, err := evalq.LoadDir(baselineDir)
	if err != nil {
		t.Fatalf("基准集装载失败: %v", err)
	}
	byLang := ds.ByLanguage()
	for _, lang := range wantLanguages {
		cases, ok := byLang[lang]
		if !ok {
			t.Errorf("缺语言基准: %s", lang)
			continue
		}
		if len(cases) < minCasesPerLanguage {
			t.Errorf("%s 样例数 %d < %d", lang, len(cases), minCasesPerLanguage)
		}
		labels := map[string]int{}
		severities := map[string]bool{}
		for _, c := range cases {
			labels[c.Label]++
			for _, mf := range c.Expect.MustFind {
				severities[mf.Severity] = true
			}
		}
		if labels[evalq.LabelKnownMiss] < 1 {
			t.Errorf("%s 缺已知漏检正例（known_miss ≥1）", lang)
		}
		if labels[evalq.LabelKnownFP] < 1 {
			t.Errorf("%s 缺已知误报正例（known_fp ≥1）", lang)
		}
		if !severities["high"] {
			t.Errorf("%s 无 high 严重度金标准（严重缺陷漏检率维度无锚）", lang)
		}
	}
	// 语言面不越界（目录里混入未约定语言 = 基准集口径漂移）
	for lang := range byLang {
		if !slices.Contains(wantLanguages, lang) {
			t.Errorf("基准集出现未约定语言: %s", lang)
		}
	}
	// 样例 id 全局唯一（LoadDir.Validate 已断言，这里再对账总数）
	if len(ds.Cases) != len(byLang["go"])+len(byLang["java"])+len(byLang["python"])+len(byLang["jsts"]) {
		t.Fatalf("样例总数对账失败: %d", len(ds.Cases))
	}
}

// TestBaselineStubRun 全集打桩跑批：分语言指标产出 + 报告 artifact 落盘
// （eval/results/baseline-stub.{json,md}）。⚠️ 分名纪律：打桩写 stub、
// 真实跑批写 latest——共用 latest 会让每次 make test 把真实基线覆盖成
// stub（2026-11-14 实锤：门禁跑完真实基线被冲掉，消费测试连锁红）。
func TestBaselineStubRun(t *testing.T) {
	ds, err := evalq.LoadDir(baselineDir)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := evalq.Evaluate(context.Background(), ds.Cases, stubReviewer{}, evalq.Options{
		Model:  "stub",
		Params: map[string]string{"dataset": "eval/baseline"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.ByLanguage) != len(wantLanguages) {
		t.Fatalf("分语言聚合 = %d, want %d", len(rep.ByLanguage), len(wantLanguages))
	}
	// 打桩口径：全部金标准命中、known_fp 样例按夹具多报 style-nit → 全集 FP 数
	// = known_fp 样例数；漏检应为 0。
	if rep.Overall.Confusion.FN != 0 {
		t.Fatalf("打桩不应漏检: %+v", rep.Overall.Confusion)
	}
	wantFP := 0
	for _, c := range ds.Cases {
		if c.Label == evalq.LabelKnownFP {
			wantFP++
		}
	}
	if rep.Overall.Confusion.FP != wantFP {
		t.Fatalf("FP = %d, want %d（known_fp 正例逐一多报）", rep.Overall.Confusion.FP, wantFP)
	}

	outDir := filepath.Join("..", "..", "eval", "results")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonBytes, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "baseline-stub.json"), jsonBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	var md strings.Builder
	if err := rep.WriteMarkdown(&md); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "baseline-stub.md"), []byte(md.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("基准集打桩跑批报告已落盘: eval/results/baseline-stub.{json,md}（分语言 %d 组）", len(rep.ByLanguage))
}
