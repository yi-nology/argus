// Package gitdeepen 浅克隆工作副本按需历史补全的门面（v3.80.0 实现下沉
// agentkit/workcopy——工作副本生命周期属主收编 deepen 语义；本包保留 API
// 门面，ocrshim/ocrdeleg 两个调用方零改动）。
//
// 语义：先本地探测 merge-base 可达性（零网络），不可达且 .git/shallow 存在
// 再 `git fetch --deepen` 指数加深（64 起步 ×2）；详见 workcopy 包文档。
package gitdeepen

import "git.example.com/enjoydream/agentkit/workcopy"

// EnsureMergeBase 确保 origin/<baseBranch> 与 HEAD 的 merge-base 可达。
var EnsureMergeBase = workcopy.EnsureMergeBase
