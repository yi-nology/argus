package plugin

import (
	"strings"
	"testing"

	"github.com/oagentkit/textx/reportutil"
)

func TestParseRepoConfigReviewSection(t *testing.T) {
	rc, err := ParseRepoConfig([]byte(`
review:
  exclude_paths: ["**/generated/**", "proto/"]
  min_severity: medium
  max_comments: 10
  extra_instructions: "关注错误包装"
describe:
  labels:
    - {name: bug, description: 缺陷修复}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Review.ExcludePaths) != 2 {
		t.Fatalf("exclude_paths = %v", rc.Review.ExcludePaths)
	}
	if rc.Review.MinSeverity != "medium" {
		t.Fatalf("min_severity = %q", rc.Review.MinSeverity)
	}
	if rc.Review.MaxComments != 10 {
		t.Fatalf("max_comments = %d", rc.Review.MaxComments)
	}
	if rc.Review.ExtraInstructions != "关注错误包装" {
		t.Fatalf("extra_instructions = %q", rc.Review.ExtraInstructions)
	}
	if rc.Review.ReportTemplate != "" {
		t.Fatalf("未配置 report_template 应零值, got %q", rc.Review.ReportTemplate)
	}
	if len(rc.Describe.Labels) != 1 || rc.Describe.Labels[0].Name != "bug" {
		t.Fatalf("labels = %+v", rc.Describe.Labels)
	}
}

// v6.10.0 report_template 白名单：合法三值直通、非法值整份拒载（笔误如 analist
// 静默落意外模板比缺失更有害——与 min_severity 同配置卫生纪律）。
func TestParseRepoConfigReportTemplate(t *testing.T) {
	for _, ok := range []string{"standard", "analyst", "minimal"} {
		rc, err := ParseRepoConfig([]byte("review:\n  report_template: " + ok + "\n"))
		if err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
		if rc.Review.ReportTemplate != ok {
			t.Fatalf("%s 直通失败: %q", ok, rc.Review.ReportTemplate)
		}
	}
	for _, bad := range []string{"analist", "CUSTOM", "none"} {
		if _, err := ParseRepoConfig([]byte("review:\n  report_template: " + bad + "\n")); err == nil {
			t.Fatalf("%q 非法值应整份拒载", bad)
		}
	}
}

// 敌对输入：非法 severity 词整份拒绝（LoadRepoConfig 侧降级为无配置+告警）；
// 超限列表钳制、超长 extra_instructions 截断、max_comments 封顶。
func TestParseRepoConfigReviewClamps(t *testing.T) {
	if _, err := ParseRepoConfig([]byte("review:\n  min_severity: urgent\n")); err == nil {
		t.Fatal("非法 min_severity 应拒绝加载") // critical 已入词表（v5.5.0 四档），非法样例换 urgent
	}
	long := strings.Repeat("x", 9000)
	rc, err := ParseRepoConfig([]byte(
		"review:\n  max_comments: 99999\n  exclude_paths: [a, b, c]\n  extra_instructions: \"" + long + "\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if rc.Review.MaxComments != 500 {
		t.Fatalf("max_comments 未封顶: %d", rc.Review.MaxComments)
	}
	if len(rc.Review.ExcludePaths) != 3 {
		t.Fatalf("exclude_paths = %v", rc.Review.ExcludePaths)
	}
	if !strings.HasSuffix(rc.Review.ExtraInstructions, "…（截断）") {
		t.Fatalf("extra_instructions 未截断: %d runes", len([]rune(rc.Review.ExtraInstructions)))
	}
}

func TestParseRepoConfigReviewDefaults(t *testing.T) {
	rc, err := ParseRepoConfig([]byte("use: [correctness]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if rc.Review.MinSeverity != "" || rc.Review.MaxComments != 0 ||
		len(rc.Review.ExcludePaths) != 0 || len(rc.Review.IncludePaths) != 0 ||
		rc.Review.ReportTemplate != "" {
		t.Fatalf("缺省 review 段应全零值: %+v", rc.Review)
	}
}

func TestFilterByMinSeverity(t *testing.T) {
	fs := []Finding{
		{Severity: reportutil.Low},
		{Severity: reportutil.High},
		{Severity: reportutil.Medium},
		{Severity: reportutil.Low},
	}
	kept, dropped := FilterByMinSeverity(fs, reportutil.Medium)
	if len(kept) != 2 || dropped != 2 {
		t.Fatalf("kept=%d dropped=%d, want 2/2", len(kept), dropped)
	}
	if kept[0].Severity != reportutil.High || kept[1].Severity != reportutil.Medium {
		t.Fatalf("保留项顺序错乱: %+v", kept)
	}
	// 无阈值 / 阈值最低：原样返回
	kept2, dropped2 := FilterByMinSeverity(fs, "")
	if len(kept2) != 4 || dropped2 != 0 {
		t.Fatalf("空阈值应原样返回: %d/%d", len(kept2), dropped2)
	}
	kept3, _ := FilterByMinSeverity(fs, reportutil.Low)
	if len(kept3) != 4 {
		t.Fatalf("low 阈值不应滤任何词表级: %d", len(kept3))
	}
}

func TestRepoSettingsFilter(t *testing.T) {
	rc := &RepoConfig{Loaded: true, Review: ReviewSettings{
		ExcludePaths: []string{"**/generated/**", "proto/**"},
	}}
	filter := RepoSettingsFilter(rc)
	if !filter("api/generated/types.go") || !filter("proto/v1/x.proto") {
		t.Fatal("exclude 命中失败")
	}
	if filter("internal/api/types.go") {
		t.Fatal("exclude 误伤正常路径")
	}
	// 白名单模式：Include 非空时未命中者排除
	rc2 := &RepoConfig{Loaded: true, Review: ReviewSettings{IncludePaths: []string{"internal/**"}}}
	filter2 := RepoSettingsFilter(rc2)
	if filter2("cmd/main.go") != true || filter2("internal/x/y.go") != false {
		t.Fatal("include 白名单语义错误（true=排除）")
	}
	// 空配置：永不排除
	filter3 := RepoSettingsFilter(EmptyRepoConfig())
	if filter3("anything.go") {
		t.Fatal("空配置不应排除")
	}
}
