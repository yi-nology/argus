package runner

// v6.10.0 报告契约模板裁决接线单测：按仓 yaml 优先于服务默认，空/非法层层
// 回退 standard（优先级语义 reporttmpl.Resolve 已钉，这里钉 runner 侧接线）。

import (
	"testing"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/plugin"
)

func TestResolveReportTemplate(t *testing.T) {
	rcAnalyst := &plugin.RepoConfig{Review: plugin.ReviewSettings{ReportTemplate: "analyst"}}
	cases := []struct {
		name string
		cfg  string
		rc   *plugin.RepoConfig
		want string
	}{
		{"仓级优先于服务", "minimal", rcAnalyst, "analyst"},
		{"仓空取服务默认", "minimal", &plugin.RepoConfig{}, "minimal"},
		{"全空回退 standard", "", nil, "standard"},
		{"服务非法回退 standard", "bogus", nil, "standard"},
		{"仓级生效服务无关", "", rcAnalyst, "analyst"},
	}
	for _, c := range cases {
		r := &Runner{Cfg: &config.Config{ReportTemplate: c.cfg}}
		if got := r.resolveReportTemplate(c.rc); got != c.want {
			t.Fatalf("%s: resolve = %q, want %q", c.name, got, c.want)
		}
	}
}

// v6.11.0 四级链的 admin 层：repo_settings > 仓 yaml > env（任务级在 Execute
// 装配处先于本链判定，不在此函数语义内）。
func TestResolveReportTemplateChain(t *testing.T) {
	rcYaml := &plugin.RepoConfig{Review: plugin.ReviewSettings{ReportTemplate: "analyst"}}
	r := &Runner{Cfg: &config.Config{ReportTemplate: "minimal"}}
	if got := r.resolveReportTemplateChain(rcYaml, taskKnobs{reportTemplate: "standard"}); got != "standard" {
		t.Fatalf("admin 层应压过 yaml/env: %q", got)
	}
	if got := r.resolveReportTemplateChain(rcYaml, taskKnobs{}); got != "analyst" {
		t.Fatalf("yaml 层应生效: %q", got)
	}
	if got := r.resolveReportTemplateChain(nil, taskKnobs{}); got != "minimal" {
		t.Fatalf("env 默认应生效: %q", got)
	}
	if got := r.resolveReportTemplateChain(nil, taskKnobs{reportTemplate: "bogus"}); got != "bogus" {
		t.Fatalf("本函数不复核词表（复核在 loadTaskKnobs 消费端）: %q", got)
	}
}
