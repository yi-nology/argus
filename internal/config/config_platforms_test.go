package config

import (
	"testing"

	"github.com/yi-nology/go-git-platform/provider"

	// 空白 import 副作用：注册全部平台后端（provider registry 的填充方）。
	_ "github.com/yi-nology/go-git-platform/backends/all"
)

// TestSupportedPlatformsMatchSDK 防漂移：SupportedPlatforms 与上游 go-git-platform
// 注册后端集合必须双向相等——SDK 增删平台即此门禁红，强迫人工决策（补词表 +
// webhook_ensure 事件表 + mcpserver platformVocab + 文档，或显式排除并留痕）。
func TestSupportedPlatformsMatchSDK(t *testing.T) {
	registered := map[string]bool{}
	for _, p := range provider.RegisteredPlatforms() {
		registered[string(p)] = true
	}
	inVocab := map[string]bool{}
	for _, p := range SupportedPlatforms {
		inVocab[p] = true
		if !registered[p] {
			t.Fatalf("SupportedPlatforms 的 %q 未在 SDK provider registry 注册（SDK 去支持了？）", p)
		}
	}
	for p := range registered {
		if !inVocab[p] {
			t.Fatalf("SDK 注册了平台 %q 但 SupportedPlatforms 缺（新平台须入词表+事件表+文档，或显式排除留痕）", p)
		}
	}
}
