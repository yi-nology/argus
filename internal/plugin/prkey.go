package plugin

import "git.example.com/enjoydream/agentkit/workcopy"

// PRToKey 将 PRContext 转换为 agentkit/workcopy.WorktreeKey（工作副本定位键）。
// adapter_remote / adapter_cli 共享单一出处（此前两处逐字段克隆，v3.59.0 收口）。
func PRToKey(pr PRContext) workcopy.WorktreeKey {
	return workcopy.WorktreeKey{
		Platform:      pr.Platform,
		Owner:         pr.Owner,
		Repo:          pr.Repo,
		Number:        pr.Number,
		HeadSHA:       pr.HeadSHA,
		DefaultBranch: pr.DefaultBranch,
	}
}
