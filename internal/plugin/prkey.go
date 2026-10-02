package plugin

import "git.example.com/enjoydream/agentkit/workcopy"

// PRToKey 将 PRContext 转换为 agentkit/workcopy.WorktreeKey（工作副本定位键）。
// adapter_remote / adapter_cli / runner.inject 共享单一出处（v3.59.0 收口两处、v3.95.0 收编 runner 末份手抄）。
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
