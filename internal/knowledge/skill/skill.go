// Package skill L5 知识子系统 · Skill 方法论注入（design §9.1）。
// Skill ≠ agent：它是可复用的知识/方法论包（SKILL.md、评分标准、检查清单）。
package skill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Ref 技能引用（plugins.yaml 的 skillRefs 词表）。
type Ref struct {
	Name    string
	Version string
	Source  string // file | git | http | registry（v3.0 只实现 file）
}

// Skill 技能内容（拼进系统提示词，带 checksum 防内容漂移难排查）。
type Skill struct {
	Name     string
	Version  string
	Content  string
	Checksum string // sha256(content) 前 16 位
}

// Provider 技能解析接口（统一模式：稳定接口 → 注册表 → 配置声明 → 按需注入）。
type Provider interface {
	Resolve(ctx context.Context, ref Ref) (*Skill, error)
}

// FileProvider file 来源实现：root/<name>/SKILL.md 或 root/<name>.md。
// git/http 来源留接口，v3.0 不实现（§8 扩展点纪律：未实装前不引依赖）。
type FileProvider struct {
	Root string

	mu    sync.RWMutex
	cache map[Ref]*Skill
}

func NewFileProvider(root string) *FileProvider {
	return &FileProvider{Root: root, cache: map[Ref]*Skill{}}
}

func (p *FileProvider) Resolve(_ context.Context, ref Ref) (*Skill, error) {
	if ref.Source == "" {
		ref.Source = "file"
	}
	if ref.Source != "file" {
		return nil, fmt.Errorf("skill: 来源 %q 暂未实现（v3.0 仅支持 file），ref=%s", ref.Source, ref.Name)
	}

	p.mu.RLock()
	cached := p.cache[ref]
	p.mu.RUnlock()
	if cached != nil {
		return cached, nil
	}

	name := filepath.Clean("/" + strings.TrimSpace(ref.Name))[1:] // 防路径穿越
	if name == "" || strings.Contains(name, "..") {
		return nil, fmt.Errorf("skill: 非法引用 %q", ref.Name)
	}
	candidates := []string{
		filepath.Join(p.Root, name, "SKILL.md"),
		filepath.Join(p.Root, name+".md"),
	}
	var data []byte
	for _, c := range candidates {
		if b, err := os.ReadFile(c); err == nil {
			data = b
			break
		}
	}
	if data == nil {
		return nil, fmt.Errorf("skill: 未找到 %s（尝试 %v）", ref.Name, candidates)
	}
	sum := sha256.Sum256(data)
	s := &Skill{
		Name:     ref.Name,
		Version:  ref.Version,
		Content:  string(data),
		Checksum: hex.EncodeToString(sum[:8]),
	}
	p.mu.Lock()
	p.cache[ref] = s
	p.mu.Unlock()
	return s, nil
}
