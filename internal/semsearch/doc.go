// Package semsearch 工作副本语义检索底座（v3.126.0，ARGUS_SEMSEARCH opt-in）：
// 把工作副本的 Go 顶层声明/文本文件抽成结构化摘要 chunk → embedding → 进程内
// 余弦检索，补 search_code（词法正则）答不了的「按意图找代码」面（哪里处理
// 认证/哪里刷新 session token——代码里未必有查询词）。v5.9.0 增图面：search
// 命中自动附调用方/被调邻接（SymRef），另提供 Path 两符号最短调用链与
// SymbolContext 单符号上下文两个图查询——调用边自产自 chunk 同一次 AST 解析
// （见 chunks.go/graph.go）。v5.10.0：节点键迁移 NodeKey 契约（dir 限定消歧
// 跨包同名），EdgeAugmenter 精确边注入钩子（档一接口，实现方经 WithEdges
// 注入，成功应用后精确边替换词法邻接、Manager.Precise 透出模式）。v5.11.0：
// embedder 允许 nil（纯图模式）——图查询（Path/SymbolContext/命中邻接）零
// embedding 依赖，Search/Manager.Search 在 Index.Search 单源拦截报错；审查
// 管线以 NewIndex(nil) 接图，不被 ARGUS_EMBEDDING_* 绑架。
//
// 行业坐标（2026-10 调研）：审查是异步、准确率优先、延迟不敏感的任务，预索引
// 在审查场景的价值高于编码助手场景（Greptile 全库图/Ellipsis 向量+历史 PR 索引
// 均为护城河）；「embedding 已死」是编码助手侧叙事，Cursor +12.5% 实证与
// JetBrains 反向建 RAG 都指向「grep+语义双工具组合最佳」。本包取混合形态的
// 低成本近似：embedding 文本用「路径+文档注释+签名」结构化摘要而非裸代码
// （Greptile NL 化做法的零 LLM 版），向量只驻进程内存、不落外部向量库。
//
// 层级：L0 纯底座（零 internal 依赖）。Embedder 由装配侧注入（rag.OpenAIEmbedder
// 天然满足 Embed+Dim 两方法）；进程内向量、余弦、指纹增量刷新均在此单源。
package semsearch
