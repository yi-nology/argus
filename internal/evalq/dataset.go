package evalq

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// datasetFile 基准集文件形态（eval/baseline/<language>.yaml，一语言一文件）。
type datasetFile struct {
	Language    string `yaml:"language"`
	Description string `yaml:"description,omitempty"`
	Cases       []Case `yaml:"cases"`
}

// Dataset 基准集（多语言样例集）。
type Dataset struct {
	// Description 各文件 description 按 language 聚合（报告头用）。
	Description map[string]string
	Cases       []Case
}

// LoadDir 装载目录下全部 *.yaml 基准集文件。文件名（去扩展名）必须与文件内
// language 字段一致——目录即语言维度的事实源，防两处漂移。
func LoadDir(dir string) (*Dataset, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("evalq: 读基准集目录: %w", err)
	}
	ds := &Dataset{Description: map[string]string{}}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("evalq: 读 %s: %w", path, err)
		}
		var f datasetFile
		if err := yaml.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("evalq: 解析 %s: %w", path, err)
		}
		lang := strings.TrimSuffix(e.Name(), ".yaml")
		if f.Language == "" {
			f.Language = lang
		}
		if f.Language != lang {
			return nil, fmt.Errorf("evalq: %s 的 language=%q 与文件名 %q 不一致", path, f.Language, lang)
		}
		for i := range f.Cases {
			f.Cases[i].Language = f.Language
		}
		if _, dup := ds.Description[f.Language]; !dup {
			ds.Description[f.Language] = f.Description
		} else if f.Description != "" {
			ds.Description[f.Language] = ds.Description[f.Language] + "；" + f.Description
		}
		ds.Cases = append(ds.Cases, f.Cases...)
	}
	if len(ds.Cases) == 0 {
		return nil, fmt.Errorf("evalq: 基准集目录 %s 无样例（fs.ErrNotExist 口径: %w）", dir, fs.ErrNotExist)
	}
	if err := ds.Validate(); err != nil {
		return nil, err
	}
	return ds, nil
}

// validLabels / validVerdicts 词表。
var (
	validLabels   = []string{LabelStandard, LabelKnownMiss, LabelKnownFP}
	validVerdicts = []string{VerdictApprove, VerdictComment, VerdictRequestChanges}
)

// Validate 数据集完整性：id 唯一、label/verdict 合法、known_fp 无 must_find、
// known_miss 必有 must_find（漏检正例的定义面）。
func (d *Dataset) Validate() error {
	seen := map[string]string{}
	for _, c := range d.Cases {
		if c.ID == "" {
			return fmt.Errorf("evalq: 样例缺 id（language=%s title=%q）", c.Language, c.Title)
		}
		if prev, dup := seen[c.ID]; dup {
			return fmt.Errorf("evalq: 样例 id 重复: %s（%s 与 %s）", c.ID, prev, c.Language)
		}
		seen[c.ID] = c.Language
		if c.Language == "" {
			return fmt.Errorf("evalq: 样例 %s 缺 language", c.ID)
		}
		if !slices.Contains(validLabels, c.Label) {
			return fmt.Errorf("evalq: 样例 %s label=%q 非法（词表 %v）", c.ID, c.Label, validLabels)
		}
		if !slices.Contains(validVerdicts, strings.ToLower(c.Expect.Verdict)) {
			return fmt.Errorf("evalq: 样例 %s verdict=%q 非法（词表 %v）", c.ID, c.Expect.Verdict, validVerdicts)
		}
		switch c.Label {
		case LabelKnownFP:
			if len(c.Expect.MustFind) > 0 {
				return fmt.Errorf("evalq: known_fp 正例 %s 不应有 must_find（金标准=无缺陷）", c.ID)
			}
		case LabelKnownMiss:
			if len(c.Expect.MustFind) == 0 {
				return fmt.Errorf("evalq: known_miss 正例 %s 必须有 must_find（金标准=有缺陷且曾被漏检）", c.ID)
			}
		}
		if strings.TrimSpace(c.Diff) == "" {
			return fmt.Errorf("evalq: 样例 %s 缺 diff", c.ID)
		}
	}
	return nil
}

// ByLanguage 按语言分组（分语言评测报告的维度源）。
func (d *Dataset) ByLanguage() map[string][]Case {
	out := map[string][]Case{}
	for _, c := range d.Cases {
		out[c.Language] = append(out[c.Language], c)
	}
	return out
}

// Languages 语言清单（稳定序）。
func (d *Dataset) Languages() []string {
	langs := make([]string, 0, len(d.ByLanguage()))
	for lang := range d.ByLanguage() {
		langs = append(langs, lang)
	}
	slices.Sort(langs)
	return langs
}
