package evalq

// Stability 裁决稳定性（issue #25：同 MR 多次审查裁决一致性）：k 轮同输入
// 跑批的逐案一致率均值。
//
// runs[r][caseID] = 第 r 轮该案的 verdict。口径：逐案取 k 轮中出现最多的
// verdict 占比（modal agreement），对全部案取均值——1.0 = k 轮全同，0 = 全异。
// 轮数 <2 或无样例返回 1（无信息不惩罚，报告侧以 runs 字段呈现轮数）。
// 各轮输入同一基准集，缺轮（某轮未出该案）按缺失票处理（分母只计实际票数）。
func Stability(runs []map[string]string) float64 {
	if len(runs) < 2 {
		return 1
	}
	total, cases := 0.0, 0
	for caseID := range runs[0] {
		counts := map[string]int{}
		votes := 0
		for _, r := range runs {
			if v, ok := r[caseID]; ok {
				counts[v]++
				votes++
			}
		}
		if votes == 0 {
			continue
		}
		majority := 0
		for _, n := range counts {
			majority = max(majority, n)
		}
		total += float64(majority) / float64(votes)
		cases++
	}
	if cases == 0 {
		return 1
	}
	return total / float64(cases)
}

// CaseStability 单案 k 轮一致率（钻取面：哪些案裁决摇摆）。
func CaseStability(runs []map[string]string, caseID string) float64 {
	if len(runs) < 2 {
		return 1
	}
	counts := map[string]int{}
	votes := 0
	for _, r := range runs {
		if v, ok := r[caseID]; ok {
			counts[v]++
			votes++
		}
	}
	if votes == 0 {
		return 1
	}
	majority := 0
	for _, n := range counts {
		majority = max(majority, n)
	}
	return float64(majority) / float64(votes)
}
