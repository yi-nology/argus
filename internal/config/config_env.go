// config_env.go —— 环境变量读取面：env* 助手族 + ARGUS_API_TOKENS 三档 scope 解析 + .env 加载。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	ekitconfig "git.example.com/enjoydream/ekit/config"
)

func envStr(key, def string) string {
	return ekitconfig.GetStringWithDefault(key, def)
}

// normalizeEvidenceMode 把 ARGUS_REFLECTION_EVIDENCE 归一到 off|soft|strict。
// 未知值视为 off（保守，不改变 v3.4 行为）并由调用方启动日志可见。
func normalizeEvidenceMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "soft":
		return "soft"
	case "strict":
		return "strict"
	default:
		return "off"
	}
}

// normalizeCompletenessPolicy 把 ARGUS_COMPLETENESS_POLICY 归一到 annotate|demote。
// 未知值回退 annotate（保守：只注明不改 verdict）。词表与 plugin.CompletenessPolicy
// 常量对齐——此处用字面量避免 config → plugin 依赖边（config 被全仓引用）。
func normalizeCompletenessPolicy(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "demote":
		return "demote"
	default:
		return "annotate"
	}
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, ok := ekitconfig.GetBool(key)
	if !ok {
		warnBadEnv(key, v, def)
		return def
	}
	return b
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, ok := ekitconfig.GetInt(key)
	if !ok {
		warnBadEnv(key, v, def)
		return def
	}
	return n
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		warnBadEnv(key, v, def)
		return def
	}
	return d
}

func envFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		// 严格解析（v3.8.0）：此前 fmt.Sscanf 对 "0.5abc" 尾部垃圾静默接受为 0.5，
		// 与 envInt/envBool 的严格纪律不一致，费率 knob 配错不易察觉。
		warnBadEnv(key, v, def)
		return def
	}
	return f
}

// warnBadEnv 非法环境变量值回退默认时必须留痕——静默回退会让
// "以为开了 SKIP_TLS/DEBUG/MAX_CONCURRENT 实际没开"的配置漂移无从排查
// （config 包此时还没有 logger，直接 stderr）。
// APITokenScope 一条 API token 及其权限档（read < ci < admin）。
type APITokenScope struct {
	Token string
	Scope string // read | ci | admin
}

// apiTokenScopeRank scope→覆盖等级（闭集校验单源；未知 scope = 0 = 不覆盖任何要求）。
var apiTokenScopeRank = map[string]int{"read": 1, "ci": 2, "admin": 3}

// ParseAPITokens 解析 ARGUS_API_TOKENS（"tok:scope,tok2:scope2"）。坏条目
// 告警跳过——fail-closed：只认完全合法条目，绝不猜默认档。
func ParseAPITokens(raw string) []APITokenScope {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []APITokenScope
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tok, scope, ok := strings.Cut(part, ":")
		tok, scope = strings.TrimSpace(tok), strings.TrimSpace(strings.ToLower(scope))
		if !ok || tok == "" || apiTokenScopeRank[scope] == 0 {
			warnBadEnv("ARGUS_API_TOKENS", part, "token:scope（scope ∈ read|ci|admin）")
			continue
		}
		out = append(out, APITokenScope{Token: tok, Scope: scope})
	}
	return out
}

// warnBadEnv 非法环境变量统一告警。
func warnBadEnv(key, val string, def any) {
	fmt.Fprintf(os.Stderr, "argus.config: 环境变量 %s=%q 非法，回退默认值 %v\n", key, val, def)
}

// LoadDotEnv 依次尝试常见位置的 .env（服务根目录、lacus/ 根目录；test 子目录
// 多一级）。v3.81.0 实现下沉 ekit/config（服务开发期配置引导通用语义；
// 候选路径与语义逐字保持：首个存在文件生效、已 export 的优先、无文件静默）。
func LoadDotEnv() {
	ekitconfig.LoadDotEnv()
}
