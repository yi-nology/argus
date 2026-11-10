// Package impactview 代码改动影响面视图（v3.183.0）：从一次变更出发，在依赖图
// 上反向推导受影响范围（blast radius），产出版本化快照 + 三出口渲染
// （PR 评论 mermaid / MCP get_impact / 自包含交互 HTML）。
//
// 分层管线（每层独立可用，高层失败逐层降级并记入 Notes）：
//
//	L0 文件层  变更文件 → 同包聚合（零成本，任何仓库可用）
//	L1 包层    Go import 反向闭包：谁依赖了改动的包（parser.ImportsOnly 扫描）
//	L2 符号层  变更触达符号 → 调用方（v6.0.0 消歧符号图优先、逐符号回退
//	           codeintel token 级调用位 + AST 封闭函数精化）
//	L3 测试面  受影响包目录下的 *_test.go 定位
//	P2 共变    git log 历史共变（change coupling，频率阈值过滤）
//
// 架构定位：影响面管线能力包（test/arch 分层白名单 internal 边 = gitdeepen/
// plugin/semsearch/storage，全部朝底座与契约单源方向）——输入只收通用原语
// （目录/路径/行区间），不认识 plugin.DiffContext；包装与接线在 runner
// （inject.go）。风险分是确定性算法（可复现、可解释），LLM 只做语义描述
// 不做打分。所有输出确定化排序，同输入同输出。
package impactview
