// Package plugin 是 Argus 的插件契约单源（L4 核心域，design §3）：统一契约
// （AgentPlugin/Task/Result/PRContext/DiffContext/AgentManifest）→ Registry
// 注册表；分发/熔断/聚合执行面在 dispatch（v3.173.0 拆出）。另含完备性裁判
// 契约（Completeness*/WorkflowOutput）、插件/仓配置解析（PluginsFile/
// RepoConfig）、Hook* 观测钩子（实现装配期注入，L4 无 prometheus 依赖）、
// ToolErrNote 工具错误信封、DataFence 提示词数据围栏（fence.go 单源，禁本地
// 转义克隆）、severity/prkey/词表净化件与 FindingsWire 序列化。
// 原则：路由/裁决/合并不走 LLM，全部确定性 Go 代码；跨包引用只准引本包类型，
// 不得克隆。
package plugin
