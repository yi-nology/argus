// sanitize.go —— 落盘文件名安全化（OCR 留档/dream 导出共用）。
package plugin

import "git.example.com/enjoydream/agentkit/textutil"

// SanitizeFileStem 文件名安全化：owner/repo/number 等 PR 标识里的路径分隔与
// 引用语法字符归一为 "-"。GitLab 嵌套组 owner 含 "/"、TaskAPI 的 number 无
// 格式校验——未消毒的标识拼进留档文件名会导致写入失败甚至目录穿越（v3.9.1）。
// v3.13.0：实现下沉 agentkit textutil（本包保留 API 门面，三处调用方不动）。
func SanitizeFileStem(s string) string {
	return textutil.SanitizeFileStem(s)
}
