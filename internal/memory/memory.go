// Package memory repo 级评审记忆（对标 ZCode memory：一文件一事实 + 索引注入）。
//
// 与 dream 合流：dream 周期把低置信模式（确定性信号抽取——regret/suppress 计数，
// 无需 ZCode 式 LLM 抽取子代理）双写为本目录事实文件（frontmatter + 正文）；
// R3 提示词注入索引提供前置感知，细节全文仍走 search_knowledge 检索
// （dream/<platform>/<owner>/<repo> 聚合文档）。
//
// 文件形态：
//
//	---
//	name: <slug>
//	type: pattern
//	description: <一行摘要（R3 索引行的来源）>
//	updated: <RFC3339>
//	---
//	<正文>
//
// 目录：<MemoryDir>/<platform>-<owner>-<repo>/，文件名 = 消毒后的 slug。
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// IndexBudgetRunes 索引注入的 rune 预算（超限确定性降级：截断+尾注）。
const IndexBudgetRunes = 2000

// Ref 记忆归属仓库（与 storage.RepoRef 同形，避免依赖）。
type Ref struct {
	Platform, Owner, Repo string
}

// Save 写入/覆盖一条事实（幂等；同名 slug 覆盖）。description 须单行。
func Save(dir string, r Ref, slug, factType, description, body string, at time.Time) error {
	if dir == "" || r.Owner == "" || slug == "" || (r.Repo == "" && r.Owner != orgDirName) {
		return fmt.Errorf("memory: dir/ref/slug 不可空")
	}
	d := repoDir(dir, r)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "---\nname: %s\ntype: %s\ndescription: %s\nupdated: %s\n---\n\n",
		sanitize(slug), sanitize(factType), SingleLine(description), at.Format(time.RFC3339))
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\n")
	}
	name := fmt.Sprintf("%s.md", sanitize(slug))
	return os.WriteFile(filepath.Join(d, name), []byte(b.String()), 0o644)
}

// ClearRepo 清空仓库记忆目录（dream 澄清路径：仓库不再有低置信模式时，
// 旧记忆必须随知识导出一并退场——v3.8.0 只增不删教训的记忆侧同款修复）。
func ClearRepo(dir string, r Ref) error {
	d := repoDir(dir, r)
	entries, err := os.ReadDir(d)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			if err := os.Remove(filepath.Join(d, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// LoadIndex 渲染索引（"- description" 行，文件名序确定性）；目录不存在或为空
// 返回 ""。超预算截断并注明——索引是感知面，不是检索面。
func LoadIndex(dir string, r Ref, budget int) string {
	d := repoDir(dir, r)
	entries, err := os.ReadDir(d)
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if budget <= 0 {
		budget = IndexBudgetRunes
	}
	var b strings.Builder
	for _, n := range names {
		desc := readDescription(filepath.Join(d, n))
		if desc == "" {
			continue
		}
		line := "- " + desc
		if b.Len()+len([]rune(line)) > budget {
			b.WriteString("…（其余记忆已省略，全量见 search_knowledge 检索）")
			return b.String()
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// repoDir 仓库记忆目录（各段消毒，防平台 owner 嵌套组"/"等路径噪声）。
func repoDir(dir string, r Ref) string {
	if r.Owner == orgDirName {
		return filepath.Join(dir, orgDirName) // 组织桶固定目录，无仓后缀
	}
	return filepath.Join(dir, sanitize(fmt.Sprintf("%s-%s-%s", r.Platform, r.Owner, r.Repo)))
}

// readDescription 从 frontmatter 提取 description 行（自产文件，简单行扫描足够）。
func readDescription(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "description: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "description: "))
		}
	}
	return ""
}

// sanitize 文件名/键消毒：非 [A-Za-z0-9._-] 一律替换 '-'。
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// SingleLine 压平换行（description 进单行 frontmatter）；memory/dream 共用单源（v3.64.0）。
func SingleLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// orgDirName 组织级记忆桶：跨仓共享的模式/教训（dream 周期聚合或运维工具
// 手工放入）。与仓桶同文件形态；读取时先于仓桶注入，前缀区分来源。
const orgDirName = "_org"

// OrgRef 组织桶归属（Save/LoadIndex 共用；Owner=orgDirName 由本常量统一）。
var OrgRef = Ref{Owner: orgDirName}

// LoadIndexWithOrg 读组织桶 + 仓桶两级记忆索引：组织段（跨仓模式）在前，
// 仓段在后，共用同一预算（组织段超预算时确定性截断，仓段保底 60%）。
// 无组织桶时零开销（读不到目录直接跳过）。
func LoadIndexWithOrg(dir string, r Ref, budget int) string {
	if budget <= 0 {
		budget = IndexBudgetRunes
	}
	orgBudget := budget * 2 / 5 // 组织段上限 40%，防跨仓噪声挤占本仓记忆
	org := loadIndexFrom(repoDir(dir, OrgRef), orgBudget)
	if org != "" {
		org = "## 跨仓常见模式（组织级）\n" + org
	}
	repo := loadIndexFrom(repoDir(dir, r), budget-len([]rune(org)))
	if org == "" {
		return repo
	}
	if repo == "" {
		return org
	}
	return org + "\n## 本仓记忆\n" + repo
}

// loadIndexFrom 单目录索引拼装（LoadIndex 原逻辑抽出复用；budget<=0 视为空）。
func loadIndexFrom(d string, budget int) string {
	if budget <= 0 {
		return ""
	}
	entries, err := os.ReadDir(d)
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		desc := readDescription(filepath.Join(d, n))
		if desc == "" {
			continue
		}
		line := "- " + desc
		if b.Len()+len([]rune(line)) > budget {
			b.WriteString("…（其余记忆已省略，全量见 search_knowledge 检索）")
			return b.String()
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
