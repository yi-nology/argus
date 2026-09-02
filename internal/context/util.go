package context

import "strings"

// scrub 从错误输出中抹除嵌入 URL 的凭证（git 报错会原样打印 clone URL）。
func scrub(msg string, secrets ...string) string {
	for _, s := range secrets {
		if s == "" {
			continue
		}
		msg = strings.ReplaceAll(msg, "oauth2:"+s+"@", "***@")
		msg = strings.ReplaceAll(msg, s, "***")
	}
	return msg
}
