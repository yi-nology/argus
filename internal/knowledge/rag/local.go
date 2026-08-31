// local.go KnowledgeService 本地实装（design §9.3 v3.1 最小闭环）。
// 检索源 = ARGUS_KNOWLEDGE_DIR 下的 markdown 文件（编码规范/部署约定/历史评审结论）。
// 检索算法 = 词元重叠打分（ASCII 词 + CJK 二元组），零外部依赖——向量库/ES 等
// 后端按 design 留给后续实装（接口不变，换 Retrieve 实现即可）。
package rag

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const (
	defaultTopK      = 5
	chunkTargetRunes = 600
	rescanInterval   = 10 * time.Minute
	toolSnippetRunes = 800 // 单片段进工具输出的上限
)

// indexedChunk 索引单元。
type indexedChunk struct {
	chunk  Chunk
	tokens []string
}

// Local 本地文件知识库（启动扫描 + 过期重扫）。
type Local struct {
	dir string

	mu        sync.Mutex
	index     []indexedChunk
	scannedAt time.Time
}

// NewLocal 构造并立即扫描。dir 不存在时返回错误（调用方回退 Noop）。
func NewLocal(dir string) (*Local, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("rag: %s 不是目录", dir)
	}
	l := &Local{dir: dir}
	l.rescan()
	return l, nil
}

// Retrieve 检索 topK 片段（filter 保留接口语义；本地实现支持 metadata 精确匹配）。
func (l *Local) Retrieve(_ context.Context, query string, topK int, filter Filter) ([]Chunk, error) {
	l.mu.Lock()
	l.maybeRescan()
	idx := l.index
	l.mu.Unlock()

	if topK <= 0 {
		topK = defaultTopK
	}
	qt := tokenize(query)
	if len(qt) == 0 {
		return nil, nil
	}
	type scored struct {
		c indexedChunk
		s float64
	}
	var hits []scored
	for _, ic := range idx {
		if !matchFilter(ic.chunk.Metadata, filter) {
			continue
		}
		s := score(ic.tokens, qt)
		if s > 0 {
			hits = append(hits, scored{ic, s})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].s > hits[j].s })
	if len(hits) > topK {
		hits = hits[:topK]
	}
	out := make([]Chunk, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.c.chunk)
	}
	return out, nil
}

// AsTool 把检索包成 search_knowledge 工具（挂进 R3 ReAct 工具表）。
func (l *Local) AsTool() tool.BaseTool {
	t, err := utils.InferTool("search_knowledge",
		"检索团队知识库（编码规范/部署约定/历史评审结论）。返回最相关的知识片段及出处。",
		func(_ context.Context, in *searchIn) (*searchOut, error) {
			chunks, err := l.Retrieve(context.Background(), in.Query, defaultTopK, nil)
			if err != nil {
				return &searchOut{Error: err.Error()}, nil
			}
			var b strings.Builder
			for _, c := range chunks {
				content := c.Content
				if t, tr := truncRunes(content, toolSnippetRunes); tr {
					content = t + "…（截断）"
				}
				fmt.Fprintf(&b, "【%s】%s\n\n", c.Metadata["file"], content)
			}
			return &searchOut{Results: b.String()}, nil
		})
	if err != nil {
		return nil
	}
	return t
}

type searchIn struct {
	Query string `json:"query" jsonschema:"description=检索关键词或问题"`
}
type searchOut struct {
	Results string `json:"results,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ---------- 索引与打分 ----------

func (l *Local) maybeRescan() {
	if time.Since(l.scannedAt) < rescanInterval {
		return
	}
	l.rescan()
}

func (l *Local) rescan() {
	l.scannedAt = time.Now()
	l.index = l.index[:0]
	_ = filepath.WalkDir(l.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil // 不可读文件跳过，不阻塞整体索引
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(l.dir, path)
		for _, c := range chunkMarkdown(string(data)) {
			l.index = append(l.index, indexedChunk{
				chunk: Chunk{
					Content:  c.content,
					Metadata: map[string]string{"file": rel, "heading": c.heading},
				},
				tokens: tokenize(c.content),
			})
		}
		return nil
	})
}

type mdChunk struct {
	heading string
	content string
}

// chunkMarkdown 按空行分段并合并到 ~chunkTargetRunes，标题取最近的一级/二级标题。
func chunkMarkdown(text string) []mdChunk {
	var chunks []mdChunk
	heading := ""
	var cur []string
	curLen := 0
	flush := func() {
		if len(cur) == 0 {
			return
		}
		content := strings.TrimSpace(strings.Join(cur, "\n"))
		if content != "" {
			chunks = append(chunks, mdChunk{heading: heading, content: content})
		}
		cur = cur[:0]
		curLen = 0
	}
	for _, line := range strings.Split(text, "\n") {
		if h, ok := mdHeading(line); ok {
			flush()
			heading = h
			continue
		}
		cur = append(cur, line)
		curLen += len([]rune(line))
		if curLen >= chunkTargetRunes {
			flush()
		}
	}
	flush()
	return chunks
}

func mdHeading(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, "## ") {
		return strings.TrimPrefix(t, "## "), true
	}
	if strings.HasPrefix(t, "# ") {
		return strings.TrimPrefix(t, "# "), true
	}
	return "", false
}

// tokenize 分词：ASCII 词（小写）+ CJK 二元组（单字成词时补单字）。
func tokenize(s string) []string {
	var out []string
	var word strings.Builder
	addWord := func() {
		if w := strings.ToLower(word.String()); w != "" {
			out = append(out, w)
		}
		word.Reset()
	}
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case unicode.IsLetter(r) && r < 128 || unicode.IsDigit(r):
			word.WriteRune(r)
		case unicode.Is(unicode.Han, r):
			addWord()
			if i+1 < len(runes) && unicode.Is(unicode.Han, runes[i+1]) {
				out = append(out, string([]rune{r, runes[i+1]}))
			} else {
				out = append(out, string(r))
			}
		default:
			addWord()
		}
	}
	addWord()
	return out
}

// score 词元重叠打分：命中数 / sqrt(查询词元数)，天然归一、偏短片段。
func score(chunkTokens, queryTokens []string) float64 {
	if len(chunkTokens) == 0 || len(queryTokens) == 0 {
		return 0
	}
	set := make(map[string]int, len(chunkTokens))
	for _, t := range chunkTokens {
		set[t]++
	}
	hits := 0
	for _, t := range queryTokens {
		hits += set[t]
	}
	if hits == 0 {
		return 0
	}
	return float64(hits) / sqrtF(float64(len(queryTokens)))
}

func sqrtF(x float64) float64 {
	if x <= 0 {
		return 1
	}
	// 牛顿迭代 4 次足够检索排序精度
	g := x
	for i := 0; i < 4; i++ {
		g = (g + x/g) / 2
	}
	return g
}

func matchFilter(meta map[string]string, filter Filter) bool {
	for k, v := range filter {
		if meta[k] != v {
			return false
		}
	}
	return true
}

func truncRunes(s string, n int) (string, bool) {
	r := []rune(s)
	if len(r) <= n {
		return s, false
	}
	return string(r[:n]), true
}
