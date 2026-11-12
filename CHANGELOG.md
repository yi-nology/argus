# argus CHANGELOG

> 本文件由 `matrix-tools/scripts/gen_changelogs.py` 从 git 历史生成，按 tag 切段、取每段内的 conventional commit 渲染。

## [v6.14.0] — 2026-11-12

- 与 v6.13.0 相比无代码差异（版本号/打包变更）

## [v6.13.0] — 2026-11-12

### 新增(impact)：v6.11.0 四连增强——目录展开 + PNG导出 + 小地图 + 节点拖拽

AC 目录节点双击展开/收起：动态插入成员文件节点 + 虚线连接，
收起时移除。展开状态目录节点虚线框标识。
AK 导出 PNG：SVG → XMLSerializer → Image → Canvas → toDataURL，
2x 缩放白底，可直接贴 IM/文档。
AM 小地图：右下角 180×120 缩略图，节点以红/灰小矩形表示，
蓝色矩形指示当前视口位置，随 apply() 实时更新。
AN 节点拖拽：节点上 mousedown 拖动更新位置 + 重绘连接边，
拖拽时禁用视口平移（nodedrag CSS class）。
全量测试零失败 + staticcheck 干净 + arch 守卫绿。

### 新增(impact)：v6.10.6 演化对比图上高亮——新增绿框 / 移除灰虚框

renderImpactHTML 从 opts.Compare 构建 addedSet/removedSet 传入 svgBody，
新增节点绿色粗框 + tooltip 🆕，移除节点灰色虚线框 + tooltip 🔄。
与页头演化摘要块（compareSummary）联动——数字 + 图上高亮双通道。
全量测试零失败 + staticcheck 干净 + arch 守卫绿。

### 新增(impact)：v6.10.5 边类型过滤——按 rel 关系显隐边

工具栏加边类型 chips（紫色调区分节点 chips）：caller/import/source/
lineage/test_of/coupled 各一枚，点击切换显隐。
CSS .edge.hidden{display:none} + JS data.rel 匹配。
全量测试零失败 + staticcheck 干净 + arch 守卫绿。

### 新增(impact)：v6.10.4 全屏模式——隐藏 header/toolbar，viewport 占满

工具栏加「全屏」按钮：document.documentElement.requestFullscreen +
body.fullscreen CSS 隐藏 header/toolbar，viewport 高度 100vh。
再点退出全屏恢复。
全量测试零失败 + staticcheck 干净 + arch 守卫绿。

### 新增(impact)：v6.10.3 关键路径高亮——改动→测试 BFS 最短路径金色虚线

改法：svgBody 内 BFS 从每个 changed 节点找最近 test 节点，
回溯路径上的边标记 criticalEdges，画边时金色虚线高亮。
帮 review 快速定位「改动 → 需跑测试」链路。
全量测试零失败 + staticcheck 干净 + arch 守卫绿。

### 新增(impact)：v6.10.2 图例说明风险热力图含义

受影响文件图例加「颜色越红=关联越多=风险越高」说明，
与 v6.10.1 风险热力图联动。
全量测试零失败 + staticcheck 干净 + arch 守卫绿。

### 新增(impact)：v6.10.1 风险热力图——degree 归一化蓝→红渐变

非改动节点按 degree/maxDeg 插值 fill 色（蓝 #3b82f6 → 红 #ef4444），
degree 越大颜色越红 = 风险 Concentration 越高的文件在图上越显眼。
changed 节点保持红色不变，不受影响。
全量测试零失败 + staticcheck 干净 + arch 守卫绿。

### 新增(impact)：v6.10.0 分享视图——复制当前 focus/scale/tx/ty 状态链接

工具栏加「分享视图」按钮：navigator.clipboard 复制当前 URL +
focus（如聚焦）+ scale + tx + ty 四参数。
接收方打开链接时 URLSearchParams 解析四参数精确恢复视图状态
（focus 自动聚焦开侧栏 + 缩放平移复原）。
全量测试零失败 + staticcheck 干净 + arch 守卫绿。

### 新增(impact)：v6.9.9 打印样式 + 键盘快捷键

方向 Z：@media print 隐藏 toolbar/aside，SVG 自适应宽度，
  .node.dim 透明度调为 .3 保证打印可读，details 防跨页断裂。
方向 AD：键盘快捷键——Esc 关侧栏，F 复位，+/− 缩放。
  输入框聚焦时不拦截（e.target.tagName==='INPUT' 短路）。
全量测试零失败 + staticcheck 干净 + arch 守卫绿。

### 新增(impact)：v6.9.8 tooltip 加风险信号——度数 + findings 数

悬停节点时 tooltip 显示「关联 N · 意见 M」——度数（边数）代表
风险 Concentration，findings 数（已有徽标计数复用）代表审查密度。
零结构改动，纯 tooltip 字符串追加。
全量测试零失败 + staticcheck 干净 + arch 守卫绿。

### 新增(impact)：v6.9.7 节点大小编码风险 + 边粗细编码调用频率

方向 T：节点大小编码风险——文件节点度（边数）越大框越高。
  改法：svgBody 统计每个节点在 viewEdges 中的出现次数，
  节点高度 = nodeH + min(degree,6)*4。度大 = 被更多人调用/
  影响更多人 = 风险 Concentration 的一手信号。
方向 U：边粗细编码调用频率——同一 (from,to) 对的边数越多线越粗。
  改法：统计 edgeFreq，stroke-width = 1.2 + 0.4×(freq-1)，封顶 3.0。
  目录聚合后边自然变粗（多条 file→caller 合并为一条 file→dir）。
全量测试零失败 + staticcheck 干净 + arch 守卫绿。

### 新增(impact)：v6.9.6 复制路径按钮 + 只看改动过滤

方向 S：侧栏文件节点 ID 行加「📋复制路径」按钮——navigator.clipboard
  writeText 复制 file: 前缀后的纯路径，供外部工具/IDE 使用。
方向 W：类型过滤 chips 末尾加「只看改动」开关——非 changed 节点 dim，
  再点取消恢复。独立状态不与 kindFilter 互斥。
全量测试零失败 + staticcheck 干净 + arch 守卫绿。

### 新增(impact)：v6.9.5 URL 参数直达——?focus=<nodeId> 自动聚焦并开侧栏

纯 JS 改动：URLSearchParams 解析 focus 参数，存在且 byId 命中时
调 focusFileNode() 完成聚焦+侧栏+视口定位三联动。
外部集成场景：报告/评论可链 ?focus=file:path 直达具体节点。
全量测试零失败 + staticcheck 干净 + arch 守卫绿。

### 新增(impact)：v6.9.4 暗色模式——prefers-color-scheme 自动切换

纯 CSS 改动：@media(prefers-color-scheme:dark) 覆盖全站配色。
覆盖范围：body/header/toolbar/viewport/aside/buttons/inputs/chips/
节点文本/边/标签/徽标/badge 三色。零 JS 零 HTML 结构变更。
全量测试零失败 + staticcheck 干净 + arch 守卫绿。

### 新增(impact)：v6.9.3 搜索命中自动定位 + 被折叠函数可点击搜索

方向 D：搜索命中自动定位——filter() 后第一个非 dim 节点居中视口
  改法：querySelectorAll('#g .node:not(.dim)') 取第一个命中，
  计算视口坐标差值平移 tx/ty，apply() 重绘。
方向 E：被折叠函数可点击搜索定位——侧栏函数名点击 → 触发搜索框
  改法：cplrow 样式 + onclick 设置 q.value 并 dispatch input 事件，
  复用方向 D 的自动定位链路。
全量测试零失败 + staticcheck 干净 + arch 守卫绿。

### 新增(impact)：v6.9.2 影响面视图三向增强——侧栏函数列表 + 目录聚合 + 成员展开

方向 A：改动文件侧栏展示被折叠函数列表
- viewGraph 返回 symFoldNames（fileID → 函数名列表）
- foldedSymsJSON 序列化注入 HTML 数据块
- showPanel 读取 folddata 展示「被折叠函数 N」列表
方向 B：调用方按目录分组折叠
- viewGraphDirGroupCap=4：同目录文件数超阈值时聚合
- 目录节点 kind=package，label=目录名，detail=N 个文件
- 边重定向到目录节点并按 (from,to) 去重
- 目录深度 = 最小成员深度
方向 C：目录聚合节点侧栏展开成员文件
- dir: 前缀节点点击侧栏显示完整成员文件列表
- 与既有 package 节点详情共存（dirExtra 优先）
全量测试零失败 + arch 守卫绿。视图剪枝层改动——快照 JSON 全量不回退。

- 杂务：高版本 Go 语言特性现代化（纯等价重构，零行为变更）

吃 Go 1.21+ 标准库与语言特性，全部为机械等价替换：
- sort.Strings/Ints → slices.Sort；sort.Slice/Stable → slices.SortFunc/SortStableFunc
  （比较器统一 cmp.Compare/strings.Compare；降序用 -c / 参数对调）
- map 收集+排序 → slices.Sorted(maps.Keys(m))（merger/funcdiff/coupling/graph/
  html/service_knowledge/dream/build 共 9 处 helper 与内联形态）
- 计数循环 for i := 0; i < N; i++ → for range N / for i := range N（含字符串
  按字节扫描 range len(s)）；基准测试 for b.N → for b.Loop()
- 干净 WaitGroup fan-out（wg.Add+go+defer Done）→ wg.Go（storage 并发测试 ×3、
  r4ckpt 并发落键 ×1）；async.GoSafe 场景保留原样（panic 防护语义不同）
- 线性查找（纯 == 判断）→ slices.Contains（semsearch stepRel）
保守跳过（拿不准不改）：
- r3tools.go sortToolsByName：比较器内调用 Info(ctx) 有副作用
- sandbox_verify.go 外层循环：i 被内层循环修改，非纯计数
- embed_batch.go 分批循环：while 形态增量推进，非计数循环
- dispatcher_run.go 两处 fan-out：async.GoSafe 携带 panic 恢复，wg.Go 不等价
门禁：gofmt -l 除生成文件外零输出；go vet 零告警；go build ./... 通过；
go test ./... 与基线全绿一致；改动并发包 -race 抽查通过；benchmark 冒烟通过。


- deps：上游升级批——go-git-platform v0.127.0 + goagent-kernel v0.34.0（吃下重定向凭据修复与执行意图红利）

- ggp v0.126.0→v0.127.0：多跳重定向凭据复活修复（issue #127，GitLab 令牌
  重定向场景不再泄漏）+ LastReadAt 族/gogit rev/ttlCache 世代等被动受益
- gak v0.31.0→v0.34.0：执行意图持久化/taskboard 令牌铸造/checkpoint Seq 门/
  egress 强度分档；argus 消费面仅 budget/checkpoint/ptc/toolname，BREAKING 全落
  零消费面；唯一适配=QA PTC 补 AllowUnmanaged（v0.32 起有绑定无判定环缺省拒，
  运行期拒绝由 qa 测试实锤抓获）
- 未升 ggp v0.129.0：v0.128 起硬依赖 pkg≥v0.45（Errorf 删除）与 gak 全线
  （含 main）冲突，MVS 编译断——已提票 goagent-kernel#134，待其迁移发版回补
- guardx 保持 v0.24.3（v0.24.5 适配 pkg v0.46 同因缓升）
- go.mod 污染回退实录：首轮 go get 抬升全部直接依赖行（pkg v0.46 混入），
  checkout 回退后外科式单模块升级重作
- 46 包全绿 + arch 门禁绿


## [v6.11.0] — 2026-11-12

### 新增(report)：报告模板演进批 v6.11.0——任务级 --template 覆盖 + repo_settings admin 旋钮 + 规模统计 + CI 状态进 meta

spec §9 演进记录（v6.10.0 非目标解禁三项 + C 类记账落地一项）：
- 任务级模板覆盖：@argus review --template analyst（commentcmd ExtractTemplateFlag
  纯词法零依赖；词表校验失败截停报错；模板键经任务 artifacts 下发，runner 侧
  IsValid 防御复核——API 路径可绕开 commentcmd）
- repo_settings report_template 指针旋钮（Parse 形状校验，词表归 runner 消费端
  ——reposettings 零 internal 依赖纪律）；裁决链升四级：任务级 > admin > 仓 yaml
  > env
- analyst 报告头规模统计（N 文件 +A/−B，DiffContext 就地汇总，兑现 v6.10 TODO）
- CI 状态进全模板 meta：SDK CommitStatusManager capability 门 + 统一词表
  worst-state 聚合；取不到省略不显式报无，绝不阻断审查
测试：commentcmd/reposettings/runner 链/reportview 渲染四组新增；46 包全绿 +
arch 门禁绿（service→reporttmpl 新边补白）

### 新增(impact)：v6.9.2 影响面视图三向增强——侧栏函数列表 + 目录聚合 + 成员展开

方向 A：改动文件侧栏展示被折叠函数列表
- viewGraph 返回 symFoldNames（fileID → 函数名列表）
- foldedSymsJSON 序列化注入 HTML 数据块
- showPanel 读取 folddata 展示「被折叠函数 N」列表
方向 B：调用方按目录分组折叠
- viewGraphDirGroupCap=4：同目录文件数超阈值时聚合
- 目录节点 kind=package，label=目录名，detail=N 个文件
- 边重定向到目录节点并按 (from,to) 去重
- 目录深度 = 最小成员深度
方向 C：目录聚合节点侧栏展开成员文件
- dir: 前缀节点点击侧栏显示完整成员文件列表
- 与既有 package 节点详情共存（dirExtra 优先）
全量测试零失败 + arch 守卫绿。视图剪枝层改动——快照 JSON 全量不回退。

## [v6.10.0] — 2026-11-12

### 新增(report)：报告契约模板系统——standard/analyst/minimal 三模板白名单 + 分析师综合段 + 声明五态/风险链渲染

spec: docs/superpowers/specs/2026-11-12-report-templates-design.md
- internal/reporttmpl 新包：模板词表单源（standard|analyst|minimal）+ analyst
  综合段提示词（检查表提炼自 mr-review playbook：漏项清单/坏味词表/30 秒结构
  判定/输出纪律）+ chain 纪律片段 + Resolve 三层裁决
- analyst 综合段（workflow 条件阶段 LayerR4X）：merge 后一次 LLM 调用统一产出
  claims/design/test_gaps/open_questions；失败降级 Analyst=nil + Degraded 注明
  退化为 standard 骨架；R5 裁决不读新字段
- 风险链=findings 复用指纹/抑制/R5/行内/fix 全闭环；domain.Finding 加 chain
  证据链字段（渲染属性不进指纹）；builtin 仅 analyst 经 Artifacts 通道追加
  chain 纪律（standard/minimal 提示词 golden 钉住逐字节不动）
- 渲染分发 reportview.Report.Template：analyst 声明对照表（✅⚠️❌➕❗ 五态）→
  风险链🔴🟠分组+chain 子段→坏味编号组→结构维度（触发式）→测试缺口/待确认；
  minimal 结论+高危封顶 10 条；standard 逐字节保留
- 配置面：.reviewagents.yaml review.report_template 白名单 sanitize（非法整份
  拒载）+ ARGUS_REPORT_TEMPLATE 服务默认（reporttmpl.Resolve 按仓优先）
- 敌对输入：SanitizeAnalyst 条数/runes 双钳 + 全渲染面 fence；输入三区 DataFence
- result_json 顺带 analyst/report_template 结构化字段
- arch 白名单补 reporttmpl 新包 + 五条消费边；测试 46 包全绿

### 修复(impact)：v6.9.1 混合语言 PR 影响面图可读性——列 0 超载时 symbol 折叠进父文件

实测 Rust 混合 PR（27 changed symbol + 7 changed file = 34 节点列 0）在
1080p 视口完全不可读：边交叉全糊，caller 文件全压一列。
修法（viewGraph 视图剪枝层，快照 JSON 全量不回退）：
- 列 0 改动节点超 viewGraphSymbolsCap(12) 时，changed symbol 折叠进父文件节点
- 文件节点 Detail 补 "N 函数被触达" 计数
- sym→caller 边重定向为 file→caller，按 (from,to) 去重
- 度=0 孤立 symbol 仍按原逻辑剪掉（不参与折叠判定）
- 快照入参零污染（mermaid/报告面用原 snapshot 不受影响）
回归：TestViewGraphSymbolCollapse（15 sym 折叠到 3 file + 边 15→4 去重）
+ TestViewGraphNoCollapseUnderCap（cap 内行为不回退）
+ TestViewGraphCollapseIsolatedSymsSkipped（孤立 symbol 不触发折叠）
+ TestRenderHTMLSymbolCollapse（端到端 SVG/JSON 数据面分离验证）

- 文档(spec)：报告契约模板系统设计——standard/analyst/minimal 三模板白名单 + 分析师综合段

## [v6.9.0] — 2026-11-12

### 新增(all)：第三轮审计 P1/P2 行为批——事件 Outbox 跨实例分发 + Token 双账本 + 副作用对账 + R5.L 完整性门禁 + 五枚运行时指标（#10 #11 #14 #15 #16）

P1-05(#10): 发布点 Bus 快路径 + event_outbox 双写，每实例 fanout 环（500ms）回灌
本地 Bus，Event.ID 双源去重；SSE handler 语义不变；24h 保留期随环清理。跨实例
送达回归（双 Service 同库 outbox）。
P1-06(#11): 配额账（done 口径）不回退；真实消费账（done+failed+stale 聚合）+
ARGUS_REAL_TOKEN_BUDGET 独立上限（超限任务失败终态）；/usage/monthly 双账本。
P2-03(#14): CommentDelivery 扩副作用账本（处理事实/回帖终态/外部评论 ID/ref 四列
分离）；回帖出口经 ctx 关联回填；OutOfSyncDeliveries 对账（回帖成功但任务 failed /
done 无回帖）进运维 ticker + 指标；重投递计数。
P2-04(#15): R5.L——LLM 主备切换史进裁决，本轮降级 → APPROVE 拦下 demote COMMENT
+ 结构化原因；无降级路径语义不回退（插件失败率/证据充分性已由完备性裁判覆盖）。
P2-05(#16): argus_migration_lease_lost_total / argus_authz_denied_total{route,scope} /
argus_events_dropped_total{reason} / argus_comment_redelivery_total /
argus_budget_exceeded_total{ledger} 五枚指标上线并接入消费点。
arch 白名单补边三处显式化（obs→storage 钩子 / storage→progress 载荷 / runner→obs）。
全量+arch+lint+e2e+eval 五门禁绿。
Co-authored-by: Murphy-Yi <zhangyi@Murphy-YideMac-Mini.local>

### 新增(web)：Skills · AI 协作页 + install-skills.sh 一键安装

- scripts/install-skills.sh：三技能（argus-mcp/triage/precommit）一键装进
  AI 客户端 skills 目录——curl|bash 一行（默认 ~/.claude/skills），--target
  zcode/自定义目录；仓库检出内执行自动离线本地复制；幂等重复执行即升级；
  本地实测 3 技能 4 文件安装链路绿
- web/skills.html：两类技能对照（AI 客户端 vs 服务端方法论）/一键安装/
  MCP 四客户端接入表（Claude Code/Cursor/ZCode/自建）/AI 协作五步工作流图
  （一句话→review_pr→结构化意见→triage→fix 闭环）/场景速查 8 行/安全边界
- navbar 加「AI 协作」；首页 MCP 区链入

### 修复(storage)：第三轮审计 P1 批——迁移租约 fencing + webhook 回查补 Kind + 在飞唯一约束三方言（#6 #7 #8）

P1-01(#6): 续约失败/ok=false = 失租 → fencing 取消迁移执行、结果作废不写 marker、
他实例接手；marker 落库前终检续约补强语义；主路不阻塞等续约环（死锁教训：成功路径
曾互等 renewStop/renewDone）。
P1-02(#7): 撞唯一防线后的在飞回查 7 占位符 6 实参（漏 t.Kind）→ 回查必失败上抛
→ webhook 500（GitHub 连续 500 禁用 webhook）。并发双投递回归三方言。
P1-03(#8): EnsureSameSHAGuard → EnsureInFlightGuard 三方言——SQLite partial index
不变、pg 补 partial unique index、MySQL in_flight_key 生成列+唯一索引；手工任务空
SHA 参与唯一；执行时点移入 Open 迁移租约（DDL 串行）。并发回归×2。
全量+arch+lint+e2e+eval 五门禁绿。
* test(storage,worker): 在飞唯一约束下机械测试夹具各占一号（P1-03 引入约束后的夹具适配）
openTestStore 经 storage.Open 建防线（v6.8.0 起 guard 在 Open 迁移租约内）——
裸任务/同号双任务直插形态撞唯一索引：认领/心跳/优先级机制测试改各任务独占
number（非去重语义面，语义测试不动）。
* fix(storage): 三方言矩阵双修——REPEATABLE READ 回查快照限制 + 续约间隔 atomic 化
①CreateTaskDirectIfIdle 撞在飞唯一约束后的回查移出事务：MySQL REPEATABLE READ
下事务内快照取自首读，并发方在其后提交的行不可见（CI MySQL 矩阵实锤 1062 后
回查落空上抛）——quietDB 独立连接读最新提交态。
②migrationRenewalInterval 改 atomic.Int64：续约环 goroutine 的读与测试 cleanup
恢复写之间无 happens-before 边（CI -race 实锤）。
本地 MySQL/PG 容器矩阵 + 全量 -race 绿。
---------
Co-authored-by: Murphy-Yi <zhangyi@Murphy-YideMac-Mini.local>

### 修复：第三轮审计 P0 批——迁移 fail-closed + 统一 Authorizer + RepoMap 隔离 + 租约串行（#2 #3 #4 #9 #5）

* fix(all): 第三轮审计 P0 批——数据迁移 fail-closed + 统一 Authorizer + RepoMap 目录隔离与凭据卫生 + AutoMigrate 纳入租约（#2 #3 #4 #9 #5）
P0-01(#2): 关键回写迁移失败缺省 fail-closed 拒绝启动（此前 warn-only 带病服务），
ARGUS_MIGRATION_FAIL_OPEN=1 显式逃生门；runOneTimeMigration 单点收口+三态回归。
P0-02(#3): 统一 Authorizer 收口读面仓库约束——reports/stats/repomap/repo-settings
四读面补判定（此前持任意 read token 可跨仓读完整报告），suppress 写面与 repo-settings
PUT/DELETE 的仓库维度同补；ErrAuthzDenied→403；SSE 事件逐仓过滤。防漂移：路由授权
矩阵 e2e 门禁（IDL 路由↔矩阵行双向对账）+ 跨仓 403/行过滤行为矩阵。
P0-03(#4): RepoMap 工作目录三元组哈希寻址（g/s1+r 与 g+s1-r 碰撞串仓封死）；
凭据改 http.extraHeader 注入（token 不落 .git/config）；孤儿克隆 GC（旧寻址残留
立即清、超期失配克隆按保留期清）。
P1-04(#9): repomap ls-remote 补 -C 且探测失败翻转为 fail-safe 刷新（提交更新后
地图永不刷新根因）；freshness 探测移出全局锁；旧目录治理随 P0-03。
P0-04(#5): AutoMigrate+LONGTEXT 纳入迁移租约（leader_leases 单表引导→抢锁→
持锁 DDL→释放），多副本并发首启串行化，双实例并发 Open 回归。
全量+arch+lint(staticcheck/vet/gofmt)+e2e+eval 五门禁绿。
* fix(storage): 双副本并发首启引导竞态——leader_leases 改判定+原生 DDL（CI 双方言矩阵实锤）
GORM AutoMigrate 的 check-then-create 在两副本并发 AutoMigrate(&LeaderLease{}) 时
撞 catalog：MySQL 1050 table already exists / PG pg_type_typname_nsp_index 23505。
改 Migrator().HasTable 判定 + 原生 CREATE TABLE IF NOT EXISTS（服务端幂等），DDL
竞态失败再复核表存在（他副本先建成功即放行，类型对齐由全量 AutoMigrate 兜）。
本地 pg/MySQL 容器矩阵 + 并发压测 5×/3× 绿。
---------
Co-authored-by: Murphy-Yi <zhangyi@Murphy-YideMac-Mini.local>

### 修复(web)：dark 守护——MutationObserver 守死 data-theme=dark + 关切换钮

破案：theme-default rc.137 hydration 会把 localStorage 写成 'auto' 并按系统
偏好翻回 light（themeData 的 colorMode 不被客户端尊重，colorMode:'dark' 无效）。
改 dark-only 守护：head 脚本 setItem+set data-theme+MutationObserver 拦截翻转，
colorModeSwitch:false。产品决策：宣传页统一暗色气质（设计系统按 dark-first）。

### 修复(web)：修 Pages 构建——lock 竞态损坏重建 + sass 钉版 + CI npm ci --ignore-scripts

- lock 与 package.json 失步根因：npm install 与 sass install 两进程并写
  lock 竞态（markdown-it@15 树缺失）→ 干净重装重建（281 包同步）
- 依赖评审条件落地：sass ^1.108.2 → 1.108.2 精确钉版；CI/本地
  npm ci --ignore-scripts（@parcel/watcher 等安装期脚本默认拒绝，已验证
  禁脚本可完整构建）；install/build 与 deploy 本就分 job 分权限

- 文档(release)：v6.9.0 发布说明——第三轮审计 P0/P1/P2 行为批合流
- 文档(web)：架构页 + 全站关系图——四库分层图/数据流/定位象限/生产拓扑

- 新增 architecture.html：一张图讲清 Argus 与四共享库关系——
  go-git-platform（接入层：7 平台 provider/webhook/token）、oekit（底座层：
  日志/HTTP/存储三方言/租约）、oagentkit（能力层：LLM/编队/textx 索引/检索）、
  goagent-kernel（内核层：ReAct/ptc/预算）→ Argus 审查领域层；含消费面标注、
  四库定位对照表、一次审查 9 步数据流图（库归属色标+不走 LLM 锚点标注）、
  L0–L5 分层堆叠图、切分理由
- 全站补关系图：首页技术底座迷你生态条（链架构页）/guide 生产拓扑图
  （开发者→平台→Argus×2→PG/LLM 网关→回帖）/vs 形态定位象限图
  （SaaS↔自托管 × 评论↔需求级，竞品散点）
- 纯 CSS 图套件零新依赖：diagram/arch-map/eco-grid/flow-row/layer-stack/
  quadrant/topo 七组件，贴 dark 设计系统

- 文档(web)：宣传页内容扩充——酷炫介绍 + 部署构建 + 版本规划 + 竞品分析

- 首页：渐变数据带（24 工具/7 平台/13+ 语言/R1-R5/68+ golden/开源离线）+
  「为什么是 Argus」六大高亮卡（需求复核/确定性裁决/编队共识/影响面/
  数据主权/评测体系），hover 浮起+渐变边条
- 新增 guide.md：go install/Release/GHCR 多架构镜像+docker run/Actions
  三段流水线（ci 四门禁+三方言矩阵、image GHCR、pages）/k3s 换芯部署链
- 新增 roadmap.md：v3→v6 里程碑时间线 + v6.7+ / v7.0 / 长期愿景三段规划
  （状态标注：已拍板/待评估/外部依赖，事实源=tech-debt 与在案待办）
- 新增 vs.md：竞品分析（调研快照 2026-09~11 标注时点）——业界格局表 +
  18 行能力矩阵（vs CodeRabbit/PR-Agent/Copilot/Agent-CI 形态）+
  差异化定位 + 诚实差距 + 选型建议；事实源=仓内对标调研存档
- 样式：stats/hl-card/vsmatrix/roadmap-phase 组件面入 index.scss

- 文档(web)：宣传页 VuePress 2 重构——真实回帖截图画廊 + web/ 站点源

- 单页手写 docs/index.html → VuePress 2 站点（vuepress@2.0.0-rc.31 +
  theme-default@2.0.0-rc.137 + sass，纯构建期依赖；base=/argus/ 对齐 Pages）
- 真实回帖截图画廊 8 图：GitHub/GitLab 官方 markdown 渲染器渲染真实评论
  （smoke PR 行内 [HIGH]+suggestion 一键应用/执行期回执/验收清单报告；
  43 生产实弹 TL;DR 先行+REQUEST_CHANGES 回执，生产图内部标识模糊脱敏）+
  影响面/函数级 diff/mermaid 实拍工件图三张
- 页面内容全量承继（hero/五能力卡/六步流水线/MCP 24 工具/快速开始），
  平台口径顺手追平七平台；旧 index.html 退役
- pages.yml 改 VuePress 构建部署（web/** 触发；npm ci+build →
  upload-pages-artifact）；.gitignore 补站点依赖与构建产物
- 构建产物本地验证绿（2 页渲染；坑：rc.137 全局样式约定 styles/index.scss
  而非 index.css，写错静默不生效；{#id} 锚点语法不支持改显式 HTML 锚）

- 文档：追平批——文档口径清扫 + releases 补 v6.5.0/v6.6.0 + 宣传页多语言卡

- docs/ide-mcp.md 工具面 21→24（补 get_trace/get_symbol_context/get_repo_map，
  第四处契约漏载点；报错注记同步 search_sem/get_trace/get_symbol_context 门控）
- README：版本行 v6.3.0→v6.6.0（去「CHANGELOG 记至 v3.69.0」怪句）；目录导览
  context/→argusctx/（v6.2.0 改名欠账）+langcap·impactview 入册；「生产现运行
  v3.0.x/HEAD v5.1.x」过时边界订正为 43 在役 v6.5.0/HEAD v6.6.0；设计骨架存储面
  补 PostgreSQL
- docs/releases/ 补 v6.5.0.md + v6.6.0.md 发布说明（沿 v6.0.0.md 形态）
- docs/index.html 补「🌐 多语言仓库」能力卡（此前零多语言卖点）；CHANGELOG
  v6.6.0 节追记本批 + 计数修正（ide-mcp 两处）


- 格式(web)：doc 页宽度体系统一——--content-width 740→1120 消宽屏黑洞

用户标注 guide/architecture 页两侧大留白。元凶：主题 --content-width: 740px
正文列过窄，而 diagram 破框 1294px 与正文错位，两层留白叠加。
- --content-width 740→1120px；段落/列表限 880px 行宽（表格/图/代码全列宽）
- diagram 去 translateX(-50%) 破框，与正文/表格同宽对齐
- topo 拓扑节点 space-between 撑满图框

- 格式(web)：布局重构 + 去 AI 味——双栏 hero/宽内容列/瀑布流画廊/编辑式排版

用户红框标注四问题：宽屏两侧黑洞、卡片头部空档、3+2 网格缺口、画廊留白。
- 双栏 hero（文案 | 产品 mockup）填满宽屏；内容列 960→1240px
- 修 hero 光晕 -200vw 负 inset 横向溢出（fullPage 拉超宽、内容缩左角的真凶）
- 主题默认 hero 标题+简介重复块隐藏（空洞感来源）
- 卡片图标+标题同行（消头部大空档）；核心能力补第 6 卡「评论指令交互」
  （消网格缺口）；画廊改 CSS columns 瀑布流（消行间高低差留白）
- 去 AI 味：删大写英文眉标/徽章 tagline/居中堆叠——改编号小节（01-06）+
  左对齐标题+右侧导语的编辑式排版；navbar 重复 GitHub 链接去重

- 格式(web)：隐藏主题切换钮（dark-only 守护下点击无效徒增困惑）
- 格式(web)：dark-first 重设计——去衬线/去 emoji/单点渐变/产品 mockup 视觉锚点

用户再判『没有设计美感』。设计师视角批判：衬线+emoji=廉价、渐变五处打架、
浅色发灰无锚点。推倒重做 dark-first（Linear/Supabase 气质）：
- 近黑画布 #0b0d12 + 白色 800 字重大字 sans（-0.03em 字距）
- 单点渐变纪律：仅主按钮与 hero 光晕用渐变，标题/数字/标签全去渐变
- SVG 线性图标替 emoji（11 枚 20px stroke 图标）
- 产品 mockup 视觉锚点：浏览器条 + 真实审查截图 + 渐变描边 + 光晕投影
- 发丝描边暗玻璃卡（内顶部高光）+ mono 标签体系 + 窗口三点终端代码块
- 暗色机制修正：rc.137 是 html[data-theme] 属性且 bootstrap 无 dark 默认，
  head 脚本预写 localStorage(vuepress-color-scheme)=dark 双保险
- 明暗双态（显式切回 light 仍可用）

- 格式(web)：宣传页视觉重设计——设计系统全量重做

用户反馈整页偏平、不专业。重做视觉语言（纯 CSS 零新依赖）：
- hero：蓝紫渐变光晕 + 44px 网格纹理（径向遮罩渐隐）+ 渐变大标题
  （text-wrap: balance 自然断行）+ 渐变光效主按钮/玻璃次按钮
- 区块节奏：流水线与 MCP 两块包深色 band（暗蓝渐变+暗玻璃卡），
  白-暗-白 节奏对比
- 统一卡片系统：hl-card 渐变边条+hover 浮起+蓝晕阴影；gallery 阴影
  卡+hover 微放大；pipeline 暗玻璃卡；stats 玻璃拟态浮卡
- eyebrow 标题体系（渐变小字 CAPABILITIES/PIPELINE/… + 居中衬线大标题）
- 终端三点代码块 / 毛玻璃 navbar / 竞品表表头淡蓝圆角 / 路线图渐变
  时间线 / 页脚渐变分隔——全站视觉语言统一
- 明暗双态（html.dark）阴影与卡片变量对齐


## [v6.6.0] — 2026-11-11

### 新增(langcap)：语言能力面收口——langcap 单一事实源 + funcdiff 补 JS/TS + 契约面补 get_repo_map

- internal/langcap：图层/funcdiff/semsearch chunk 三套参与扩展名集收口一处
  （族与能力面分层声明，Graph/FuncDiff 由族并集派生、SemChunk 独立历史面不并集）；
  impactview/semsearch/workflow 消费，R3/MCP 描述口径经 LangsOf 派生；
  防漂移测试三组（族并集不变式/展示名全登记/mcpserver 描述↔langcap 集合一致）
- funcdiff 词法面补 JS/TS（reJSFunc 保守形态集：function/箭头赋值/类方法含
  get/set 与泛型，语句关键字黑名单；与 shell/py/rust 同走 BodyHash 对齐）
- verdict benign 补真缺口：test_*.py（pytest 惯例）/.spec.jsx/.tsx/.test.mjs/.cjs/_test.sh/.bash
- 契约面补 get_repo_map（v5.6.2 已注册但 README§3/SKILL.md/tools.md 三处漏载），
  工具计数 23→24 六处；此前语言口径三套并存（README「Python/JS/TS」/tech-debt
  「py/js」/「funcdiff 扩 shell/py」），rust/jvm/c-family 全程文档隐身
- README §5.5 重写为「多语言支持与语言能力矩阵」（能力×精度档+已知缺口）；
  §5.3「非 Go 跳过」限定语修正；MCP 描述补语言范围与索引分层；index.html 同步
- repomap 装载边按族分流（shell source=RelSource、import/include=RelImport）
- 架构面：langcap 入册 L0（semsearch/impactview/workflow 白名单增边），
  ARCHITECTURE.md/deep-dive 同步；上游 oagentkit/textx#83（polyglot 缺 shell 词法表）
- make test 全量 -race 绿 + vet/fmt/arch 门禁绿

## [v6.5.0] — 2026-11-11

### 新增(platform)：平台配置面收口——SDK 七平台对齐 + 平台词表单一事实源 + 文档口径统一

- config.SupportedPlatforms（gitea/forgejo/github/gitlab/gitee/gitcode/tencent_code）
  收口散落五处的平台口径；env 装载循环/启动校验提示由它派生
- forgejo/gitcode/腾讯工蜂接入面补齐：装载循环扩为 SDK 注册后端全集（此前 forgejo
  被 MCP/事件表宣称支持但 ARGUS_FORGEJO_* 不存在、requirePlatform 必 404）；
  webhook 事件词表补 gitcode（pull_request+note）/tencent_code（merge_request+note）
- 防漂移门禁三枚：config↔provider.RegisteredPlatforms() 双向相等（SDK 增删平台即红）；
  bootstrap 事件词表覆盖全集；mcpserver platformVocab↔SupportedPlatforms
  （mcpserver 生产面零 internal 依赖不变，一致性由测试钉）
- 文档口径统一：README（快速开始/env 表补全后缀/路由表 /webhook/{platform}）、
  docs/index.html（修死变量 ARGUS_LLM_BASE_URL→OPENAI_BASE_URL，平台示例多平台同构）、
  architecture-deep-dive、skills/argus-mcp/tools.md、deploy 模板三件、ops-runbook 速查表
- setup-webhook.sh 回调路径段参数化（ARGUS_PLATFORM，默认 gitea）
- gitcode/工蜂为 SDK 支持、Argus 未实测档（文档已标注）；make test 全量 -race 绿 +
  vet/fmt/arch 门禁绿

## [v6.4.0] — 2026-11-11

### 修复(biz)：修合并错解——task_service.go import 排序回归 goimports 规范态

e8b6113 合并把该文件解成了 theirs 未排序 import 态（根因：amend 并行会话提交
致历史分叉、三方合并基变成 f908400，rename 配对后内容侧悄悄取了 theirs）；
idl-drift CI 门禁首跑即红抓获（正是该门禁设计目的）。恢复 c9052f4 已验证态
（goimports 排序，make idl-gen 幂等输出），其余合并解法盘点无恙：qa/* 取
theirs=e6bdf77 终版正确（我 c9052f4 内的 qa 改动系并行会话在途快照被 git add -A 扫入）。

### 修复(all)：v6.3.0 外部评估修复批——P0 迁移锁并发缺陷实锤修复 + P1/P2 门禁与口径 + 依赖配套收编

外部技术评估（GitHub main 快照 7.6/10）逐项处置：
【P0 多副本迁移锁失效——静态推导实锤的真并发缺陷】
ensureMigrationApplied 租约 holder 全实例相同（"schema_migration:"+version），
TryAcquire 的 `holder = ? OR expires_at < ?` 让后启动实例直接把锁"续约"抢走——
双实例并发首启同跑重建型迁移（DROP+CREATE 互相踩/数据不一致）。修复三面：
① lease key 固定、holder = instanceID()+":migration:"+version（RunnerID 未装配
时进程内惰性 uuid 兜底）；② 抢锁失败不再静默返回 0（旧语义误以为已应用带病
服务），改让位轮询等 marker（2s 间隔，上界 TTL+2min），超时 fail-closed 拒启动；
③ 执行期每 TTL/10 续约防第三实例闯入。新增 TestEnsureMigrationAppliedConcurrent
双 Store 并发回归（fn 恰好一次，-race 绿）；续约 goroutine 收编 async.GoSafe。
【P1 完备性裁判缺省翻转 annotate→demote】
评估：部分插件失败时 annotate 仍可 APPROVE 放行合并——合并门禁必须 fail-closed。
ARGUS_COMPLETENESS_POLICY 缺省与未知值回退均改 demote（拼错配置从严不误放）；
显式 annotate 可回退旧行为。e2e 栈直构 runner 不经 config 不受影响。
【P1 LICENSE 补齐】仓库根新增 Apache-2.0（与开源仓同许可证，此前内部主仓
缺失，GitHub 无法识别授权条款）。
【P2 架构门禁盲区】TestLayering 白名单外包直接 continue（新包零约束逃逸）——
翻转后未归类包必须显式声明分层位置；bootstrap 组合根例外（TestBootstrapNot
Imported 单独守卫）。42 包全部归类。SECURITY.md master→main。
【v6.3.0 IDL 收敛收官（与 b07f883 同版本）】handler 响应壳收敛 respond.go 单源
（bind/reply/replyMsg，六处直线尾应用）；Makefile idl-gen（hz update + goimports
收敛——hz 每次 update 机械重写 handler import 可能悬空，幂等已实测）+ CI
idl-drift job（钉版 hz v0.9.8 再生成 + git diff 断言，生成物手改失真类永久关闭）；
e2e auth 覆盖探针（正则解析 idl/*.thrift 路由清单单源，无 token 打 17 条 🔒 路由
断言 401——v5.6.2 匿名失守类从此是 CI 门禁）；design.md §5 重写（拆分形态 +
hz 只认顶层 service 实锤 + 双门禁）。
【依赖配套修复（并行升级批收编）】oekit/pkg v0.44.0 后 oekit/data v0.20.11
cache/shell.go re-export 悬空（src.Option undefined）全门禁挂死——补升
oekit/data v0.21.0（连带 gormx v0.11.13）；本轮 10 项上游升级在本仓消费面
零 BREAKING（39 包全绿背书）。另：剔除误入 b07f883 的探针垃圾文件
（-idl/= 错路径产物，已 amend）。
门禁：make lint + 全量 test 39 包 + e2e（含 TestAuthzProbe）+ arch + P0 race 全绿

### 重构(deps)：agent-harness → goagent-kernel module 更名替换

上游 module 更名续作（goagent-kernel 与 agent-harness 版本序列一一对应、代码同源）：
- go.mod：github.com/yi-nology/agent-harness v0.30.0 → github.com/yi-nology/goagent-kernel
  v0.30.1-0.20261111030508-dfc51305f58a（pseudo-version 钉 HEAD）——⚠️ tag 面暂不可用：
  v0.30.0 及以下全部 tag 的 go.mod 仍声明旧路径 module github.com/yi-nology/agent-harness，
  Go 工具链校验拒绝（module declares its path as ... but was required as ...，实测）；
  HEAD 已改声明但未发 tag，待上游发版（如 v0.31.0）后一行升级钉版
- import 重写 5 文件（qa/budget/qa/ptc/ptc_test + workflow/requirement/r4ckpt）：
  四消费包 budget/checkpoint/ptc/toolname API 面零变化（签名抽查 Describe/Run/Normalize 在位）
- 注释历史指代追平（3 处 agent-harness/spill 等 → 今 goagent-kernel + 改名溯源标注）
门禁：build + make lint + 全量 test 44 包 + arch 全绿

### 重构(all)：v6.3.0 深度重构批——oagentkit/oekit 全家桶升级+架构卫生清偿——①P0 安全：acpx buildPrompt 手工围栏直拼 diff/黑板改走 DataFence（注入中和测试钉死伪围栏/伪数据区头）②收编：fixverifier 围栏 23 行→guardx FSFence（悬空软链拒判更强）、procx.Run→RunDetailed（截断事实结构化，ocrshim fail-closed）、fence_norm 退役回归实测 2 形态退化按勿硬删保留③架构卫生 8 项：arch 白名单清 3 幽灵 4 死条目+盲区 4 包补边、10 包包文档统一 doc.go（86 文件注释搬移恰一文件）、ARCHITECTURE.md 依赖表 41 包整表重写、死导出 4 删、fixverifier 吞错留痕④超长函数现状表 18 个（新债 13 集中 impactview/r3tools）入台账。全家桶 8 条 BREAKING 逐条核销仅 1 良性命中。全量+arch+lint+eval 四门禁绿

- 8a403c0

- 杂务(deps)：goagent-kernel 钉版 v0.31.0（替代 pseudo-version）

上游 v0.31.0 发版落地（module 声明已修正为 github.com/yi-nology/goagent-kernel），
从 v0.30.1-0.20261111030508-dfc51305f58a pseudo-version 升级钉版——7017742 留的
「待上游发版后一行升级」待办销账。顺带 go 工具链 1.27.3→1.27.4（上游 go.mod 要求）。
门禁：build + lint + 全量 test 44 包 + arch 全绿（API 零变化）

- 杂务(hygiene)：P2 架构卫生清扫 7 项收尾——四处文件头注释补空行（go doc 包注释归 doc.go 防拼接：bridge/catalog/assemble/a2a）；toolpool 层号 L5→L3 对齐 ARCHITECTURE.md；arch_test「门禁红至今」过时措辞订正；deep-dive internal/context→argusctx 旧路径；main.go GoSafe 表述准确化（copilot CLI 1 处豁免注明）；store_task 两处 json.Unmarshal 吞错留痕注释（v5.4.3 先例）；CONTRIBUTING 补代码组织惯例（service 双命名+包注释恰一文件）。含 ScopedHeartbeater 补实现随并行汇流入库（TouchHeartbeatsFor 三重守卫+僵尸复位因果链测试）。全量+arch 绿

## [v6.3.1] — 2026-11-11

### 新增(oss)：GitHub Actions CI/CD + 开源宣传页——ci.yml 三 job（ci=build/vet/test/arch → image=buildx 双平台推 ghcr.io/yi-nology/argus，main→latest+sha、tag v*→版本号 → release=交叉编译 amd64/arm64 二进制+sha256+docs/releases 说明）；pages.yml=docs/index.html 发布 GitHub Pages（upload/deploy-pages 标准组合，paths 过滤只发 index）；docs/index.html 单文件零外链 landing page（Hero/六步流水线/能力卡片/审查示例/23 工具 chips/快速开始，深浅双主题跟随系统）。权限最小化+并发防重入；Dockerfile 完整 multi-stage 形态直建（ssh 挂载实测空挂载兼容 CI）

- e6a13e8

### 新增(review)：v6.1.5 审查进度可见化+结论 TL;DR 先行（用户诉求：MR 上先看结论再看详细）——执行期 start marker 变活进度条：OnStage 闭包原地更新「进度：R1 需求解析 ✅ · R3 完成度判定 ⏳ · …」（StageTimeline 全词表+R1a 最长前缀回退，锚点复用零改动，失败仅 Warn），完成/失败三态文案优先级保持；完成时结论评论插入 TL;DR 区：verdict 行+意见分级计数（high/medium/low+行内已标注数）+重点关注 top3（Rank 降序保报告序，摘要 fence.EscapeUntrusted 注入中和），无意见单行「✅ 未发现问题」——评论时序天然「简洁结论→详细报告置底」。TLDR/进度条构造函数纯函数可测，11 个新用例

- f8bdaf6

### 新增(impact)：v6.1.4 repo map 小仓文件级展开——用户复访反馈「单包仓地图就一个孤点=视觉空数据」：目录节点 ≤3 且 Go 文件 ≤24 的仓，文件节点入图（file: 路径 KindFile，contains 边=包→文件，词表只加不改），Detail=导出符号清单（全量解析 cap5，小仓毫秒级）——单包仓从「一个孤点」变成「包+文件+符号」的可用地图；大仓不展开（目录级才是地形图）。地图页图例分语境参数化（ViewOpts.RepoMode：受影响文件/波及→文件/依赖关系词，影响面页文案不变）。用户报的 argus-validate 页将有 根包+validator.go(Validate()) 可视内容

- cf8bb15

### 新增(review)：v6.1.0 R3 search_sem——语义检索进审查面，图演五连收官——runner 建图索引随 ARGUS_SEMSEARCH 门注入 embedder（未开保持纯图零成本，与 assemble.go 同款判定式），Index.Searchable() 观测面；R3/R4 挂 search_sem 工具（topK 钳 5/10、单条 600 rune 截断、命中附调用邻接行——审查 agent 直接吃到 v5.9.0 图增强红利），无命中提示文本非 error、错误上抛（环境面故障可感知），toolprior PriorityCore；邻接 Rel 词表 requirement 包内本地单点（v5.11.0 纪律）；含 v5.4.6 并行补记账与本批 tech-debt 台账压缩记账（v5.5.x→v6.1.0 图谱与图面大版本批全段，此前台账停在 v5.4.6 欠账一个半月）。全量+arch 绿

- e16e20f

### 修复(impact)：v6.1.3 repo map 孤岛目录兜底——BuildRepoMap 只在有跨目录依赖边时建节点，单包仓/无跨包 import 的仓整图空 → 分享页 502「无可视化节点（空图）」（用户实报 demo-user/argus-validate 分享链接死链；每个被审查的单包仓都产出此死链）。修复=有 Go 文件的目录（fileCount+testFiles）cap 感知兜底入图（单包仓→根节点「(根)」、无依赖子包→孤岛节点），真零 Go 仓维持空图+注明文案。回归钉子：根节点+孤岛子包+零边三断言

- d654154

### 修复(semsearch)：v6.1.2 ByteBatchEmbedder 字节预算分批——产线网关 /v1/embeddings 路由有请求体大小分流（实测 9KB 过/12KB 起 404 误导性「model does not exist」=超限被 APISIX 分流到兜底路由；tcpdump 实锤 argus 10×2000rune≈21KB 恒死、直调小请求全绿、pod 内同网 wget 亦绿=纯 body 大小问题）。ByteBatchEmbedder 按 8KB 预算切子批（每段≥1 条、超预算大段独占、段序/向量序对齐，clipText 2000rune≤6KB 恒可独占），bootstrap 与 runner 两装配口同 wrap（memorykernel 单文本查询天然在预算内不 wrap）。批形钉子测试。v6.1.1 scanDir 修复后该限浮出——SkipAll 与字节限两层叠加把 search_sem 压死了一个月

- 219b19b

### 修复(semsearch)：v6.1.1 热修 scanDir SkipAll 中止整树——排除目录剪枝误用 filepath.SkipAll（应 SkipDir 只跳该目录）：真实仓库必有 vendor/testdata/.git 之类，走序命中即中止 → 索引恒空 → search_sem 自 v3.126.0（2026-10-09）起在所有真实仓库静默失效；codesearch 用 SkipDir 故 search_code 一直正常（产线对照实锤）。单测 fixture 系裸 TempDir 无 .git/vendor 形态未复现——产线实弹验收（search_code 有命中 vs search_sem 无命中的直接矛盾）抓现形。回归钉子：vendor 词序夹在真实文件之间断言两侧文件都被扫到且 vendor 内容不入索引。产线实弹教训：工具「注册成功」≠「数据面工作」，验收必须打到真实数据

- 0654342

### 重构(qa)：ptc 诊断渲染收编 harness Result.Describe 单源（agent-harness v0.27.1→v0.30.0，上游 #68）

- 删宿主手写 describePtcFailure/describePos（-25 行）：修复回喂环改调 ptc.Result.Describe()，诊断措辞与 harness 归因口径单源；归因链摘要段随升级白得
- 本侧只保留策略面 programFixable（判定逐字不变）
- TestDescribePtcFailure → TestPtcFeedbackViaHarnessDescribe：钉回喂契约，渲染形态归 harness 侧用例
- go.mod：agent-harness v0.30.0（带入其依赖地板 goja/regexp2/x/* 上移 + eino v0.9.23）
- 门禁：build/vet/staticcheck/gofmt/arch/全量 -race 绿；govulncheck 报 mcp-go v1.5.0 GO-2026-6911（Fixed in: N/A，main 上已在非本轮引入，无修复版——留档勿重探）

### 重构(all)：v6.3.0 深度重构批——oagentkit/oekit 全家桶升级+架构卫生清偿——①P0 安全：acpx buildPrompt 手工围栏直拼 diff/黑板改走 DataFence（注入中和测试钉死伪围栏/伪数据区头）②收编：fixverifier 围栏 23 行→guardx FSFence（悬空软链拒判更强）、procx.Run→RunDetailed（截断事实结构化，ocrshim fail-closed）、fence_norm 退役回归实测 2 形态退化按勿硬删保留③架构卫生 8 项：arch 白名单清 3 幽灵 4 死条目+盲区 4 包补边、10 包包文档统一 doc.go（86 文件注释搬移恰一文件）、ARCHITECTURE.md 依赖表 41 包整表重写、死导出 4 删、fixverifier 吞错留痕④超长函数现状表 18 个（新债 13 集中 impactview/r3tools）入台账。全家桶 8 条 BREAKING 逐条核销仅 1 良性命中。全量+arch+lint+eval 四门禁绿

- b07f883

### 重构：storage 方言收敛批 + internal/context→argusctx 改名（v6.2.0）

代码结构评估后的定向清理（目录结构评估健康不动）。除两处标注行为面外零行为变化：
- Dialect 单源收敛（dialect.go）：Store.sqlite/mysql 双 bool + isSQLite(tx)
  三源判定 → 单一 Dialect 枚举 + isSQLite()/isMySQL()，写门/行锁/迁移 DDL
  分流全看一个字段
- isDuplicateKeyErr 归位 dialect.go 并补 pg 23505（⚠️行为修复：生产方言 pg
  撞唯一键此前被当普通错误上抛——MarkCommentDelivery 幂等命中误报失败、
  CreateTaskForWebhook/DirectIfIdle 的 duplicate 回退分支永不触发）
- GetTaskByEventID 补 ErrNotFound 哨兵（⚠️契约修复：此前裸透 gorm 错误，
  违反「ORM 错误语义不出 storage 包」）+ TestGetTaskByEventIDNotFound 钉契约
- writeLock/defer writeUnlock 30 处收编 withWrite(fn)：忘 defer 解锁从人肉
  纪律问题变成语法上不可能；锁覆盖范围零变化
- store.go 拆分：模型定义→models.go，Get/UpsertReviewTarget 归位
  reviewtarget.go；store.go 407→254 行
- internal/context→internal/argusctx：包名与 stdlib context 撞名，11 处
  导入方 argusctx 别名税清零；波及 17 go 文件 + arch 白名单 + ARCHITECTURE.md
- isDuplicateKeyErr 测试钉三方言判定（mysql 1062/pg 23505/sqlite 文案/包装穿透）
go.mod：pgx/v5 转正直接依赖（pgconn.PgError 判定用）。全量测试（含 arch
守卫+e2e）两轮绿；gofmt/vet 干净（router*.go 生成面既有豁免不动）。

### 重构(mod)：module 路径 argus→github.com/yi-nology/argus——对齐开源仓地址，外部可 go get 引用；197 文件 479 处 import 全量重写（含生成面 biz/ 与 docs 示例），replace 指令均为外部依赖不受影响；全量+arch 绿

- 6f538d9

- 杂务(deps)：oagentkit/oekit 全家桶批量升级至最新——oagentkit 9 模块（agent v0.14.8/guardx v0.24.1/mcpkit v0.12.8/obs v0.9.15/queue v0.11.1/rag v0.11.7/skillkit v0.10.4/toolgate v0.17.0/tools v0.14.3）+oekit 4 模块（bootstrap v0.7.5/data v0.20.11/gormx v0.11.11/middleware v0.16.2）；零代码适配（BREAKING 面待收编调研逐一核销）；全量+arch+lint 绿。附：agent-harness issue #68 已提（ptc Result.Describe() 导出）
- 杂务(deps)：agent-harness 真实升级 v0.27.0→v0.27.1（前一提交 message 谎报——升级改动被并行会话 v6.2.0 的 go.mod 状态覆盖回退，线上回读抓获）
- 杂务(deps)：agent-harness v0.27.0→v0.27.1 + 深度简化复核入档——七修（#53-#59）零影响；全仓收编面复核「真重叠 5 处全收敛」仍成立，可净删≈0（诚实降级评估）；不做三项留痕勿重探（spill 语义不同/drain 语义相反/去重映射错位）；存疑一项=ptc 诊断渲染 ~25 行等上游导出 Result.Describe()（issue 已提 P3）；全量测试绿
- 杂务(deps)：v6.1.6 go-git-platform v0.121.0→v0.124.0（跨三版零代码适配）——v0.122 上游提案八连回流+三件套收编、v0.123 oekit 三件套被动 bugfix 轮、v0.124 契约测试矩阵扩展；传递升级 concurrency v0.31.2/core v0.13.6/logx v0.9.1/pkg v0.41.1；全量+arch 绿
- 杂务(gitignore)：本地构建产物 /dbcopy 与 /bin/ 入忽略——迁移日遗留的根目录二进制不再污染 git status

- 文档(ops)：v6.1.6 服务文档追平——根 README 版本块 v3.91.0→v6.1.6（k3s 在役/23 工具/进度条+TLDR/GitHub 开源镜像链接）；deploy/k8s43 README：形态单副本→双副本实况、镜像行改换芯层叠+Mac 直连链、升级行指向 swap-deploy.sh、新增 v6.x 运维备忘（网关 embedding body≥10KB 分流 404 误导报错/PRECISE GO-ABSENT/webhook 链路保持 215/search_sem 两层失效教训）

- ops(deploy)：k8s43 迁移清单入库——11-05 迁移日手挂面补入环境即代码：00-ns/20-pvc(argus-data 20Gi local-path)/40-staging2(数据恢复一次性 pod)/60-service(NodePort 30899)/80-pg-backup(3:30 CronJob+5Gi PVC)/Dockerfile.ocr+ocr-config.json(ocrshim 内部网关配置)。环境实况与清单对齐复核：在役 v6.1.0 双副本同 pin master01（local-path 不跨节点）、webhook 入站经 215 前门实测在跑（webhook.skipped 过滤日志实锤）、pg 备份 CronJob Completed 正常、ocrshim 层随换芯延续。PUBLIC_URL 保持 215 前门不动（直连探针无定论+现链路健康，翻转向无收益有风险）

- 格式：gofmt 补格式化 4 文件（CI Lint job 二轮抓获——filter-repo 历史重写后 import 对齐漂移；biz/ 生成面豁免不动）
- 格式：gofmt 补格式化 7 文件（make lint 门禁抓获——v6.1.5 子代理产出与 python 改写留下的格式漂移，开源仓 CI Lint job 首跑即挂实锤；教训=gofmt -l 恒 0 退出码断言姿势必须查输出）
- 格式(semsearch)：v6.1.2 补 gofmt（上一批 -l 输出被退出码掩盖，断言姿势修正为 -l 非空即失败姿势留待后批统一）

- 测试(storage)：CI 矩阵补 PostgreSQL（用户点破：43 生产自 v5.4.8 在役 pg，测试却只测 MySQL——生产方言零覆盖）——setupTestPG 同构独立库（pgx 管理面/DROP IF EXISTS 幂等/同测试名缓存复用=fencing 跨实例测试多 Store 打同库的正确语义）；test.yml 加 test-postgres job（postgres:16-alpine 与生产同款）；**pg 实弹揪出生产代码真 bug：RestorePrint 3 处 int 绑 text 列（pgx 严格类型拒隐式转换，MySQL/sqlite 宽松转换掩盖了一个多月——args[3] encode error 实锤）**；测试面 DDL datetime→timestamp（pg 无 datetime）+库名小写归一+DDL 引号钉大小写（pg 裸标识符折叠）。三方言实测：pg 8.5s/MySQL 28.9s/SQLite 0.6s 全绿
- 测试(storage)：MySQL 方言兼容三修（开源 CI MySQL 矩阵首跑暴露的既有红，v3.67 加矩阵后从未真跑）——①setupTestMySQL CREATE 前 DROP IF EXISTS（64 字符库名截断撞名/残留重跑幂等化）②SameSHAGuard 测试按方言分支（部分唯一索引=SQLite 专有，MySQL 分支恒 (true,nil) 占位是 samesha_guard.go:34 设计在案——跨实例索引断言仅 sqlite，MySQL 断占位语义）③FindingPrint Create 补 FirstSeenAt/LastSeenAt（MySQL 严格模式拒 '0000-00-00' 零值 datetime）。双方言实测：MySQL 8.0 全包 ok + SQLite 回归 ok

## [v6.0.0] — 2026-11-10

### 新增(impact)：v6.0.0 影响面 L2 喂消歧符号图——图优先逐符号回退：触达符号经 NodeKey(dirOf(File),Recv,Name) 精确对接图节点，SymbolContext ok 即取消歧调用方（同名 New 各归各），ok=false 回退 codeintel 词法（预算截断/非 Go 场景接住）；semsearch SymbolContext in/out 补定义位置（refAt，loc 缺失空串=Path 同语义），impactview 侧空 Path 调用方跳过不建空 ID 节点（悬挂边不变量不破）；Stats.CallerSource 四档词表（graph_precise/graph_lexical/codeintel/mixed）+Notes 注明「N 经图 M 回退」；displayOf 按 dir 前缀精确剥离（含斜杠 dir 键安全）；runner 透传 wf.Graph。图演四连收官：影响面图（PR 评论人眼消费面）告别同名混入。全量+arch 绿

- 1ae5684

### 新增(review)：v5.11.0 图面进审查管线——R3/R4 审查 agent 用上消歧符号图：semsearch 纯图模式（NewIndex(nil) 合法，chunk/边/图照建、embed 跳过、Search 单源拦截报错，图查询零 embedding 依赖），Runner injectCodeGraph 同目录再建图索引（codeintel 失败不再 early-return=两索引互不阻断），Workflow.Graph 注入，R3/R4 新增 get_trace（两符号 ≤4 跳调用链，qa.Trace 同款视觉）+get_symbol_context（定义+调用方+被调全景，cands>1 显式透出合并），边界声明随 Precise() 动态（词法级↔类型解析级），toolprior 两入口 PriorityCore；arch 白名单补 requirement/runner→semsearch。审查面从此告别同名混入（v5.10.0 消歧红利直达评审报告）。全量+arch 绿

- 817d469

### 新增(semsearch)：v5.10.0 图面真调用边翻案落地（档一+档二）——档一：节点身份迁 dir 限定键（dir.Base/dir.Recv.Base，跨包同名 New 各自成节点，裸调用同目录优先=Go 语义正解），同次解析白捡 import 表消歧（pkg.F 经 go.mod 剥前缀解析到目标包）+同接收者兄弟方法（s.Bar→T.Bar），qualified 目标缺失降 bare 回退保召回，SymbolContext 透出同名候选数；档二：EdgeAugmenter 钩子（FNV 签名门控+失败闩 per Index=新 MR 自动重试）+internal/goedges go/packages 精确抽取器（typeutil.Callee types 级解析，接口分派解到接口方法声明由存在性过滤丢弃=宁缺勿假边，泛型/别名接收者剥基名，任一包带 Errors 整体降级词法，MaxFiles 3000 成本闸），ARGUS_GRAPH_PRECISE opt-in 装配注入（需工作副本可编译+依赖可解析 vendor/内部 GOPROXY，不可编译自动降级词法——43 部署前先确认 Go 模块依赖解析通路），qa/mcpserver 措辞随 Precise(dir) 动态化（词法级↔类型解析级），x/tools v0.52.0，arch 白名单补录 goedges→semsearch。全量+arch 绿

- bececa8

### 新增(semsearch)：v5.9.0 语义检索图增强（Graph RAG 设计借鉴 GitNexus，不引其代码——PolyForm Noncommercial 不可商用）——chunk 抽取的同一次 AST 解析顺带抽 FuncDecl 体 CallExpr 赤名调用边（闭包归外层函数），Refresh 收尾重建符号邻接图（callee 限定名末段归一到仓内符号表，stdlib/未解析剪掉；adj/radj/loc/bare 恒排序去重，遍历序确定即输出确定）；search_sem 命中自动附「调用方/被调」邻接行（单侧封顶 4，渲染侧 3+3）；新增 MCP get_trace（两符号 ≤4 跳最短调用链，无向多源 BFS，同名多候选合并种子/目标）与 get_symbol_context（单符号定义+调用方+被调，同名合并邻接）——两工具 ARGUS_SEMSEARCH 双门同 search_sem，core 未实现接口不注册死工具；tools.md/SKILL.md/README 契约面同步 21→23 工具。词法级边界声明同 get_callers 纪律（接口分派/方法值不可见，引用前须读代码核实）。全量+arch 绿

- 1ed5337

### 修复(ops)：swap-deploy 上传非 201 不再静默放行(curl 000 曾误报 DEPLOY_DONE)

- d88636f

- 文档(release)：v6.0.0 发布说明——图面/真调用边大版本，图演四连（v5.9.0 MCP 外部 AI 面→v5.10.0 消歧底座+go/types 精确边→v5.11.0 审查管线→v6.0.0 影响面图），同名假边根治+边质量可感知+零 embedding 图查询；部署与 v5.8.2 共用 VPN 窗口，ARGUS_GRAPH_PRECISE 前提=43 Go 模块依赖通路实弹验证

## [v5.8.2] — 2026-11-09

### 新增(impact)：v5.8.2 惯犯文件热力——prints 跨 MR 意见史挂图:storage.FindingHistoryByFiles(DB 侧 GROUP BY 聚合 total/suppressed,IN 参数化);ViewOpts.History(storage.FindingFileHistory 聚合结果类型,arch 白名单补录);historyJSON 瘦身仅收 total≥3 惯犯(低频防噪);侧栏「历史档案」(总意见数+人工抑制计数=误报倾向参考,审阅降权依据);图例补说明. 该文件历史上被提过多少次意见/几次被压——评审权重的一手参考. 全量+arch 绿

- f55a03f

- ops(deploy)：v5.8.1 事故修复+跳板部署链固化入仓——事故复盘:v5.8.1 首次部署走 curl 拉取(替代 scp),拉取文件落 644 无执行位,COPY 进镜像后 uid1000 exec permission denied → CrashLoop 3h+(评审服务停机,MR 评审中断);三重根因=①curl 拉取无 x 位(根治:COPY --chmod=0755+镜像内探针 test -x 才 set image) ②ctr 同 tag 重导 kubelet 不重解析(新 tag+强删 pod) ③后台自动循环与人工部署竞争(时序竞争曾推坏镜像). swap-deploy.sh 固化入仓:大小+ELF magic 双校验→build→镜像内可执行探针→set image→rollout→中转清理全程断言. 事故期间 43 nc 管道速率降至 0.5MB/s(夜深拥塞)

## [v5.8.1] — 2026-11-09

### 新增(impact)：v5.8.1 影响面演化对比——同 MR 上一轮快照集合差上图:CompareSnapshots 纯函数(节点 ID/边三元组/函数级变更 file:name 三口径集合差+风险前后值+Prev/Cur 计数);service.prevImpactCompare(ListTasksByPR 取上一 done 任务有快照者,无上一轮/无快照/解码失败一律 nil 不阻断主图);交互图页头演化摘要块(风险分涨跌 ↑红↓绿+节点/边前后计数+可展开明细:新增/移除波及、新增/已恢复函数级变更、边变化,零差异显式说明);MCP get_impact 数据面同步带 compare 字段(外部 AI 消费);ViewOpts 收口渲染签名(findings/compare,三入口零膨胀);侧栏意见 fp 短码点击复制 fix 指令(看图→发起修复闭环). 全量+arch 绿

- 4202d96

## [v5.8.0] — 2026-11-09

### 新增(impact)：v5.8.0 审查意见挂图——findings 与影响拓扑在图上互查:任务 ResultJSON 的 findings(service 层 taskFindings 单源解码)传入渲染面,按文件聚合为节点右上角红点计数徽标(svg 静态渲染)+侧栏意见列表(severity 图标/行号/fp 短码/摘要,完整建议指回报告);findingsJSON 瘦身子集嵌入独立 script 块(空则整块省略);图例补徽标说明;RenderHTMLWithFindings 新签名(RenderHTML/RepoHTML 传 nil);arch impactview→plugin 白名单补录(findingswire 同向). 测试:徽标坐标/数据块存在性/空块省略三断言. 全量+arch 绿

- 4fcaf81

## [v5.7.4] — 2026-11-09

### 新增(impact)：v5.7.4 图谱深度迭代二批——函数级变更一等节点融入主图:视图层合成 FuncDiff 节点(kind=funcdiff,状态三色)与 file→fd 归属边,JSON/布局/绘制三处同源一致(RenderHTML 视图副本 withFuncDiffNodes,mermaid/报告面用原 snapshot 零影响,所在文件不在改动集不合成,封顶 8);侧栏 funcdiff 节点点击渲染完整 BEFORE/AFTER 签名卡与调用增减;包聚合详情面板(Go 文件数/测试覆盖[无测试红字告警]/对外暴露/距改动跳数);包节点无测试覆盖标注「·无测试」(风险面 TestMissing 的图上直读形态);搜索扩 detail 字段. Snapshot 契约只加 kind 词表值. 全量+arch 绿

- 9b11988

## [v5.7.3] — 2026-11-09

### 新增(impact)：v5.7.3 图谱信息与关联深化——①HTML 侧栏融合关联信息:点选文件节点侧栏直接渲染该文件的函数级变更(状态徽标/签名同否/行数变化/调用增减 chips)与历史共变列表(条目可点击跳转聚焦) ②函数级 diff 卡片(fdcard)与主图联动:点击卡片→聚焦对应文件节点+开侧栏+滚动到画布 ③历史共变独立面板(≥MinCoupleCount 的对可点击聚焦,import 图外隐性耦合是评审最易漏的关联面) ④图例与交互说明面板(kind 色彩编码/边关系词表/交互指南) ⑤SQL 表节点专属紫色视觉编码(nodeFill 此前落 default 灰). Snapshot 契约零变更,纯渲染层融合(数据已全量内嵌 client). 全量+arch 绿

- 7e0fc32

## [v5.7.2] — 2026-11-08

- chore(deps)+fix(parse): v5.7.2 依赖再追平 26 模块(修票批发版)+fence 回归防御——oekit 12(pkg v0.38.0/concurrency v0.30.0/core v0.13.4/logx v0.8.11/hertz v0.10.7/middleware v0.13.15/bootstrap v0.6.16/data v0.20.7/kitx v0.4.3/metrics v0.1.17/tracing v0.3.7/gormx v0.11.7)+oagentkit 12(llm v0.21.5/guardx v0.23.4/agent v0.14.5/queue v0.10.4/runtime v0.12.4/tools v0.13.4/rag v0.11.4/skillkit v0.10.2/mcpkit v0.12.4/textx v0.10.4/toolgate v0.15.3/obs v0.9.13)+agent-harness v0.27.0+go-git-platform v0.121.0. 一处适配:async.PanicHandler 扩 (ctx,r,stack). **挖掘出消费面回归**:pkg v0.38.0 StripCodeFence 从正则收紧为行首扫描(嵌套围栏修复同批),「说明文字+同段 ```json」形态(agent 输出常态)剥不出→Unmarshal 空输入——plugin.NormalizeFence 五消费点统一防御(非行首开栏前补换行,幂等,code span 不误伤),acpx 红转绿. 全量+arch+vet 绿

## [v5.7.1] — 2026-11-08

### 修复(report)：v5.7.1 运行中断场景报告降噪——jianmu MR47 实弹:24-30 条 AC 全灭时逐条盖同款长文案刷屏,且「直接重跑任务即可」的断言在持续限流下被现实证伪(同任务错峰重跑两连灭,387 万 tokens 全在 429 上空转). ①AC Detail 收敛短后缀(死因在 Summary/completeness 建议里说一次) ②报告面新增顶部 banner(本轮 N/M 条判定被运行中断,收敛说明+错峰重跑指引)——RuntimeFailMark 上提 plugin 契约层,requirement/reportview 共用判定不反向依赖 ③completeness 全灭建议改「错峰重跑」中性措辞,不断言必然成功. 全量+arch 绿

- 9aabd14

## [v5.7.0] — 2026-11-08

- 杂务(deps)：v5.7.0 上游全量追平——25 模块升级:oekit 11(pkg v0.37.1/bootstrap v0.6.15/concurrency v0.28.1/core v0.13.1/data v0.20.3/hertz v0.10.2/kitx v0.4.2/logx v0.8.6/metrics v0.1.14/middleware v0.13.13/tracing v0.3.5)+oagentkit 12(agent v0.14.1/guardx v0.23/llm v0.21.1/mcpkit v0.12/obs v0.9.9/queue v0.10/rag v0.11/runtime v0.12/skillkit v0.10/textx v0.10/toolgate v0.15/tools v0.13)+agent-harness v0.26.0(11 个 minor 跳变)+go-git-platform v0.117.0+连带 gormx/registry/go-gitcode/go-gongfeng/genproto 三方. 编译+全量测试+arch+vet 零代码适配绿;grpc 半截缓存(下载被打断)purge 后恢复

## [v5.6.5] — 2026-11-08

### 新增(impact)：v5.6.5 词法图层扩 Rust/Java/Kotlin/C-C++——①Rust:mod 声明(带分号文件模块,inline 不入图)+use crate::(归属 crate 锚点=**/src/{lib,main}.rs 最长前缀,workspace 多 crate 各自映射)/super::/self:: 全路径与去尾段双候选(尾段可为类型);fn 进函数级 caller 检测与 funcdiff ②Java/Kotlin:import 点路径→文件(通配 .* 不入图;方法级刻意不做) ③C/C++:#include 双引号形态相对解析,系统头不入图 ④源根后缀索引:包路径相对源根而非仓库根(Maven src/main/java 与 Python src 布局同病)——段边界后缀 map O(1) 命中,绝对 py 模块需补 .py 候选(实弹两轮修正). TS 相对 import 已随 v5.6.3 覆盖. 全量+arch+vet 绿

- 66a957f

## [v5.6.4] — 2026-11-07

- fix+feat(impact): v5.6.4 funcdiff 扩 shell/py+repo map 鉴权失守热修——①函数级图 diff 扩展:shell/py 词法定义对齐(funcdiff_script.go,与 scriptgraph 同正则单源;Calls 出边刻意留空防误报;FuncSnap 增 BodyHash 指纹感知行数不变体变;FuncDiffs 三出口零改动生效) ②🔥安全热修:repo map 自 v5.6.2 起匿名开放——hz update 重生成把 _repomapMw 手改恢复成 stub 当时漏补,匿名探针实锤 200;热修=组合鉴权回落 Bearer+新增 repo-map 域 share 令牌(SetRepoMapShare,与任务域串互不通用) ③图谱互跳:影响图页头注入整仓地图链接(带 repo-map share,share 访客不落 401). 教训:hz update 后必须逐项核对 🔒 _Mw 挂载——门禁不覆盖手改 mw 的鉴权语义. 全量+arch 绿

## [v5.6.3] — 2026-11-07

### 新增(impact)：v5.6.3 图谱演进批——①Python/JS 词法图层:py 绝对/相对 import(def 进函数级 caller 检测)+JS/TS 相对 import(require/ESM,候选覆盖扩展名与 index,裸名不入图;函数级刻意不做防误报面失控),两处实弹修正=绝对模块点转路径后不再被斜杠检查误拒/ESM 的 from 在路径之后与 Python 相反 ②评论图 caller 边预算:词法层后单 MR sym→file 边可破 40 条(deploy-sql 实弹 45)逼近 4400 字符截断线——每符号≤3 总预算 24,纯函数不污染边集,图注注明剪裁数 ③仓库地图新鲜度:缓存命中经 ls-remote 探测远程 HEAD,失配删副本全量重建(探测失败按一致处理,宁可旧图不打死缓存). MR186 v5.6.2 端到端验收同批完成:报告 share 链接实测提取+插件 ocr,security 双产出+完成度 5/8. 全量+arch 绿

- 7dcd1bb

## [v5.6.2] — 2026-11-07

### 新增(impact)：v5.6.2 仓库级代码地图(图谱可视化 D 项)——整仓目录依赖图:Go 目录=包 import 聚合+shell source 聚合+SQL 表血缘目录对聚合(复用 Build/scanScripts 同纪律,封顶 MaxMapDirs/Edges+Notes);浅克隆+TTL 30min 进程缓存+in-flight 占位防并发双克隆+失败短因半程缓存;owner/repo 走 query(repo 常含子群斜杠,路径段不跨 /——hz 首版实弹 404 修正);出口=GET /repos/map(🔒 Bearer,自包含 HTML)+MCP get_repo_map(tools/list 无死工具模式);htmlTemplate 标题参数化双页头. 全量+arch 绿

- bce4d55

## [v5.6.1] — 2026-11-07

### 新增(impact)：v5.6.1 影响面 share 链接——MR 评审人免 API 令牌直开交互图:任务绑定 HMAC-SHA256(API master token, taskID) 稳定令牌(不过期,MR 评论存活数月,短时令牌会变死链;泄露面=单任务图谱页,其数据在评论中本已 mermaid 全量公开),_taskimpactMw 组合 ?share= 放行否则回落 Bearer;secret 空(鉴权关闭)机制整体不启用;报告链接自动拼 ?share=. 用户拍板:预算放至 5M(token 不再受限)+四提交已 push

- 64db94e

## [v5.6.0] — 2026-11-07

### 新增(impact)：v5.6.0 代码图谱可视化三件套——①多语言图层:非 Go 仓(shell/SQL)此前只有文件层孤岛(deploy-sql 实弹 4 红盒 0 边),新增词法图 source(含 $(dirname) 惯用法)/函数调用/SQL 表血缘(CREATE/INSERT/UPDATE/DELETE 写方→FROM/JOIN 读方,table 枢纽两跳),单遍反向索引防平方扫描,封顶+Notes 纪律,risk 增词法波及项 ②报告附交互图链接(PUBLIC_URL/tasks/:id/impact,措辞按 🔒 鉴权面写实) ③HTML 探索器增强:类型过滤 chips(file/symbol/table/package/test)/点选邻域聚焦(一跳高亮+dim 其余)/SVG 导出(样式内联单文件). 全量+arch 绿

- 7773ef4

## [v5.5.4] — 2026-11-07

### 修复(review)：v5.5.4 预算门适用域修正——R3 自救重试不再被已失控的预算拦死:门只在预算真实约束区间(Used≤2×Limit)生效,超支 2× 以上说明 ReAct 直调早已跑在记账节奏外(MR186 实弹 922k/160k),此时拒掉一次生成的重试省下的是零头、丢掉的是全部判定;门拦下时降级报告同样带诚实死因与调参路标(repo_settings.task_token_budget/ARGUS_TASK_TOKEN_BUDGET). 用例覆盖门内拦/门外放两种形态,arch+全量绿

- dd79e65

## [v5.5.3] — 2026-11-07

### 修复(review)：v5.5.3 运行中断不再洗白+ReAct 出口补重试环——MR186 实弹四不合理:①R3 首轮 429 秒杀整轮,降级报告却写「模型输出不可解析」并在完备性里建议「拆分 PR」(死因透传:限流/超时/迭代上限→「R3 运行中断…建议重跑」,completeness 对症三分支) ②ReAct 直调旁路(R3/Reviewer/QA/Router)无重试环,单发 429 即阵亡——新增 retryChatModel 装饰(wrapStage 最外层,分类复用 llm.ClassifyLLMError 单源,429 下限 5s 指数封顶 30s,Stream 仅建立即败重发) ③R1 把跨仓「配套/同一批发布」内容提成本 MR AC(env.example 在他仓,必 unverifiable)——prompt 收窄+ambiguous 注明他仓配套 ④预算裁剪插件不指旋钮——降级行注明 repo_settings.task_token_budget/ARGUS_TASK_TOKEN_BUDGET 可调. 测试+arch+全量绿

- 1e0da54

## [v5.5.2] — 2026-11-07

### 新增(deploy)：pg 双副本——跨节点流复制热备（worker01 standby，lag=0）+ failover 人工预案

- c79040f

### 新增(deps)：v5.5.1 上游 issue 修复收编批——8 模块追平 + 三件消费适配

11 枚上游 issue 全部 CLOSED，对应修复发版收编：
- textx v0.9.0（#27 Normalize 迁移警示 + Normalize3 兼容钩子——argus 已四档
  适配无需 Normalize3）
- llm v0.20.0（#58 Limiter 收窄最小 Waiter——TokenBucket 天然满足零改动）
- queue v0.7.0（#24/#25 sqllease+sqlqueue 三方言参考实现 + 契约硬化）
- tools v0.12.0（#36 DeepenOptions 参数化）
- go-git-platform v0.114.0（#33 DeleteIssueComment + #34 EnsureSecretMode）
- agent-memory v0.10.0（#13 Reindex Go API + #14 维度指引 + #15 FTS tokenize）
- guardx v0.19.0 / oekit/concurrency v0.25.1 联动
三件消费适配：
1. webhook ensure：EnsureSecretAlwaysUpdate 收编 secret 轮换 workaround——
   删除后置 UpdateWebhook 补写段（48 仓 × 每启动省一轮 API）；语义变化=
   created 分支不再补写（secret 随建生效），match 分支单次往返内重写
2. sdkctx DeleteIssueComment：IssueManager 主路（v0.114 七平台覆盖）+
   DiffManager.DeleteNote 兜底（旧地板兼容）
3. memorykernel：ARGUS_MEMORY_FTS_TOKENIZE（缺省 trigram）——中文词面召回轨
   复活（unicode61 不切 CJK）；存量库切换需重建 FTS 虚表（43 现网空库，
   删 kernel.db 重启即可）
测试基建：poster fakePlatform 显式实现 DeleteIssueComment（gp v0.114 后
nil 嵌入接口会以空方法集"满足"断言然后 SIGSEGV——fake 纪律=上游新接口
方法必须显式实现）；ensure 测试拆 created/match 两场景对齐上游语义。
评估后不收编（留档勿重探）：sqllease（50 行在役实现+DB 时钟切换有混用
窗口）；sqlqueue（表 schema 与 argus tasks 不匹配）；Service.Reindex
（Go API 已就位，argus atoms=0 无消费面，将来需要时直接接）。
39 包全绿。

### 新增(ha+deps)：v5.5.0 多副本控制面收口 + 四库 15 模块追平 + textx 四档适配

多副本（v5.4.9 收尾）：
- bootstrap：dream / memory-kernel-gc / health-report 三 ticker 加
  runOnceUnderLease 租约单飞门（per-tick TryAcquire 短租约，跑完即释放）
  ——dream 的知识库导出是写面（双跑双导出），gc/health 避免重复全表
  扫描与 IM 双发。至此全部「只能跑一份」面收口：poller 选主（既有机
  制）+ webhook ensure（v5.4.9）+ 迁移哨兵（v5.4.9）+ 本批三 ticker
- spill-prune 刻意不门控：work 根已 per-pod emptyDir，各自清各自的是正确语义
依赖追平（13+2 联动）：
- oagentkit：agent v0.12 / guardx v0.18 / llm v0.19 / mcpkit v0.10 /
  obs v0.9.8 / queue v0.6.6 / runtime v0.7.3 / textx v0.8.0 /
  toolgate v0.12.6 / tools v0.11.1
- yi-nology：agent-harness v0.15 / go-git-platform v0.113（七版本全
  refactor/test/docs，零功能缺口）
- 联动：oekit/concurrency v0.24 / oekit/pkg v0.34.1
两 BREAKING 适配（零语义漂移）：
- oekit/concurrency v0.24 ratelimit 接口去时钟参数：LLMLimiter 类型
  *rate.Limiter → ratelimit.Limiter，构造换 NewTokenBucketFloat（底层
  同为 x/time/rate，语义一致）；消费面仅 Wait(ctx) 不受影响
- go-git-platform v0.107 JobLog.Truncat→Truncated 拼写修正：内部字段
  同步改名（writing.CIJob）
textx v0.8.0 四档词表（#20）消费面适配（上游 issue #27 已附踩坑证据）：
- R5.2 裁决真值表：case High + Critical（critical 掉出 switch 计数=
  最严重问题被 R5.5 APPROVE，测试钉子抓出）
- .reviewagents.yaml min_severity 白名单加 critical + 文案更新
- domain.SeverityCritical 常量导出；7 处测试期望更新（domain/plugin/
  reporeview/ocrshim e2e/adapter_acpx/reflection）
39 包全绿。

### 新增(ha)：v5.4.9 多副本加固两件——迁移哨兵租约单飞 + webhook ensure 单飞门控

- storage/ensureMigrationApplied：leader_leases 租约互斥包住一次性迁移执行段
  （pg/MySQL 网络库多副本同时首启时，printalias PR 域迁移是 DROP+CREATE 重建型，
  并发执行互相踩）。拿不到锁让位返回（marker 未写，下次启动重试，语义与
  「失败不写 marker」一致）；拿到锁双检 marker 防「他实例已跑完」竞窗。
  sqlite 单实例无竞争，同一逻辑跨方言一致
- bootstrap/webhook ensure：启动期 48+ 仓 hook 校准加租约单飞（幂等但多副本
  全刷翻倍浪费 GitLab API，并发 PUT 同仓 hook 有平台侧竞态面）。拿不到=
  他副本持有，本次启动跳过（hook 已被校准；secret 轮换自愈由持有者重启覆盖）。
  锁面故障降级照常跑（幂等兜底），跑完 Release 让位
- 多副本其余面审计结论（无需迭代）：ClaimNextPending FOR UPDATE SKIP LOCKED
  为 pg 标准特性；心跳/stale 重置标准 SQL；samesha 防线=事务锁（与 MySQL
  同形态）；LeaderElector/poller 选主租约跨方言；memorykernel sqlite 为
  WAL+busy_timeout+单连接，同节点共享 PVC 多进程写安全（跨节点不可行——
  local-path 不跨节点，多副本须同节点 pin）

### 新增(storage)：v5.4.8 argus 切 PostgreSQL——Open 三态方言路由 + dbcopy 迁移工具

- Open 路由三态：postgres://|postgresql:// 前缀 → gorm postgres dialector
  （新）；mysql://|@tcp( → MySQL；否则 SQLite。Store 加 mysql 方言标志，
  upgradeMySQLLongText 收窄为 MySQL 专属（pg 不跑 information_schema MySQL
  探测），printalias PR 域迁移拆三方言 DDL 分支（pg: 双引号+timestamptz）
- cmd/dbcopy：sqlite→pg 一次性逐表全拷（目标须空库；schema_migrations 一并
  拷入，目标库哨兵视为已应用防 printalias 重建迁移清掉灌入数据；AutoMigrate
  建目标 schema；主键冲突即失败暴露不做静默去重）
- 冒烟：pg 空库 Open+AutoMigrate+两哨兵落库+读写全通；dbcopy 演练 3213 行
  全拷（tasks 951/finding_prints 1157/review_targets 727…）状态分布核验合理

### 新增(webhook-ensure)：v5.4.6 go-git-platform v0.106.0——#32 已上游修复，GitLab 评论链路 SDK 面闭环

- go-git-platform v0.101.0→v0.106.0（11 提交）：**#32（gitlab note 事件族缺口）
  已随 v0.101.1 上游修复**——Create/Update 双路补 note/comment/issue_comment 映射
  （含别名+显式 false 语义），argus v5.4.1 提的 issue 闭环；另收编 govulncheck
  归零（x/crypto 修复版+x/net v0.62）、CR 指派人写面（UpdateCROptions.Assignees
  七平台）、CI 失败诊断三平台（CILogManager）、部署/模板建仓/Fork 同步、注册表
  面接 oekit SafeMap；连带 gitlab client-go v3.43 / go-gitcode v0.14。零代码适配
- webhookEvents[gitlab] 补 note 词（merge_request+note）：ensure 建出的 GitLab
  webhook 从「只覆盖 PR 事件」升级为完整覆盖评论事件——GitLab 评论命令链路
  SDK 面闭环（此前需平台侧手工勾 note_events）；补词表钉子测试防回归
门禁：lint+arch+eval+全量测试零失败

### 新增(webhook-ensure+db)：v5.4.1 启动期 webhook 幂等确保 + tasks 预算复合索引

- webhook ensure（tech-debt 遗留项 3 落地，opt-in）：ARGUS_ENSURE_WEBHOOK=1 +
  ARGUS_PUBLIC_URL 时对 PollRepos 逐仓 provider.EnsureWebhook（不存在建、同 URL
  事件集漂移重建）；secret 每次启动经 UpdateWebhook 重断言——平台侧 write-only
  不可比对，Ensure 对已存在钩子报 unchanged，轮换自愈全靠这步（相对
  setup-webhook.sh 的增量价值）。事件词表按平台内置（gitea/forgejo=
  pull_request_only,issue_comment 试点实弹词；github/gitee 同形；gitlab 只能
  merge_request——note 族 SDK 面不可表达 ⇒ go-git-platform#32 已提），
  ARGUS_<P>_WEBHOOK_EVENTS 可覆盖。异步 60s 上界、失败只告警（poller 兜底
  不受影响）；3 用例（created+secret 补写/缺 secret 跳过/覆盖词表）
- tasks 表复合索引 idx_budget（platform,owner,repo,status,created_at）：
  RepoMonthlyTokens（预算闸每任务启动一次）/MonthlyTokensByRepo 此前靠 idx_pr
  前缀回表过滤，仓库任务量随月累积后每次预算闸扫全量历史；AutoMigrate 幂等建
  + schema.sql 同步；其余热路径已被既有索引覆盖（审计在案）
- docs/tech-debt.md：遗留项 3 销账 + v5.4.1 节
门禁：make lint 绿 + 全量测试零失败

### 新增(deps-recycle)：v5.4.0 深度迭代批——go-git-platform v0.101.0 + 条件请求默认开 + WithFilter 防御纵深

- 依赖：go-git-platform v0.100.0→v0.101.0（#31 ETag 304×LRU 驱逐竞态进程内自愈
  healETag304Miss——本仓所提 issue 闭环；#30 空 diff PR MergeCR 谎报 MERGED 修复；
  gitcode 按 live 探针收缩能力面）+ logx v0.8.1（测试幂等）+ pkg v0.31.2（#162
  Table 宽度修复——同为本仓所提）+ registry v0.5.2；go-gitcode 传递跳跃 v0.13.0。
  零代码适配，全量测试绿
- ConditionalRequests 默认开（v5.3.2 引入时默认关的顾虑=上游 #31 硬失败竞态，
  已修⇒顾虑解除）：四平台 ARGUS_<PLATFORM>_CONDITIONAL_REQUESTS 默认 true（=0
  显式关）；GitHub 304 不计限流预算，poller 每 tick 全量翻页直接受益，平台不支持
  ETag 时透明回退 200
- logx.WithFilter 防御纵深（v0.8.0 原语）：main.go 两处日志装配口接 FilterKey
  （token/secret/authorization/api_key/apikey/password/access_token）——logredact
  只护 error 字符串，本层补结构化 kv 面（v3.9.0 凭证泄漏前科的纵深兜底）
- 手写面→oekit 映射存档（评估后不换，tech-debt.md 在案勿重探）：月度预算闸是
  DB 实数记账非 budget.Ledger 对口；float64 断言链形态已正确；contextx/分页/
  singleflight/kitx 无匹配场景
门禁：make lint 绿 + 全量测试零失败

### 新增(copilot+sdkctx)：v5.3.3 cli.App 收编 + LoadRepoConfigAuto 收口 + 上游 issue 两枚

argus 侧（收编 v5.3.1 升级带来的 pkg/cli v0.31.0 能力）：
- argus-copilot 分发/usage 收编 pkg cli.App：手写 switch 分发+33 行 usage 删除，
  退出码 0/1/2 单源 cli 常量（4=空 diff/130=信号 仍自定义）；每命令独立
  FlagSet——假 flag 不再被静默接受而是用法错误（行为收紧）；help/--help 退出 0
  走 stdout（此前问帮助是 2）；懒连接：用法/帮助路径不要求 argus 在位。全局
  -url/-token/-timeout 仍在子命令前，调用 surface 兼容；冒烟过全部退出码路径
- sdkctx 新增 ResolveBranch（5min TTL 默认分支缓存，显式分支零调用短路）+
  LoadRepoConfigAuto 单步入口，收口 runner/workflow/describe/agents 四处
  「取默认分支→读 .reviewagents.yaml」两步模式；describe/agents 是评论/HTTP
  请求路径，此前每请求真打一次 GetRepo；runner/workflow 保持 pr.DefaultBranch
  回填语义；2 用例（缓存命中/显式短路/解析失败上抛）
- docs/tech-debt.md 记 v5.3.3 批 + 上游 issue 两枚（v5.3.2 遗留 1/2 销账）：
  go-git-platform#31 ETagCache 304+LRU 驱逐竞态硬失败；
  oekit/pkg#162 cli.Table 状态符号宽度计 1 列致表格错位（附复现）
门禁：make lint（vet/staticcheck/gofmt）绿 + 全量测试零失败

### 新增(comment)：v5.3.0 评论命令面商业化抛光——help/status/👀即时回执/协作者门控/冷却感知/changelog

- help/帮助 与 status/进度 指令（cmdCatalog 目录单源：手册渲染/门控判定/冷却提示
  共用一张表，help 不漏项；info 类免节流永远可达）；status 读 ListTasksByPR 最近
  轮状态/结论/意见数 + 下一步引导（storage 新查询）
- 即时回执 👀：指令评论受理即打表情（SDKCtx 补 AddIssueCommentReaction/
  ListCollaborators 门面，ReactionManager/CollaboratorManager 能力断言，缺失静默降级）；
  评论 ID 随 commentMeta 归一（canonical/GitLab note/GitHub issue_comment 三形状）
- 烧预算指令权限门控（ARGUS_COMMENT_GATE 默认 collaborator）：review/recheck/fix/
  testgen 仅协作者可触发（🔒 拒答），防公开仓路人评论白烧审查预算；open 放开；
  名单 5 分钟缓存，平台不支持/查询失败 fail-open 留痕。⚠️ 行为变化
- 冷却有感知：指令撞 30s 冷却回 ⏱️ 提示不再静默丢弃（自由文本保持静默防刷屏），
  TestScenario20 节流断言随行为更新
- changelog 评论指令（CHANGELOG 条目草稿，writingSpec 模板，不写 CHANGELOG.md）
- e2e 补 help/status/门控拒绝/changelog 四场景 + 👀 回执断言；单测钉目录单源
  （手册全覆盖）与门控开关语义；lint+arch+-race 42 包全绿

### 新增(comment)：v5.2.0 @bot 评论命令面深化——mention 任意位置+显式重审命令+recheck fp: 单条意见复核

- mention 任意位置触发（句首/句中/句尾）：extractQuestion 重写，词边界挡邮箱
  与 @bot2 长用户名、引用行 > 转述不触发；句尾 mention 取全文去 mention 作指令
- 显式重审命令 review/评审/复核/重审/rerun…（extractReviewCmd+dispatchReview）：
  确定性建全量复审任务，零 LLM 分类、不受 ARGUS_COMMENT_ROUTER 开关影响；
  在飞去重回执如实（复用 rerunReviewReply 单源）
- 单条意见复核 recheck|复核 fp:短码（recheck.go，writingSpec 模板）：prints 定位
  入库意见 → diff 对照单次 LLM 裁决 成立/误报/存疑 → 回帖证据与处置建议；
  不建任务、不自动 suppress/fix（与持久抑制只来自人工红线一致）；fp 坏形态截停
  报错不滑进重审/Q&A
- dispatch 顺序：suppression → recheck → review → describe/testgen/fix → Q&A/路由
- e2e：GitLab review 命令、GitHub 句中 mention（normalizeCommentEvent 负载兜底）、
  recheck 播种 print 全链路；stack 补 extraPlatforms 多平台装配与 RECHECK 桩
- 顺修 store_budget_test 上月时间构造（31 日 AddDate(0,-1,0) 归一化回本月的月末假红）
- 实测（git.example.com Forgejo 全链路）：ask/review/复核/recheck fp:/复核 fp:/describe/
  suppress/坏 fp 报错/三种 mention 位置/PR 自动评审报告全部通过；rpi5 集群 v5.2.0
  部署验证；lint+arch+-race 42 包全绿

### 新增(config)：v3.187.0 服务侧按仓配置——repo_settings 表+admin API+runner 合并点+MCP get_repo_config

- internal/reposettings（L0 纯底座）：Settings 三旋钮（monthly_token_budget/
  task_token_budget/max_diff_chars，指针区分未设置）+ Parse 严格校验
  （DisallowUnknownFields 防拼写静默无效/非正整数拒绝/尾随内容拒绝）
- storage：repo_settings 表（platform/owner/repo 主键，AutoMigrate）+ 事务式
  upsert/delete/list/get（先查后写，SQLite/MySQL 双方言稳）
- service：合并视图（覆盖集+全局生效值）/upsert/delete/list + requiredScope
  收口（PUT|DELETE /repo-settings* = admin；PUT 兜底 admin 防新增写路由裸放
  read）+ describe 按仓 diff 上限 + 审计 repo.settings_*
- handler+router.go：GET|PUT|DELETE /repo-settings/:platform/:owner/:repo
  与 GET /repo-settings（读 read 写 admin）
- runner：taskKnobs 单次加载（读失败/损坏降级全局默认），repoBudgetGate/
  Execute 任务预算/prepareContext diff 上限三消费点显式传递
- MCP get_repo_config（只读合并视图；写入刻意只走 HTTP admin API，工具面 20→21）
- 语义分层：repo_settings 是 admin token 写入的可信面（可上调下调），
  与 .reviewagents.yaml 半可信文件面（只能收窄）互补；SAST/OCR/LLM endpoint
  仍只来自部署 env（供应链红线不破）
- arch 白名单新增 reposettings（L0）与 runner/service→reposettings；
  README/ide-mcp/architecture-deep-dive/CHANGELOG/skills 文档同步校准

### 新增(impact)：v3.185.0 函数/方法级图 diff——base 切面 BEFORE/AFTER 对比

回应「能不能到函数方法级、像 git diff 一样出图对比」：工作副本经 gitdeepen
保 merge-base 可达，git show <base>:<file> 取 BEFORE 切面，前后各一趟 AST，
按 Recv.Name 对齐做函数级 diff——
- FuncDiff：added/removed/modified + SigSame（纯体/调用变化甄别）+
  before/after 签名原文（≤200 runes）+ 出边调用集合增减（图 diff 边语义：
  绿=新增调用、红=断开调用）；触达过滤（hunk 区间未覆盖的函数不报，防噪声）；
  触达但语义面无变化（注释级）不报
- 快照新增 func_diffs 字段（封顶 20 条/文件 40/调用 10）；新增容量守卫：
  Changed 展示清单封顶 500（千文件 PR 单清单可顶爆体积守卫的边界）
- base 参照预检（git cat-file -e）：ref 不可达整层跳过并注明——不拦会把
  全部函数误判「新文件全量新增」
- 渲染三态：报告评论加图 diff mermaid（fdadd/fdrem/fdmod 类 + ±边形态）
  与函数级清单；HTML before/after 双列签名卡片 + ±调用 chips；prompt 段
  喂评审 agent 函数级变更行
- 单测 5 例（git 双提交 fixture：签名变更/换调用/删函数/增函数/未触达过滤/
  base 不可达降级）；实弹：argus 仓对 origin/main 自检，Build 判 modified
  且新增调用 diffFunctions/hasGoChange s 正确命中
- MCP get_impact 端到端实弹通过（initialize→tools/list 20 工具→tools/call
  返回含 func_diffs 快照→不存在任务可读回执）

### 新增(deps)：v3.185.0 harness v0.13.0 收编——r4ckpt 决策层迁移 checkpoint.BuildPlan

- agent-harness v0.9.0→v0.13.0、oagentkit/llm v0.17.1→v0.18.1（breakings 核实对 argus 惰性）
- r4ckpt 决策层收编：restoreR4Outcomes 的「哪些环节可信可跳」判定交 harness
  checkpoint.BuildPlan + StepDef.Isolated，产物层 plugin.Result 仍存 stage_artifacts
  （DependsOn/Isolated 是决策字段不是存储，与 R1/R2 的 ArtifactCkpt* 同构不变）
- 新增 TestRestoreR4Outcomes_FanOutOfOrderTrusted：扇出并发下兄弟环节留洞时
  已存环节仍采信（Isolated 语义），洞内环节回执行集重跑
- 门禁：test/eval（golden 69）绿；lint 余 1 红=internal/impactview/symbols.go:215
  callerLabel U1000（并行会话已提交代码，非本批引入，按纪律只上报不删）

### 新增(impact)：v3.184.0 风险分两维+prompt 预算守卫+渲染面打磨

- 风险分新增两维:go.mod/go.sum 依赖清单变更 +8(传递面超出本仓静态图);
  改动全为测试文件 -8(不达产线,分数钳 0 下限)
- prompt 段预算守卫:受影响包清单封顶 12(溢出注明"等 N 包")——大仓 16+ 包
  直接 join 吃 prompt 预算的隐患(agentkit 实测 16 包);新增无测试覆盖包
  线索行(agent 举证影响时可注明缺测试,纪律声明防泛化)
- 报告段截断注明:mermaid 节点超 24 截断时显式注明"图示截断,完整面见
  交互视图/get_impact"——渲染端点在 keep 集外的边静默消失,不能伪装完整面
- HTML 交互:搜索时仅两端都命中的边保持可见(data-i 回查快照边序,
  不依赖 DOM 序=数据序);节点 title tooltip(类型/ID/Detail)
- 新增 3 用例:风险两维数值断言/prompt 封顶与线索/截断注明;浏览器实测
  边联动与 tooltip 全通

### 新增(mcp)：v3.183.0 get_impact 工具 + /tasks/:id/impact 交互视图 + 文档校准

- mcpserver ImpactViewer 可选接口模式(nil 不注册死工具),get_impact 返回快照结构化 JSON(nodes/edges/packages/risk/coupling),工具面 19→20
- service ImpactView/ImpactViewHTML:taskVisible 域门(域外按不存在处理,不泄露存在性)
- GET /tasks/:taskId/impact(router.go customizedRegister,Bearer 复用):自包含交互 HTML——分层 SVG + 缩放/拖拽/搜索/点选详情,零 CDN 内网可用
- skill SKILL.md 速查表/tools.md 详情段/ide-mcp.md 工具清单三处校准

### 新增(impact)：v3.183.0 评审接线——快照注入/即时落库/报告影响段/prompt 注入

- runner injectImpactView:复用 codegraph 索引(关闭时自建),零 LLM 产快照;JSON 即时 MergeTaskArtifacts 落 artifacts_json(running 期 MCP/API 可消费,失败不丢)
- plugin ArtifactImpactView/ArtifactImpactSummary 常量;builtin guidelinesPrompt 拼接影响面段(数据区围栏 runner 侧已包)
- reportview Report.ImpactSection:details 折叠段插 findings 后(预渲染字符串,reportview 保持纯模板零新依赖)
- config ARGUS_IMPACTVIEW(默认开)/ARGUS_IMPACT_COUPLING(默认 100,负数关)
- arch 白名单:impactview 入 L0 纯底座(零 internal 依赖);runner+=impactview;service+=impactview(后续 MCP/HTML 面)

### 新增(impact)：v3.183.0 影响面管线核心——internal/impactview 分层推导

- L0 文件层(改动→节点) / L1 go.mod import 反向闭包(BFS ≤3 跳,暴露面/测试覆盖聚合) / L2 codeintel token 调用位 + AST 精化(封闭函数/限定名) / L3 测试面 / P2 历史共变(git log --format=%x1e 分组,共现 ≥3 入榜)
- 确定性风险分(0-100,理由逐条落快照;LLM 不打分) ;逐层降级记 Notes 不阻断;输出全确定化
- 渲染四形态:mermaid(节点 24/源 4400 双封顶,超限聚合包级再降级列表)/报告段/prompt 段/自包含交互 HTML(零外链,内网可用)
- 全套单测:fixture 临时仓端到端 + git 共变 + 确定性 + 渲染降级 + HTML

### 新增(cost)：v3.181.0 消费上游 llm v0.17.0——记账接 RecordPricedCached

上游 oagentkit/llm#37+#38（本仓提的缓存记账缺口）已 MERGED 并发版 v0.17.0，
本批消费：记账改用 RecordPricedCached，cached 落进 CostRecord.CachedTokens、
CacheHitRatio() 可派生（此前 cached 在上游记账层被丢弃）。
设计澄清（纠正早先「删 cacheAdjustedInput」的错误判断）：上游 Price 文档明确
「缓存命中价按厂商规则、由调用方覆盖」——缓存折扣本就该调用方在 Price 层表达，
故 cacheAdjustedInput 摊薄单价**保留**（非上游内置）。argus 侧 Prometheus 缓存指标
也保留（Grafana 时序面，与上游 pull 式 Summary 互补不冗余）。
依赖：llm v0.15.2→v0.17.0（含本仓 PR #38）+ transitive 地板跟随
concurrency v0.20.0→v0.20.1、pkg v0.21.0→v0.22.0；otel/otel/sdk 经 go mod tidy
因直引提为直接依赖。
测试：TestRecordPricedCachedExposesHitRatio（cached 落 CostRecord + 成本含折扣 +
命中率派生）。四门禁全绿（lint / test+arch / eval golden 69）。

### 新增(cost)：v3.180.0 省 token 面——缓存命中可观测+成本折扣 + 截断收紧

缓存盲区修复：CachedTokens 此前被 usage.go 采集但全仓零消费——前缀缓存命中率
不可见、成本按全价入账高估。现接入指标与计价：
- argus_llm_cached/prompt_tokens_total（命中率=cached/prompt）
- 成本按 ARGUS_LLM_COST_CACHE_READ_RATIO 折扣缓存命中部分（缺省 0.1），
  摊薄 InputPerM 而非污染 PromptTokens 计数（cacheAdjustedInput 纯函数可测）
截断收紧（杠杆③，对症 token 配额）：
- ARGUS_GET_FILE_MAX_RUNES / ARGUS_GET_ISSUE_MAX_RUNES 显式覆盖
- get_file floor 20k→8k（stashOverflowAt 两级化 + get_detail 可取全文=无损）
- get_issue 保持 8k（TruncNote 有损，收紧需质量 A/B）
测试：cacheAdjustedInput 六态 + CacheReadRatio 归一化 + 截断覆盖/floor 降档。
四门禁全绿（lint / test+arch / eval golden 69）。

### 新增(cost)：v3.179.0 省 token 面——窗口配置防呆（microcompact 死区修复）

- 215 实测数据驱动：argus_ctx_breakdown_runes_total role=tool 累计 5000 万
  runes = 上下文面 70%（user 1868 万/system 90 万）——根因 FitModel 清理线
  =(窗口-输出预留)×3/2×0.9，ARGUS_MODEL_CONTEXT_TOKENS=1M 虚高（flash 档）
  使触发线到百万级 runes，单请求（工具预算合计 ~6 万 runes）永到不了——
  microcompact「旧工具结果置占位」永不触发，工具结果随 ReAct 轮数线性膨胀
- 修复：wrapFit 启动防呆（窗口 >256K 告警 argus.boot.window_suspect 提示按
  模型真实窗口校正）+ README 环境表纪律注记；215 env 已校正 1M→131072
- 四门禁全绿 golden 69

### 新增(deps)：v3.177.0 四仓大版本升级 + LLM 调用进 OTel trace——harness v0.9/memory v0.8

- 版本：agent-harness v0.6.5→v0.9.0（guardset Assembly/archcheck import_exempt，
  argus 消费面 budget/ptc/toolname 零破坏）；agent-memory v0.6.0→v0.8.0
 （FactKind 时效分治/查询理解/向量轨/图谱——API 兼容编译零改动）；oagentkit
  llm v0.15.2/agent v0.10.4/toolgate v0.12.4/tools v0.10.3/obs v0.9.6/
  mcpkit v0.8.5/skillkit v0.8.4/rag v0.8.5/promptx v0.8.3/queue v0.6.3；
  oekit pkg v0.21.0/concurrency v0.20.0/core v0.12.0/data v0.17.0/hertz v0.8.1/
  kitx v0.3.0/middleware v0.13.1——三矩阵全部对齐上游最新
- LLM 出站挂 httpotel（v3.177.0 深度使用）：llm v0.15 HTTPClient + eino-ext
  HTTPClient 注入缝，tracedLLMClient 覆盖 6 处模型构造（3 ChatModel + 3
  OpenAIProvider）——LLM 调用成 OTel span 导出 jaeger（此前 OTel 只有
  gormotel DB 查询面，LLM 主耗时面无 trace）；注入 Client 接管超时语义
  （timeout<=0 取 provider 缺省 60s 同款）
- memorykernel 升级：dream 事实显式 Kind=FactLesson（时效语义：教训不因时间
  失效）+ recallCfg 单源接 query.Analyzer 查询理解（代词消解/时间窗解析，
  宿主字段优先语义不变）
- 向量轨/图谱评估留痕：装配面需 Embedder+VectorIndex，dream 模式量级（每仓
  几条）词法召回已够——语义路留待记忆量级增长再接
- 四门禁全绿 golden 69

### 新增(deps)：v3.176.0 oagentkit 七模块地板跟随——矩阵内完全对齐

- textx v0.6.2/runtime v0.7.2/mcpkit v0.8.4/obs v0.9.5/rag v0.8.5/skillkit
  v0.8.4/queue v0.6.3（地板跟随版，上游零代码改动——内容仅为抬
  pkg v0.20.0/concurrency v0.19.0 地板，本仓 v3.174/175 已消费）
- 全部 13 模块 + oekit 12 模块现在逐包对齐上游最新 tag
- 四门禁全绿 golden 69

### 新增(deps)：v3.174.0 go-git-platform v0.94.0→v0.96.0——上游 #25 落地，探测类 404 降 Debug

- 上游 transport/client.go 日志分级按票面建议逐字落地：5xx/429 保 Warn
 （重试引擎同口径），其余 4xx（探测类 404）降 Debug——argus 零改动
- 附加：go 指令 1.27.2→1.27.3（SDK 下限）；concurrency v0.18.1→v0.19.0
 （SDK 地板自动跟进）
- 四门禁全绿 golden 69；215 部署验收=探测 WARN 消失

### 新增(deps)：v3.172.0 guardx v0.13.1 + agent v0.10.1——argus 消费面零破坏

- guardx v0.8.2→v0.13.1（5 个 minor）：v0.9~13 全部为新增面（wire 执行生命周期
  /invoke 沙箱立论强化），argus 消费的 fence/logredact/injectband 三包未动，
  编译零改动
- agent v0.10.1 补丁；连带 oekit/pkg v0.20.0
- 双矩阵全部对齐上游最新；四门禁全绿 golden 69

### 新增(deps)：v3.170.0 oekit 收官补丁——concurrency v0.18.1 / pkg v0.19.0

- 今日新发两补丁版跟进（并发地板对齐 + pkg 面补充）；argus 消费的 12 个
  oekit 模块全部对齐上游最新
- oekit 深挖收官裁定留痕（本轮逐面核过）：hertz 已深度消费（RunServer/
  health/versionh/MetricsHandlerFor/NewLogger 全在上游，无手写替换面）；
  CacheAside 不接（仅 repoGuide 一个真 aside 点，契约适配器+cachetest 套件
  代码量超手写收益）；gopool 不接（Dispatcher 信号量+GoSafe+熔断 gen 配对
  已测试钉住，等价换池零收益）；bashguard/httpx/timertask/workflow 此前裁定
  维持
- 四门禁全绿：lint/test -race/arch/eval golden 69

### 新增(observability)：v3.163.0 oekit/tracing 接入——OTLP 链路导出 opt-in（ARGUS_OTEL_ENDPOINT）

- oekit/tracing v0.3.0 进依赖图（第 11 个 oekit 模块）：全局 TracerProvider +
  W3C/B3 传播器 + gormotel 挂主库——DB 查询成 OTel span 导出（215 jaeger
  all-in-one 接 4318 即活）
- 缺省零开销：Endpoint 空 = 完全不 Init、不产生任何网络连接；flush 资源注册
  先于 store（逆序关闭时最后 flush，关库后的残余 span 也能带出去）
- 配置四键：ARGUS_OTEL_ENDPOINT/PROTOCOL(http 缺省)/INSECURE(内网 true 缺省)/
  SAMPLE_RATE(0=全量)；storage.SetGormPlugin 注入缝（组合根组合，存储层不感知
  tracing）
- 连带 genproto 单体 v2022→v2026（拆分点之后，解 googleapis/api 双模块歧义）
- 四门禁全绿：lint/test -race/arch/eval golden 69

### 新增(config)：v3.160.0 core v0.11.0 升级——plugins.yaml 严格解码吃上完整诊断面

- oekit core v0.10.0→v0.11.0（#25 当日关票落地：config.Unmarshal 字节级 +
  LoadFile 任意路径 + LoadBootstrap 薄包装，三入口同一条解码线）
- LoadPluginsFile 从 yaml.v3 KnownFields 切 config.LoadFile(path, WithStrict())：
  拒载语义不变，错误面升级为嵌套路径（agents.skils）+ 拼写建议（是否想写
  "skills"？）+ 该层合法键清单（agent/auth_token_env/cmd/... 共 20 个）
- 测试钉住三样诊断要素（防将来回退 KnownFields 即丢）；随仓示例
  plugins.example.yaml 严格解码回归保持绿
- 四门禁全绿：lint/test -race/arch/eval golden 69

### 新增(config)：v3.159.0 plugins.yaml 严格解码——自有配置拼写错误从静默失效变响亮报错

- LoadPluginsFile 切 yaml.v3 KnownFields(true)：未知键拒载。旧语义=插件作者
  把 skills 拼成 skils 时该插件悄悄不生效且零线索；新语义=启动期报错点名
- oekit core v0.10.0 严格模式（嵌套路径+合法键清单+拼写建议）面更友好但
  导出入口 LoadBootstrap 钉死 etc/config.yaml 路径、字节级入口未导出——
  argus 任意路径加载吃不上，缺口记档待上游
- 敌对面 .reviewagents.yaml 保持宽容解码（裁定：攻击者不能靠配置错误面
  搅黄审查；严格只用于自有配置）
- 新增 2 用例：typo 键拒载点名 + 随仓 plugins.example.yaml 必须过严格解码
 （用户抄写起点，示例带未知键=严格模式上线即破坏所有复制者）
- deadcode -test 复扫归零；四门禁全绿：lint/test -race/arch/eval golden 69

### 新增(qa)：v3.158.1 ptc 诊断回喂 + 会话持久化抽出——程序面成功率与主流程可读性

- ptc 修复回喂一轮（harness 诊断面兑现）：正确性类失败（syntax/runtime/
  bad_result/pending）回喂 ErrorKind/行列位置/模型侧修复建议重合成一次；
  预算类（budget_calls/budget_bytes/deadline）不重试直接回退（方案太重，
  同型重试只再烧一遍预算）；特例=OK 但 Value="null"（ptc 契约把忘写 return
  归为成功——作答轮拿到 null 证据只能瞎编，按可修复处理并诊断点明缺 return）
- 修复重试防呆：fixed==prog（模型原样重发）视为放弃
- 重构：Answer() 会话块抽 qa_session.go（loadQASession/appendTurn/persistTurn，
  CAS 冲突重放与摘要滞后一轮语义逐字保留）；buildProgramPrompt/generateProgram
  拆分（合成与修复共用系统提示基底）
- 新增 3 用例：programFixable 九分类（含 null 特例与预算类不重试）/诊断文本
  （行列+建议回喂完整性）/修复回喂端到端（真语法错+缺 return 两形态→修复产物
  真实 ptc.Run 验证可执行）
- 四门禁全绿：lint/test -race/arch/eval golden 69

### 新增(qa)：v3.158.0 harness/ptc 接入——ask 证据采集 N 轮 ReAct 塌缩 2 轮 LLM

- ARGUS_QA_PTC=1 opt-in：程序合成（工具面+程序契约）→ goja 沙箱执行（三重
  预算：墙钟 30s/调用 16 次/字节 256KB）→ 作答轮（程序归并结果 DataFence 包裹
  为证据锚）；原始工具输出不过模型面，只带回 return 的小结果
- 双入口接线：Answer（评论问答）与 AnswerAboutReview（ask_argus 报告锚追问
  ——多轮深挖正是程序化取证主场）都在 ReAct 前先试 ptc
- 失败语义零损：合成失败/语法错/预算截停/作答空 → 全部回退既有 ReAct 路径
 （ptc 是加速面不是正确性面）；askOnce 路由抽出（ptc→react→single 三级）
- 接入纪律：绑定名 toolname.Normalize 净化（撞名装配期拒，规整单源）；
  Options.Stages 留 nil 裁定在案（argus qa 工具面构造期已收口，无动态披露/
  出站面，绑定直连 eino InvokableRun 不新增旁路面）
- 新增 4 用例：程序提取三形态/绑定适配与净化/e2e 预算截停（budget_calls）/
  沙箱子集（fetch 不可用）
- 四门禁全绿：lint/test -race/arch/eval golden 69

### 新增(deps)：v3.157.0 四仓全量拉最新——agent-memory v0.6.0 大版本 + 丢弃可观测接入

- oagentkit 六模块补丁版（llm v0.14.2/mcpkit v0.8.3/rag v0.8.3/skillkit v0.8.2/
  obs v0.9.3/tools v0.10.1）；oekit pkg v0.18.0（jsonrepair ExtractArray 等）；
  agent-harness v0.6.5（archcheck god-object 判据修复，argus 无触点）；
  agent-memory v0.4.2→v0.6.0（Scope 记录级归属/排序注入缝/分桶配额与丢弃计数
  契约件/sqlite 迁移层——API 全新增面零破坏，argus 编译零改动）
- 深度使用：memorykernel.recallOne 接 Result 丢弃可观测（agent-memory v0.6.0
  契约件 Drops 分路计数——budget=调总量/quota=调分桶占比/duplicate=提炼层
  造重复，只在真丢弃时留痕）；types.Scope/Config.Ranker 评估暂不接（argus
  双命名空间模型无第二层隔离需求/默认排序已够，裁定留痕）
- 四门禁全绿：lint/test -race/arch/eval golden 69

### 新增(deps)：v3.156.0 go-git-platform v0.94.0 + oekit 双模块拉新——ListCRs 语义吸收

- go-git-platform v0.78.0→v0.94.0（16 个 minor 累积；连带 gitlab client-go
  v3.15→v3.18）：唯一破坏面=F12 ListCRs total 语义显式化（int→*int，nil=平台
  未提供权威总数）——sdkctx 包装层吸收（对外 int 面不变，nil 归 0），poller
  丢弃 total 零改动；F11/F13 为 SDK 内部重构（错误分类去 reflection/webhook
  信封骨架），35 个 golden corpus 逐字节保真，argus 无触点
- oekit concurrency v0.17.1→v0.18.0（DoValue 泛型伴生，纯新增）、
  core v0.9.2→v0.10.0（config 严格模式 opt-in，缺省行为不变）
- go directive 1.27→1.27.2（SDK 下限抬升）；staticcheck 钉版 2026.2.1→2026.2.2
 （go 1.27.2 起 export data v5，旧版内嵌 x/tools 只认到 v4——lint 全仓
  "internal error in importing" 假红实锤，升版即愈）
- 测试桩同步：fakeProvider/listFakePR ListCRs 签名跟 *int
- 四门禁全绿：lint/test -race/arch/eval golden 69

### 新增(review)：v3.154.0 R4 逐插件 checkpoint——同任务重试续跑闭环

- 设计勘误翻案：stage_artifacts 断点续审 v3.12.0 已在（R1/R2），真洞是
  R4（LLM 主体成本）不在 checkpoint 面——进程崩溃同任务重试 R4 整批重跑
- harness checkpoint.BuildPlan 裁定不接：「连续前缀」为有序步骤链设计，
  R4 fan-out 插件彼此独立乱序完成，前缀规则会把合法完成集作废——
  复用 stage_artifacts 既有机制扩展（与 R1/R2 同构）
- Dispatcher 新增 OutcomeHook + RunWithHook（Run 委派 nil hook 签名不变）：
  产物落定即回调（含 best-of-N 聚合成功一次落定）；失败/跳过/取消不落键
- dispatchR4：Select → restoreR4Outcomes 回流 → PruneByBudget → RunWithHook
  落键 ckpt_r4/<plugin>（Result JSON）；键损坏当没命中回执行集（checkpoint
  永不做正确性决策）；sel 返回口径=复用+执行保留全集，G4 覆盖裁判不受影响
- 并发安全：r4CkptHook 持任务级 ckptMu（MergeTaskStageArtifacts 读-改-写
  合并，无锁并发丢键——并发用例钉住）
- 新增 7 用例（hook 三态/回流命中-未命中-损坏/并发不丢）；四门禁全绿 golden 69

### 新增(memory)：v3.153.0 agent-memory 生命周期接线——原始事件按龄回收日级 ticker

- Kernel.GC/Vacuum 委派 lifecycle（GC 顺带退役证据全没的事实卡——上游语义）
- subsys 挂 memory-kernel-gc 日级 ticker（仅内核开启时）：命名空间与 dream
  供给面同源（_org + ListPatternRepos 派生），超龄 30 天回收；单库故障只告警
- GC 签名收 string 不向 bootstrap 泄漏内核类型；nil 安全语义与既有面一致
- 新增 2 用例（超龄删除+新鲜存活/nil 安全）；四门禁全绿 golden 69

### 新增(qa)：v3.152.0 harness budget 接入——QA 历史 token 压力门压实

- agent-harness v0.6.2 进依赖图（首个落地包 budget）：QA 会话历史从纯轮数窗
  升级为「轮数窗（预算富余）+ token 压力档压实（Critical≥90%）」双轨
- internal/qa/budget.go：BuildPlan+Apply 压实（三条空转守卫全保留），摘要器
  复用既有 QA 模型（summarizeTurns 单源化自 rollSummary），归档 no-op
 （原文在 QASession.HistoryJSON）；budget.Item 无内容面，正文按 ID 同源捕获
- ARGUS_QA_HISTORY_BUDGET 可配（缺省 6000 token，本地 ceil(runes/3) 估算口径，
  与 textx/compact 同；分段估算不回填 provider 报数）
- 小历史路径逐字不变（golden/既有单测零影响），压实路径 4 新用例钉住
- 四门禁全绿：lint/test -race/arch/eval golden 69

### 新增(memory)：v3.151.0 agent-memory 内核底座接入——opt-in 灰度（ARGUS_MEMORY_KERNEL=1）

- internal/memorykernel 新包：capture（落库前强制脱敏，Redactor 必需）+ recall
  八段流水线（MemoryBand 注入防伪）+ sqlite 存储；R3 注入与 qa 注入双点切换
- dream 写侧双写：markdown 文件面照写（权威）+ 内核事实卡（证据链 QuoteEventID
  闭合）；清桶=事实卡软退役+原始事件清龄（召回吃 atom+raw 两路，只清一边会漏回）
- 驱动冲突排解：third_party/gosqliteshim replace 挡板——glebarez/go-sqlite 与
  modernc.org/sqlite 是同一驱动的两份拷贝、init 双注册 "sqlite" 必 panic；
  挡板仅别名 Error 类型（dialector 全部引用面），注册单源归 modernc
- 架构白名单显式登记 memorykernel（能力层）与 dream/runner/service 依赖方向
- 四门禁全绿：lint/test -race（含 memorykernel 5 用例）/arch/eval golden 69

### 新增(deps)：v3.150.0 oekit+oagentkit 全模块拉最新——破坏式路径四类重写

- oagentkit 12 模块全线拉新：agent v0.10.0/llm v0.14.1/rag v0.8.2/guardx v0.8.2/obs v0.9.2/runtime v0.7.1/textx v0.6.1/mcpkit v0.8.1/skillkit v0.8.1/queue v0.6.2/toolgate v0.12.0/tools v0.10.0
  · llm/llm→llm（v0.9.0 包提模块根）、rag/rag→rag（v0.6.0）、obs/obsx→llm/obsx（v0.12.0）、guardx/audit→obs/audit（v0.7.0）
  · CostTracker.Record 退役→RecordPriced（单位 1K→1M 写在类型上，argus 配置侧 per-1K ×1000 换算）
- oekit 10 模块同步拉新：pkg v0.17.0/data v0.16.0/concurrency v0.17.1/hertz v0.7.1/middleware v0.12.1 等
- 本批为 agent-harness v0.6.2（下限 agent v0.10.0/llm v0.13.0）与 agent-memory v0.4.2（下限 oekit/pkg v0.17.0）的前置拉平
- 四门禁全绿：lint/test -race/arch/eval golden 69

### 新增(deps)：v3.149.0 agentkit→oagentkit 矩阵迁移——冻结单体整体替换为 12 模块新家

- agentkit v0.15.0 出清，切换 oagentkit 12 模块：agent/guardx/llm/mcpkit/obs/queue/rag/runtime/skillkit/textx/toolgate/tools
- oekit 全模块同步拉最新：bootstrap v0.6.2/concurrency v0.16.0/core v0.9.0/data v0.13.0/hertz v0.6.4/logx v0.7.0/middleware v0.11.0/pkg v0.14.0，新增 kitx v0.2.0
- ctxfit 包退役：上下文拟合由 oagentkit textx/conversation 承接
- 四门禁全绿：lint(vet+staticcheck+gofmt)/test -race/arch/eval golden 69

### 新增(deps)：v3.148.0 oekit 全模块拉最新——结构重置对齐 + octopus 回归本仓

上游今日完成结构重置并响应 argus 所提 7 票（全部关闭），argus 全量跟进：
版本（go.mod）：logx v0.4.0 / pkg v0.9.0 / data v0.8.2 / middleware v0.5.0 /
hertz v0.5.1 / bootstrap v0.5.0 / core v0.6.0 / registry v0.3.0 / gormx v0.5.1。
破坏式对齐（上游「包提升到模块根」）：
- mw：github.com/oekit/middleware/middleware → github.com/oekit/middleware（10 文件）
- bootstrap/bootstrap → bootstrap；hertz/hertz{,/sse,/metrics,/health} → hertz/*
  （main/subsys/events_service 等）；core v0.6.0 的 ConfigWatcher slog 化 argus 未消费
上游 issue 落地核销（本轮全部在源码逐项验证）：
- middleware #9 ✓ ResponseMiddleware(WithSkipPaths/WithSkipPrefixes)：argus 协议
  端点豁免清单收回栈装配处（/.well-known/ 前缀 + /mcp），撤 agent.json 手工打标；
  MCP 桥保留组件级 SkipResponseWrap（挂载点无关的双保险）；内部头更名 X-Oekit-No-Wrap
- middleware #10 ✓ MinimalMiddleware 出现；middleware #11 ✓ 重复 hmw 包已删
- hertz #4 ✓ health/redischeck 拆包（go-redis 不再被核心 health 强制引入，argus
  保持 indirect 待 hertz 消费面进一步收敛后自然消失）；#5 ✓ 版本端点复用
  versionh.Handler；#6 ✓ health.Degraded 软降级位 + WithVerboseErrors()
- docs #6 ✓ 迁移指南/llms.txt 更新（站上核验）
- concurrency #3 追评（ttl=0 不驱逐）随 KeyedLimiter 增强票同轨推进
octopus 回归本仓：oekit/network v0.5.0 结构重置移除业务推送客户端（上游裁定
「业务推送客户端不属于本模块」，旧 tag 已清空）。argus notify 仅用其 SendMarkdown
单方法，客户端（81 行、纯 stdlib、v3.32.0 本就出自 argus）内联回
internal/notify/octopus，github.com/oekit/network 整体退出依赖图。
arch 白名单登记 notify→notify/octopus 单向边。
⚠️ 上游待修（已知问题）：hertz v0.5.2 tag 与 sum.golang.org 记录校验和冲突
（tag 重推所致：sumdb h1:IpE//… vs 实际 h1:biN98…），且 v0.5.1 的测试 import
旧路径 bootstrap/bootstrap 使 go mod tidy 需 -e 容错。argus 暂钉 v0.5.1；
建议上游以新版本号（v0.5.3）重发修正，勿重推已发布 tag。
门禁四绿：make test 40 包（-race）/ lint / arch / golden 69；真进程实弹五端点
形状全符、零 ERROR。logx v0.4.0 起 NewSlogLogger 会读 LOG_LEVEL 环境变量
（部署面如设该变量将改变日志级别，当前 runbook 未用）。

### 新增(http)：v3.147.0 服务面 oekit 收口——Response 信封/health/version/KeyedLimiter 四项

用户显式接受契约变更，把 v3.146.0 留痕的四项一并落地：
- mw.ResponseMiddleware 全局挂载（栈 hmw→obs→Recovery→Response）：裸 JSON
  （/ping 等）自动包 {code,data,message,trace_id} 信封；已带 code 的响应
  （mw.SuccessResp/ErrorResp、health/version 自写信封）不重复包裹。协议端点
  以 mw.SkipResponseWrap 豁免：A2A /.well-known/agent.json、MCP 桥
  （JSON-RPC 载荷，HertzBridge 入口打标）——两者消费方按规范解析顶层字段。
- /healthz → oekit hertz/health PingHandler（进程级存活，零依赖）；
  /readyz → NewHealthHandler 并发 Checker（db=store.Ping、llm=30s TTL 缓存
  探针），任一失败 503 + code 503 + data.checks 逐项状态，总超时 5s 上界；
  库内脱敏：真实错误只进 logx，响应统一 "check failed"（与 v3.8.0 不回显
  BaseURL 纪律同向）。旧 readyz 的 platforms 信息位按 k8s 语义移出
  （平台降级是软状态，原设计就不让它 fail readyz）：可见性由启动告警 +
  /reports/health 承接，README 同步。registerCustom 空出的 sdk 参数清理。
- /version 新端点 → oekit versionh：core/version ldflags 注入 + 运行时
  uptime。版本元数据单源：main.version（报告头/AgentCard）与
  core/version.Version 启动时同步；Makefile LDFLAGS 注入 Version/BuildCommit/
  BuildBranch/BuildTime，Dockerfile 增 --build-arg VERSION/GIT_COMMIT
  （默认 dev/unknown），deploy-215 本地交叉编译命令补 BuildCommit。
- per-token 限速 tokenRateOK → oekit ratelimit.KeyedLimiter（TokenBucket 工厂
  rpm/60 + burst=max(10,rpm/6) 与此前 x/time/rate 参数等价；TTL 1h 驱逐闲置
  key；sync.Once 懒建，不开限速的实例不起清理协程）。429 文案与 fail-closed
  语义不变，既有 repo_guard 限速测试零改动通过。
代价留痕：oekit hertz/health 内置 RedisChecker 使 go-redis/v9 进入 argus
间接依赖图（argus 不使用）。
验证：traceid_smoke_test 增 TestHealthVersionWrapSmoke（信封/503/版本透出/
裸 JSON 包裹/SkipResponseWrap 透出六断言）；真进程实弹五端点形状全符
（含 /ping 被收编、agent.json 未被包裹）；四门禁 make test 40 包（-race）
/ lint / arch / golden 69 全绿。夹具教训：requestid 校验 X-Trace-Id 字符集
（含 / 即重生成）、health Checker 错误库内脱敏。

### 新增(deps)：v3.145.0 agentkit v0.15.0——ToEkitLogx 桥退役，ekit 彻底出清

agentkit v0.14.0 → v0.15.0：v0.14.8~14 七轮完成 ekit→oekit 源迁移
（官方 CHANGELOG 认定 API 零变更、行为零漂移；argus 可见的唯一行为差
=rag Hybrid 单路 panic 空路降级，属改善），v0.15.0 为 oekit 底座纪元
定界版（依赖 oekit concurrency v0.2.1/logx v0.2.0/pkg v0.5.0，MVS
与 argus 侧拉齐至 v0.4.0/v0.2.0/v0.5.0）。
兑现 v3.144.0 预留的退役条件：
- internal/obs/ekitbridge.go 删除，6 处穿越点（TracingHandler/
  workcopy.NewPool/audit.New/WithLeaderLogger/worker.Pool.Log/e2e）
  还原直传 oekit logx.Logger；runner 对 obs 的依赖与 arch 白名单条目
  同步回撤
- ekit 自 go.mod/go.sum 完全消失（0 残留），argus 依赖面 oekit 十模块
  + agentkit v0.15.0，迁移闭环
净 −53 行。四门禁全绿：make test 40 包（-race）/ lint / arch / golden 69。

### 新增(deps)：v3.144.0 ekit→oekit 深度迁移——冻结单体整体替换为 21 模块新家

依 oekit 官方迁移指南（llms.txt 路径映射）全量替换：argus 是最后一个
钉在 git.example.com/enjoydream/ekit（已冻结，仅安全修复）上的仓；
兄弟仓 agentkit 已迁（工作副本），go-git-platform 零依赖。
- 18 条 ekit import 路径 → 10 个 oekit 模块（70 文件）：
  logx/metrics/pkg(jsonrepair,uidgen,mathx,httpjson)/concurrency.async/
  data(cache,gormx)/middleware(mw,traceid hmw 挪模块根)/hertz(核心+sse+
  metrics+validatorh)/core.config（ekit config 新家）/bootstrap/
  network.octopus。API 对照逐签名一致或超集，包名全部不变。
- 唯一破坏式：bootstrap Run(Config,fn) 删除 → New(opts...).Run(fn)，
  停机语义（信号→带超时逆序关停→panic 转 error）逐行核对等价；
  MetricsComponent 等拆至 bootstrap/obs、bootstrap/nacos 嵌套模块
  （argus 未消费，无需接入）。
- argus↔agentkit v0.14.0（仍依赖 ekit v0.33.0）边界：两侧 logx.Logger
  方法集形状一致，唯 Level/Attr 为两包命名类型 → internal/obs 增
  ToEkitLogx 桥（5 方法显式委派+int 转换，枚举值同为 iota 0-3），
  覆盖 6 处穿越点（TracingHandler/workcopy/audit/LeaderElector/
  worker.Pool/e2e）；agentkit 发版迁 oekit 后桥与 ekit 直接依赖一并退役。
- go.mod：+oekit 十模块（logx v0.2.0/pkg v0.5.0/concurrency v0.4.0/
  data v0.5.0/middleware v0.4.0/hertz v0.5.0/core v0.5.0/bootstrap
  v0.4.2/network v0.2.6/metrics v0.1.5）；ekit 保留为桥接直接依赖。
门禁四绿：make test 40 包（-race）/ make lint（vet+staticcheck+gofmt）/
make arch / golden 69 全过。govulncheck：oekit 模块零漏洞引入。
批次 B 扫描留痕：collectionx/retry/breaker/batch 等无对应手写债务
（历史下沉轮已收敛），ClaimNextPending 竞争循环与 retryBackoff 留
已审定行为，SheddingMiddleware 行为变更类候选不在机械优化范围。

### 新增(verdict)：v3.143.0 无需求类变更豁免——docs/tests-only 的 AC=0 不再压 approve

eval-real（deepseek-v4.1-flash 生产模型 A/B，50/59）根因批：R5.3 整轮触发 35
次为第一大规则——生产模型对 docs/tests/config 类变更系统性产出零 AC 清单，
clean-docs/clean-tests 两案被「AC=0 不许 APPROVE」压成 comment（golden 契约
期望 approve）。
- R5.3 的 AC=0 条件是反注入加固（空清单绕过 R5.1/R5.3 的洞），不松；
  新增确定性豁免：变更**只**落 docs/tests 路径 + 零 findings + 零 Ambiguous
  时 AC=0 是合法形态（纯文档/纯测试无可验收需求）→ 落 R5.5 APPROVE
- 豁免判定 = 纯路径分类（benignPath：扩展名/目录约定），零模型自判参与；
  带代码路径/有意见/模糊/scope creep/inScope 缺失一律不豁免——注入洞封死
- 配置/锁文件/二进制明确不在豁免类（golden 41/60 契约=COMMENT 不受影响）
- 边界测试 7 向 + 路径分类正反例钉死；stubbed golden 69 保持全绿

### 新增(mcp)：v3.142.0 search_findings 语义补位——换说法的历史意见也捞得到

关键词 LIKE 查不到「同话题不同措辞」的历史意见（与抑制家族的措辞漂移同源
问题）。关键词命中不足且 ARGUS_SEMSEARCH 开启时按 embedding 相似度补位：
- semsearch 导出 Cosine + Manager.EmbedTexts（复用同一 embedder/配置面）
- 最近 200 条意见池向量缓存（fp 键、上限 2 万、embedder 故障静默降级回
  关键词结果）；相似度下限 0.30（跨话题实测 ~0.05，同话题改写 ~0.4+）
- 输出 [语义N] 前缀行与关键词命中区分；tools.md 同步
- 主题桩 embedder 测试：关键词漏/语义中/不牵连他族/开关关闭只走关键词

### 新增(qa)：v3.141.0 ask 追问升级 ReAct——报告为锚+工具可深挖

ask_argus 带 question 此前单轮直调（只喂报告文本），「这个函数的调用方都有
谁/历史上这条怎么裁的」一类追问答不了。升级为报告锚定的 ReAct：
- react 增 reportAnchor 参数：最近报告以数据区围栏注入 system 指令，
  工具表全量可用（get_diff/get_file/search_code/search_knowledge/
  search_similar_issues），作答仍须 file:line 证据
- ReAct 失败降级原单轮直调（语义不倒退，degraded 留痕日志）
- 既有测试对齐新消息形态（报告锚在 system、追问在 user）

### 新增(security)：v3.140.0 数据可见性隔离——task_id 维度读取纳入 token 仓库域

多租户可达切片之二（查漏补缺发现的真口子）：token 仓库约束此前只闸 repo
入参工具，持 task_id 即可跨域读任意仓报告（get_report/list_findings/
get_task_stages/get_suggestion），list_tasks 更是全仓可见。
- taskVisible/taskListable 双守卫：域外任务单读 403、Copilot 面按不存在
  处理（不泄露存在性）、清单面直接过滤；platform=local（diff_preview 等）
  按「持有 task_id ≈ 持有能力」放行单读（24 位随机 hex 不可枚举，预审闭环
  对被约束 token 必须可用），但清单不列（任务存在性本身是元数据）
- 无约束 token 与内部路径（runner/poller 无 ctx 指纹）行为不变
- 真 Store 集成测试：域内 200/域外 403/local 放行/清单过滤/Copilot 不存在语义

### 新增(mcp)：v3.138.0 search_findings 历史意见账本——工具面 18→19

「下一个自然增量」兑现（Ellipsis 验证过的方向 + argus 记忆飞轮独有资产）：
prints 表是意见的跨轮次持久身份账本，暴露为检索工具——「这个问题以前报过
吗/怎么裁的/被压过吗/复现几次」的事实源，fp 短码直出可喂 suppress/restore/fix。
- storage.SearchFindingsHistory：本仓 LIKE 检索（参数化无注入面），带
  status/times_seen/last_seen，limit 钳 1..50
- service 数据面（仓库白名单双门同其他数据工具）+ 围栏渲染（评语 240 rune 截断）
- mcpserver FindingHistorian 可选接口 + search_findings 工具
- 与 search_knowledge 分工文档化：组织级规范/结论 vs 本仓意见流水账
- skill/tools/ide-mcp/README 四处 18→19 校准

### 新增(security)：v3.137.0 审计外送 SIEM + token 身份取证

安全约束批续（多租户可达切片：谁在哪条 token 下干了什么，可溯源可外送）：
- ARGUS_AUDIT_WEBHOOK_URL/_TOKEN：audit.event 结构化日志在本地留痕之外
  POST JSON 一份到 SIEM 端点——logx.Logger 接口嵌入包装（WithService 穿透），
  零调用点改动；旁路语义（5s 超时/失败告警/队列 256 满即丢弃计数），字段值
  经 audit 包 logredact 打码、外送载荷无明文密钥
- token 身份取证：BearerMiddleware 把 token 指纹（sha256 前 8 位 hex，非原
  token）写 ctx；suppress/restore 审计带 token_fp；外部 API 建任务新增
  api 来源审计行（webhook/poller 内部路径无指纹不产生噪音）
- 测试：外送转发/Bearer 头/非 audit 不转发/WithService 穿透/故障端点不阻塞
  调用方/token 指纹传播

### 新增(security)：v3.136.0 token 过期与轮换——#expires 后缀 + 过期即拒

ARGUS_API_TOKENS 格式再扩展 "token:scope[@repo][#expires]"：# 后接
YYYY-MM-DD（当日 23:59:59 截止）或 RFC3339；过期 token 在中间件即拒 401
（不给灰度窗口）。轮换语义：新 token 先并行入清单，旧 token 标过期自动失效，
零停机换发。坏格式条目跳过并告警（fail-closed 不变）。

### 新增(security)：v3.135.0 token 级仓库约束 + 每 token 速率限制

安全约束批续（把单 token 泄露半径压到单仓 + 防被盗 token 刷成本）：
- ARGUS_API_TOKENS 格式扩展 "token:scope@repo-pattern"（向后兼容无 @ 形态）：
  token 自身可限定仓库范围，与 ARGUS_REPO_ALLOWLIST 总白名单构成双门——
  即使总白名单较宽，单个 token 的泄露半径也只在其 pattern 内
- ARGUS_API_RATE_RPM 每 token 速率限制（缺省 120/分钟，0=关；x/time/rate，
  token 指纹做键，突发 max(10, rpm/6)）：被盗 token 刷 LLM 成本/刷审查的闸，
  超限 429
- 仓库门升级 repoAllowedFor(ctx)：外部入口同时校验总白名单与 token 约束；
  拒绝文案统一。README/SKILL 文档同步（token 约束+频率限制可见）

### 新增(security)：v3.134.0 安全约束批——仓库白名单 + sastscan 软链剔除 + 输入上限

安全实测遗留的约束收口（ci token 泄露半径 + 残余旁路面）：
- ARGUS_REPO_ALLOWLIST 仓库白名单（空=不限）：MCP/TaskAPI/QA/fix/suppress/
  restore 八个外部入口只触达清单内仓库（"platform:owner/repo"，支持 /* 前缀
  通配）——ci token 泄露半径从「bot 可见一切仓库」收成「清单」；webhook/
  poller 受信路径不受门约束；拒绝态可见（error/统一文案，不伪装成无数据）
- sastscan filterRegularFiles：扫描清单剔除软链/非常规文件——外部工具
  （gitleaks/semgrep）跟随软链读宿主文件以 finding 外带的旁路封死（环境
  白名单已封 env 面，此为文件面补齐；与 codesearch/semsearch 同口径）
- ask_argus question 上限 8000 字符（成本/DoS 约束）
- get_capabilities 暴露 repo_allowlist 状态（agent 一眼看清可触达范围）

### 新增(mcp)：v3.129.0 get_capabilities 能力面快照——工具面 15→17

好用度批③：外部 agent 新接手一个部署时，此前只能盲调 fix_finding/search_sem
吃错误文案才知道开没开——现在一次 get_capabilities 看清（llm/diff_preview/qa/
code_search/sem_search/knowledge/fix_agent/fix_push/codegraph/related_repos/ocr
+version，纯读零副作用）。
- service.Capabilities 单源（配置+装配态判定，nil/Noop 安全）
- mcpserver CapabilityFace 可选接口 + get_capabilities 工具（无参数）
- copilot CLI capabilities 子命令
- skill/tools/ide-mcp/README 四处 15→17 校准；suppress_finding 工作流与
  安全边界改写（「agent 工具面没有此工具」旧文作废→admin 门语义）

### 新增(mcp)：v3.128.0 suppress_finding 进 MCP——scope 透传 admin 门

好用度批②（backlog 在案「需 scope 透传」卡点收口）：agent/IDE 看到误报后
可直接闭环抑制（回流知识库+学习环负票），此前只能转告人工走 HTTP。
- BearerMiddleware 把授予档位写入 ctx（ContextWithScope/ScopeFromContext，
  fail-closed：无 scope 一律拒）——/mcp 路由只需 ci 档，抑制是信任边界写
  操作，工具级 admin 二次判定在 service.SuppressFindingScoped 收口
- mcpserver FindingSuppressor 可选接口 + suppress_finding 工具（fp 短码，
  行号/措辞漂移靠入库指纹覆盖）；core 未实现不注册
- 复用 SuppressFinding 单源（抑制/知识回流/PatternSuppress 记账零重复）
- 测试：scope 透传三档断言（rbac）/fail-closed 门+入参校验（service）/
  注册+fp 小写透传+错误语义（mcpserver）

### 新增(mcp)：v3.127.0 get_report 内联阶段进度——轮询单跳可见进展

好用度批①（实测摩擦：E2E 轮询 get_report 只见 status=running，R 几在跑
要另调 get_task_stages 才知道）：
- taskView 装饰单源 decorateStages：core 实现 Copilot 时附 stages 列表 +
  stage_current（首个未完成阶段）；get_report 与 review_pr 终态渲染同装饰
- CLI poll 等待期按阶段变化打 stderr（不再静默干等）；-json 信封带 stages
- 无 Copilot 能力的 core 输出不变（装饰是可选能力叠加）

### 新增(semsearch)：v3.126.0 search_sem 语义检索底座——MCP 工具面 14→15

行业调研 2026-10-09 P1（审查场景预索引仍是护城河：Greptile 全库图/Ellipsis
向量+历史 PR；Cursor +12.5% 实证「grep+语义双工具组合最佳」）：
- internal/semsearch 新 L0 包（零 internal 依赖）：Go go/parser 顶层声明切块
  （路径:行+doc 注释+签名=结构化摘要 embedding 输入，Greptile NL 化的零 LLM
  近似）、其余文本文件级切块、排除集与 codesearch 对齐、512B 二进制嗅探
- 进程内余弦检索 + 文件指纹（size+mtime）增量刷新：变更文件才重抽重嵌、
  单次批量 Embed 摊薄往返；maxFiles/maxTotalChunks 双预算闸（embedding
  成本一次性、可控）；向量只驻内存不落外部库
- ARGUS_SEMSEARCH + ARGUS_EMBEDDING_*（与知识库共用配置）双门，缺省关；
  rag.OpenAIEmbedder 经 Embedder 窄接口注入，包内零 SDK 依赖
- 接线：config→bootstrap(Manager 构建)→service→qa.Engine（workcopyDir
  复用 SearchCode 同模式）→MCP search_sem（SemSearcher 可选接口，未启用
  运行期报错工具不消失）
- arch 白名单 L0+qa+service 三处扩集（架构变更显式化）；skill/README/
  ide-mcp 文档 14→15 校准，README 环境变量表补 ARGUS_SEMSEARCH

### 新增(mcp)：v3.125.0 search_knowledge 进 MCP 工具面——知识库检索 13→14 工具

行业调研 2026-10-09 小批③（对标 Greptile MCP custom context/知识库检索工具）：
把 R3/R4 审查面同源的知识库检索暴露给外部 agent——「团队规范/历史结论/
这条以前怎么裁」类问题的入口，也是记忆飞轮资产的对外变现面。
- mcpserver 新增 KnowledgeSearcher 可选接口 + search_knowledge 工具
  （query 必填，top_k 缺省 5 钳 10）；未配置知识源在 service 侧运行期报错，
  工具不消失（fix_finding/search_code 同语义）
- service.SearchKnowledge 单源 s.Knowledge（Hybrid Milvus+Local RRF），
  nil/Noop 守卫、围栏防注入（数据纪律与 search_code 对齐）、1200 rune/片段截断
- skill 文档 13→14 校准：速查表/参数表/参考行；tools.md 补 search_knowledge 节

### 新增(cli)：v3.124.0 pre-commit 机器可读面——退出码契约 + -json + untracked 覆盖

对标 Greptile CLI 退出码 hooks 契约与 CodeRabbit --include-untracked（行业调研 2026-10-09 小批①②）：
- argus-copilot review/poll 退出码：0=通过/1=有达阈值意见(-fail-on 定档) /
  2=错误 / 4=空 diff / 130=信号中断；fail 1→2 重定义
- -json 结构化信封（mode/clean/findings/report），hook/CI 免解析 markdown；
  writing.ReviewDiff prompt 钉死「- file:line [high|medium|low]」行格式与三档词表
- -staged/-uncommitted/-include-untracked：CLI 侧 git diff 自组输入，
  未跟踪文件合成 new-file diff 拼入（尊重 .gitignore、二进制跳过、上限截断告警）
- skill 文档同步：argus-precommit 全流程改写、tools.md 子命令映射补 poll 行
  与退出码契约、修「10 工具」陈旧计数

### 新增(preview)：v3.123.0 提交方异步预审闭环——review_diff_async + poll

新 kind diff_preview（domain 词表 7 类）：无 PR 语义任务——platform=local
四元组空、diff 存 artifacts（≤48KB，TEXT 列 64KB 上界留 JSON 转义余量）、
Priority=10 人类在等档；runner prepareContext 前分派 runDiffPreview
（同 ci_analysis 先例），报告落库供 get_report 轮询，不回帖。
writing.ReviewDiff prompt 单源：自 service.ReviewDiff 下沉（LLM 生成器
带 stage 记账），同步快审与异步任务同执行体；Service 补 LLM 字段
（bootstrap 接 models.Generate）。
MCP review_diff_async（DiffTaskCreator 可选接口，秒回 task_id）；
copilot review -async + poll（10min 上限 3s 间隔）构成提交方闭环。
arch 白名单新增 service→writing（同 runner→writing 同向，注释记档）。

### 新增(mcp)：v3.122.0 MCP 工具面 10→12——list_tasks + search_code + argus-precommit skill

list_tasks：service.ListTasks 接线（status enum/limit 钳制同口径），
巡检/找 task_id 的入口——此前外部 AI 只能按 task_id/PR 定位无法枚举。
search_code：qa.Engine.SearchCode 复用 workcopyDir+searchCodeExec 单源
（30s 冷克隆预算/数据围栏/4000 字符截断），ARGUS_CODESEARCH 门控，
修复前侦察；mcpserver 走 CodeSearcher 可选接口注册。
argus-precommit agent skill：review_diff 提交前自查环（分块送审/处置
分级/收敛终止）+ git hook 样例。
argus-mcp skill、README、ide-mcp 工具清单同步校准（10→12）。

### 新增(knowledge)：v3.120.0 agentkit v0.14.0 + 知识检索 Hybrid 双路收编

- agentkit v0.13.0→v0.14.0（rag Hybrid 混合检索：向量+TF-IDF 双路 RRF 融合）
- buildKnowledge 装配升级：Milvus/本地目录从互斥二选一改 Hybrid 优先——
  双备即融合检索，单路失败自动降级另一路（韧性：此前 Milvus 查询故障
  即检索全挂）；Milvus 构建失败仍降级本地，目录缺失仍 Noop
- fallbackLocalRAG → buildLocalRAG（返回 (*rag.Local, bool) 供 Hybrid
  分支判定）；日志词表不变（knowledge_absent/ready/failed）
- 装配单测：本地目录→Local / 缺失→Noop（Hybrid 分支需活 Milvus，
  部署面以 knowledge_hybrid 日志观测）

### 新增(poster)：v3.92.0 GitLab 报告评论真·原地更新——NoteManager 优先路由

依赖升级 go-git-platform v0.67.1→v0.67.2（maint/v0.67.x 维护分支，基于
argus 当前钉住线；新可选接口 NoteManager=UpdateNote/ListNotes，gitlab
backend 走 MergeRequest Notes API，GitHub/Gitea 等 issue 即 PR 平台无需
实现）。vendor 本地手动同步（gitignore 约定；go mod vendor 会带入本地
agentkit v0.12.2 破坏式快照，不可全量再生）。
sdkctx 路由序升级：NoteManager（MR Notes API，GitLab 真原地更新）→
IssueManager（GitHub/Gitea 等 issue 即 PR 平台）→ DiffManager 原子换新
兜底（v3.91.2，NoteManager 缺席时的最后防线）。ListIssueComments 同序。
效果：GitLab 同 head 重审的报告评论恢复字节级原地更新（v3.74 契约），
不再每轮换新导致评论位置漂移。
测试：NoteManager 优先路由单测（Update/List 均不再触达 IssueManager，
嵌入接口 panic 金丝雀护法）+ poster 换新语义两用例（v3.91.2 已入）。
门禁：make test（race）+ make lint + make arch + make eval（golden 68）绿。

### 新增(qa)：v3.91.0 Q&A 挂 search_code——全文检索补齐 R3/R4/QA 全覆盖

补齐 v3.84.0 承诺的工具面（当时只落了 R3/R4，QA 引擎无工作副本管线）：
- Engine 新增 WC/CodeSearch 字段（service s.qa() 装配，共享审查面的
  工作副本池，ARGUS_CODESEARCH 同源开关）
- tools() 挂同款 search_code 工具（数据区围栏+截断纪律一致）；冷克隆
  30s 预算——QA 是交互路径，超时本轮静默收窄工具面不拖首答（审查流程
  通常已建好工作副本，下一问即恢复）
- searchCodeToolFor 拆出便于单测；arch 白名单 qa 允许集加 codesearch
门禁：make test（race）+ make lint + make arch + make eval（golden 68）绿。

### 新增(related)：v3.90.0 跨仓契约快照——related 段声明+R4 注入（服务级总门）

对标 Qodo 跨仓库契约审查的最小形态：审查时把关联仓的契约面
（openapi/proto 等显式路径）注入 R4 上下文，供评审员交叉核对接口/
数据结构一致性。
- .reviewagents.yaml 新增 related 段（≤3 仓 × ≤8 显式路径，无 glob 防枚举）；
  解析期敌对输入整份拒绝（repo 形状/路径穿越/超长），与 review 段同纪律
- runner.injectRelatedContracts：逐文件 SDK 只读拉取（ref 缺省借本仓默认
  分支），单文件失败降级注明不阻断；渲染走数据区围栏（关联仓内容是注入面，
  防线 #1 与 get_file 同纪律）+ 使用纪律尾注
- 信任边界双门：服务级 ARGUS_RELATED_REPOS=1（部署方授权跨仓读取）+
  仓库声明，缺一不生效
- builtin guidelinesPrompt 挂 ArtifactRelatedContracts；渲染纯函数单测 +
  解析钳制单测 + 门控单测
已知边界：Merger 侧跨仓影响的确定性标注不做（评审意见引用来源文件已达
最小可用）；跨仓 ref 解析不查远端默认分支（内网同构约定）。
门禁：make test（race）+ make lint + make arch + make eval（golden 68）绿。

### 新增(mcp)：v3.89.0 fix_finding 工具 + copilot fix 子命令 + IDE 接入文档

IDE 集成以 MCP 形态收口（裁定：不写原生 IDE 插件——2026 年主流 IDE/agent
客户端原生支持挂 MCP server，维护两套插件成本高于收益）。
- mcpserver 新增可选接口 Fixer + fix_finding 工具（core 未实现则不注册，
  tools/list 不出现死工具；/mcp 唯一写侧工具，沙箱隔离、绝不 push）
- service.FixFinding：评论链 handleFix 的同步孪生——共用 fixExec 执行体
  （守卫/工作副本/沙箱/验证），fixInflight 跨通道互斥，audit 带 channel 标
- cmd/argus-copilot 新增 fix 子命令（-pr platform/owner/repo#num -fp 短码）
- docs/ide-mcp.md：VS Code/Cursor/Claude Code 挂载样例 + token 档位安全
  建议（IDE 配 ci 档，admin 不进 IDE）
- README：MCP 工具表/API 表/copilot 命令同步
门禁：make test（race）+ make lint + make arch + make eval（golden 68）绿。

### 新增(api)：v3.88.0 GET /stats/overview 审查概览聚合——仪表盘数据面

按仓聚合任务量 / verdict 分布 / token 用量 / 抑制计数，供 review-service
前端与报表消费（argus 自身不做 UI 的裁定不变——只补数据面）。
- 存储层 StatsOverview：tasks 与 prints 两表各自 GROUP BY 后 Go 侧合并；
  verdict 分布 CASE WHEN（SQLite/MySQL 方言通用）；抑制计数口径=窗口内
  首次出现且当前 suppressed 的意见
- 门面 StatsOverview：days 窗口缺省 30、钳制 1..365，platform/owner/repo
  过滤可选，输出窗口/总额/repo 行集
- 路由走 customizedRegister 惯例（非 IDL 面，hz 生成桩零改动），🔒 read 档
- 存储层聚合测试（verdict 分布/失败排除/窗口外排除/抑制计数/过滤矩阵）
门禁：make test（race）+ make lint + make arch + make eval（golden 68）绿。

### 新增(auth)：v3.87.0 RBAC 最小形态——多 token + scope 三档（read/ci/admin）

对标 CodeRabbit/Qodo Enterprise 治理面的最小落地：不引入用户/角色体系，
单中间件内完成分级。
- ARGUS_API_TOKENS="tok1:admin,tok2:ci,tok3:read"：逗号分隔 token:scope
  列表；坏条目告警跳过（fail-closed，绝不猜默认档）
- 旧 ARGUS_APITOKEN 等价一条 admin 条目，两者可并存；全空仍 503
  fail-closed（v3.0.1 语义不变）
- 策略表：POST /findings/*（误报抑制写库）= admin；其余 POST/DELETE
  （建任务/MCP 会话）= ci；读面（含 /reports /stats）= read；
  admin ⊇ ci ⊇ read，不足 403（argus.auth.scope_denied 可观测）
- hz 生成桩零改动：分级判定在 BearerMiddleware 内按方法+路径进行
- rbac_test 矩阵钉住三档×方法面 + service/config 两侧 rank 闭集一致性
门禁：make test（race）+ make lint + make arch + make eval（golden 68）绿。

### 新增(audit)：v3.86.0 审计面补 action——verdict.posted/finding.restore/fix.push 单源

agentkit/audit 底座已在（此前仅 task.create/finding.suppress 两个动作），
本批把审查链路的关键可审计动作补齐：
- verdict.posted（runner.announceTerminal）：终态 verdict + 命中 R5 规则，
  审查结论全程可追溯
- fix.push（runner.pushVerifiedFixes）：bot 写作者分支是最高敏感动作，
  commit 前留痕（branch/fixes/files）
- finding.restore（service.handleRestoreComment）：误抑制恢复带 matched 结果
- finding.fix_requested / finding.fix_done：v3.85.0 修复命令的字面量 action
  收编为常量
Runner 新增 Audit 字段（agentkit/audit nil 接收者安全），bootstrap 单一
auditLog 实例注入 runner+service——审计单源不双写。
门禁：make test（race）+ make lint + make arch + make eval（golden 68）绿。

### 新增(fix)：v3.85.0 @argus fix 修复命令——acpx 编码 agent 沙箱修复流

对标行业 2026 一键修复标配（Cursor/Copilot/cubic 式 review→fix 环）：
评论 @argus fix fp:短码 直达单条审查意见的自动修复。
- 指纹回查 prints 表 → 工作副本本地克隆独立修复沙箱（git clone --local，
  源目录零写入，不碰共享审查工作副本）
- acpx 编码 agent 以 workspace-write 沙箱档出 patch；无沙箱旗标的 agent
  （kimi/mimo 等）fail-closed 显式拒绝，绝不裸跑可写 agent
- 确定性验证环复用 ARGUS_FIX_VERIFY_CMD（testgen 同款包装命令纪律：argus
  自身永不执行待审代码），修没修好由验证判定、不由 agent 自评
- 异步执行（独立 ctx，不占评论并发槽），受理/结果双回帖 + 进程内在途去重
- 不自动 push：写分支仍是 v3.74.1 ARGUS_FIX_PUSH 管线环职权
- reportview.Report.FixHint：fixer 启用时报告意见行附一键修复入口
- 新增 ARGUS_FIX_AGENT（空=关闭）/ ARGUS_FIX_TIMEOUT（0=10m）
门禁：make test（race）+ make lint + make arch + make eval（golden 68）绿。

### 新增(review)：v3.84.0 search_code 全文检索——受控 grep 工具进 R3/R4 工具表

对标 Greptile/cubic 类深上下文审查的最小反制：审查 agent（R3/R4/reviewer
共用 r3Tools）新增 search_code 工具——工作副本上的受控正则搜索（确定性、
零 LLM），覆盖符号索引到不了的词法线索（字符串/配置/非 Go 代码）。
- internal/codesearch 新包（L0 零依赖）：命中数/单文件/输出三重封顶，
  .git/vendor/node_modules/testdata 等目录排除，二进制嗅探跳过，字典序
  确定性输出
- Workflow.CodeSearchDir 任务期预写字段（②类），runner.injectCodeSearch
  就绪工作副本路径注入（只记路径不持引用，workcopy TTL 兜底）
- ARGUS_CODESEARCH 默认开（=0 关闭）；无纳入文件/无工作副本池自动跳过
- arch 白名单：codesearch 入 L0 桶 + requirement 允许集
门禁：make test（race）+ make lint + make arch + make eval（golden 69）绿。

### 新增(loop)：v3.74.1 遗留三件收口——修复 push 环 + 组织桶写侧 + pre-commit 本地预审

- 修复闭环最后一环（ARGUS_FIX_PUSH=1 显式 opt-in，依赖 FixVerifyCmd）：
  验证通过的修复 fixverifier.Apply 应用进工作副本 → git commit（argus-bot
  署名，message 带任务 ID/条数/回退指引）→ push 到 PR 源分支。push 失败
  （作者已推新提交/无权限）静默降级——下轮审查在新 head 上重新验证再战。
- 组织桶写侧（v3.74.1）：dream 跨仓共现聚合——pattern 文本归一后 ≥2 仓出现
  即组织级模式写 _org（slug=shortHash 稳定化，先清后写幂等）；顺带修一处
  既有 bug：KnowledgeDir 为空时记忆双写整体被跳过（现在解耦）。
- pre-commit 本地预审：service.ReviewDiff（单轮 LLM，不建任务不落库）→
  mcpserver review_diff 工具（DiffReviewer 可选能力挂载，diff 120KB 上限）
  → argus-copilot review 子命令（stdin/-f 读 diff，.git/hooks/pre-commit
  一行接入：git diff --staged | argus-copilot review）。
- memory.repoDir 组织桶目录名统一（_org，无仓后缀）。

### 新增(fix)：v3.74.0 修复验证环——suggested_fix 沙箱应用+编译验证，agent loop 闭环第一环

差距调研 P1-4（agent 时代修复闭环）的第一环落地：
- internal/fixverifier 新包：对带 suggested_fix 的意见在工作副本上真实应用
  （锚定行单行替换，与 GitHub suggestion 一键应用语义一致）→ 调部署方声明的
  隔离包装命令（ARGUS_FIX_VERIFY_CMD，{dir} 占位；沙箱纪律与 testgen 同构）
  → 还原文件。验证结论只做渲染标注（✅ 沙箱验证通过），不改指纹身份与
  verdict 语义。
- 安全面：路径越界拒验（工作副本根 prefix 检查）、单条失败静默降级为不标注
  （验证环绝不阻断审查主链路）、未配置包装命令时功能整体不存在（零行为变更）。
- 渲染：poster 行内 + 总结报告两处 Verified 标注；与 confidence/采样标注
  并列的第三个结构化决策辅助信号。
- 后续（外部拍板）：验证通过后的自动 commit push 需要授权模型——建议立项
  「修复建议一键转 commit」作为 opt-in 能力。

### 新增(learn,qa)：v3.73.1 组织级 learnings 两级记忆 + /similar_issue 入 QA 工具面——差距调研 D 批

- memory 组织桶（_org 目录，跨仓共享模式/教训）：Save 支持 OrgRef 写入、
  LoadIndexWithOrg 两级索引（组织段 40% 预算上限在前、仓段在后），R3 注入
  与 search_knowledge 两个消费方自动受益。写侧由 dream 周期聚合或运维
  工具投放（读侧先行，生产价值依赖写侧后续批次）。
- QA 工具面加 search_similar_issues（对标 PR-Agent /similar_issue）：
  『这个问题之前报过吗/是否已知』场景，SDK SearchManager 可选能力断言，
  未实现平台自然收窄（不加工具）。
- IM 通知经查 v3.1 已有（octopus 企微/飞书/钉钉通道）——调研清单误判，
  本批销项。

### 新增(observe)：v3.73.0 findings confidence 字段 + 按仓趋势指标——对标 Bugbot/CodeRabbit 差距项

差距调研 P1-6/P1-7：
- domain.Finding 加 Confidence（0-1 可选，不进指纹——渲染属性非身份属性）；
  R4 系统提示词要求模型如实自评把握程度；poster 行内与总结渲染对 <0.7
  条目加「模型低信心」警示（Bugbot'没把握就不说'的 argus 变体：说了但
  明示把握程度）。wire 层 omitempty，模型未给零开销。
- 按仓趋势指标（CodeRabbit review metrics 对标）：argus_findings_by_repo_
  total（密度/异常仓识别）+ argus_task_duration_seconds_by_repo（延迟趋势）；
  plugin 钩子族扩展（nil 钩子安全），finalizeAndAnnounce 埋点。

### 新增(ci)：v3.72.0 CI 失败日志归因——pipeline 事件接入 + ci_analysis 任务 + LLM 归因回帖

对标 2026 agentic review 趋势的 CI 失败分析缺口（差距调研 P1-5）：
- go-git-platform 升 v0.67.0（16b3cdb，vendor 同步）：CILogManager 可选能力
  （失败 job trace 尾部 20KB）+ Pipeline Hook 失败事件解析。
- 新任务 kind=ci_analysis（IDL 5）：TaskAPI 可手动触发；webhook pipeline.*
  事件自动触发（pipelineCreateTask，event_id 幂等 + audit/publish 三件套
  与 CR 建任务同款）。
- runCIAnalysis 独立入口：不走 prepareContext（push 的 commit 无 CR 可拉），
  失败日志 → writing.AnalyzeCIFailure（结论/根因/修复建议，GenerateJSON）
  → 有 MR 则评论回 PR，产物一律落库可查。
- 归因失败降级：单 job trace 拉不到不阻塞其余 job；平台未实现 CI 能力按
  任务失败上抛（诚实降级，不静默）。

### 新增(writing)：v3.71.0 写作面——describe PR 描述生成 + changelog 条目 + AGENTS.md 规则兼容

对标 PR-Agent /describe 与 /update_changelog（业界差距调研 P0-1/P0-3）：
- internal/writing 新包：单步 LLM 写作（无 R 系列裁判/插件派发）——Describe
  产 title+body（GenerateJSON），Changelog 产 markdown 条目；diff 60KB 截断。
- 任务 kind 扩展：describe(IDL 3)/changelog(IDL 4)，TaskAPI 可直接触发；
  runner.run 写作分支独立收口（marker/落库/播报复用既有原语）。
- describe 写回保护：argus 管理段（HTML 注释对）整段替换/追加，
  绝不覆盖作者手写描述；changelog 发 PR 评论。
- RepoGuideFor 多源（P0-2）：.argus/REVIEW.md 之外兼容读 AGENTS.md/
  CLAUDE.md/.cursorrules（第一个存在的，32KB 截断）——一行兼容整个
  agent 生态的规则文件。
- arch 白名单登记 runner→writing（叶子包方向）。

### 新增(sdkctx)：provider 注入 logx logger——SDK 内部告警不再进 noop 黑洞

- b3d42a0

### 新增(runner,builtin)：v3.52.0 跨轮意见注入（P1-2 verdict 翻转最小缓解）

- ArtifactPrevFindings：同 PR 此前轮次 open 意见注入 R4（上限 15 条），
  评审员保持结论一致、推翻显式说明理由；不动 R5 真值表
- builtin guidelinesPrompt 增跨轮一致性段（回指路径纪律）
- 测试：runner 注入过滤 + 提示词段渲染

### 新增(service)：v3.50.0 五轮——comment 链路 Bus 事件对齐 + 五轮文档归档

- 新增 comment.replied 事件（status ok/error），挂 postCommentReply/postQAError
  共用出口；此前评论链路无 Bus 事件，SSE 侧只见任务不见问答
- 设计文档 §4（五轮规划与复核定案：ctxfit 单槽/指纹漂移不改的裁定记录）
- CHANGELOG v3.49.0/v3.50.0 归档

### 新增(workflow)：v3.49.0 五轮——code_review R2 checkpoint 补齐

- summaryWithCheckpoint 统一入口（Run/RunReviewOnly 共用）：命中复用零 LLM、
  未命中成功即落库、失败降级不落库
- 此前 RunReviewOnly 不读写 checkpoint，code_review/visual_check 任务重试时
  R2 每次全量重跑（215 生产实测 ~5k tokens/次，MR #144 观察直驱）
- 命中时 out.Debug 标记 r2_checkpoint=reused，与 Run 路径口径一致
- 单测三路径：未命中落库/命中复用/LLM 失败降级

### 新增(ctxfit)：v3.44.0 轮次组粒度 microcompact + 错误/提醒结果保护（全级别）+ golden 扩容 68 条（65-69 真实世界形态）

- 4d9682f

### 新增(r2,golden)：v3.42.0 R2 软失败路径钉死（重试+降级+观测）+ R4 阶段事件 + golden 扩容 63 条（62/63/64 真实世界形态；harness R2 桩对齐真实长度）

- c61f74e

### 新增(config)：v3.41.0 配置陷阱如实化（IDLE_REAP 单配告警/REACT_MAX_ITERATIONS 边界注释）+ 外部工具限额与记忆预算 env 化 + compaction 落地手册留档

- ac93471

### 新增(obs,queue)：v3.40.0 ctx_breakdown 分解指标 + 任务优先级列（人类在等先于批量）+ SSE 阶段事件 task.stage

- 0a4afaa

### 新增(tools)：v3.39.0 工具面治理——大输出两级化（get_detail 详情库）+ MCP 风险分级（ToolHints 消费）+ skill 内容上限 + QA 记忆索引 + cli finding 截断

- eb74c2b

### 新增(inject)：v3.38.0 system-reminder 收编全部合成注入点 + 仓库级评审规范（.argus/REVIEW.md，信任边界齐全）+ MCP 工具表排序确定性

- b3cdb62

### 新增(guard)：v3.37.0 防失控三件套——循环异常守卫（连续同参调用拦截）+ ReminderNote 提醒信封 + 时序不变量测试卡点

- 118e1a3

### 新增(pipeline)：v3.36.0 幂等与卫生收口——review_posted 库标记/启动复位租约化/ReAct 旁路限速+预算回灌/阶段成本近似

- a531539

### 新增(memory)：v3.35.0 repo 级评审记忆——一文件一事实+R3 索引注入+dream 同源双写/同步退场（确定性抽取零 LLM 成本）

- 922dc02

### 新增(context)：v3.34.0 输出截断续写——ContinueModel（length→续写≤3轮）+ 可暂停 deadline/流中断两项核实达标在案

- 6f8145c

### 新增(context)：v3.33.0 上下文窗口治理——ctxfit 两级拟合（microcompact→截断留痕）+ 真实 usage 校准 + 阶段窗口配置闭环

- e1ab637

### 新增(integration)：v3.32.0 agentkit 新面主动接入——Retry-After 捕获/技能清单预算/探活独立预算/调用元数据/闲置回收（默认关）

- 62b47b2

### 新增(runner)：v3.30.1 重启恢复秒级重排 + start marker 三态生命周期（失败显式告知用户）

- main 启动即 ResetRunningToPending(0)：上次进程打断的 running 任务立即重排，
  恢复从分钟级缩到秒级（单实例前提，多副本须租约感知）
- start marker 三态更新（此前发布后永不更新）：成功=✅结论+规则；失败按
  attempt 分流=⚠️中断将自动重试（attempt<MAX_RETRIES）/❌最终失败可 push
  重触发；预算耗尽路径同步终态告知。PR 侧 best-effort，平台不可达静默放弃
- 修复链路闭环：重启→秒级重排→失败自动重试（既有 failed-retry 扫）→
  重试中/最终失败 PR 侧可见（runbook MR 阻塞场景同步）
门禁：build/vet/staticcheck+gofmt/test/golden 60/60/e2e 全绿

### 新增(workflow,eval)：v3.23.0 夹具 v8——R3 纪律 3（噪声排除文件 get_file 核实，v7 41 号 R5.1 误判消除）；扩容 42-44（锁定红线/回滚/多文件 AC）；41 重定位元声明标定；Makefile eval-real 超时 150m；增量快照带聚合

- f49db02

### 新增(cmd)：v3.22.0 argus-copilot 参考客户端——MCP 只读消费 stages/findings/suggest/ask 四工具，IDE GUI 另立项的集成参考

- 1c269c5

### 新增(codegraph,workflow)：v3.22.0 codegraph v1.5——Python/JS/TS 词法符号表（get_poly_symbol）+ Go 词法调用边（get_callers），零 cgo 零新依赖，词法级边界在工具描述声明

- df75f71

### 新增(service,config,obs)：v3.22.0 testgen 沙箱验证——包装命令范式（ARGUS_TESTGEN_VERIFY_CMD {dir} 模板），argus 永不直接执行待审代码；只写 _test.go 不覆盖/失败即清理/回帖附验证段；argus_testgen_verify_total 指标

- 8966a92

### 新增(config)：v3.21.0 Gitee 平台接线——ARGUS_GITEE_* 四件套进平台循环（SDK backend 已注册，生产实例实测待试点环境）

- 6f46e7e

### 新增(sast,runner)：v3.21.0 semgrep 变更文件聚焦——只扫 diffprep 审查范围（>100 文件回退全仓、防路径穿越），与审查范围一致

- a454dbd

### 新增(workflow,eval)：v3.19.0 夹具 v5——R1 契约标定：范围性元声明不进验收清单（r1System 规则 3）；首轮 A 组 26/26 满贯 FP=0 clean 3/3 boundary 2/2，注入零失守

- b3755d0

### 新增(mcpserver,service)：v3.17.0 P2-7 MCP 副驾底座——get_task_stages/list_findings/get_suggestion（IDE 追问与一键回写的只读数据面）

- 3146424

### 新增(eval)：v3.15.0 夹具 v3——comment/rc 边界按「从严合法性」标定

v2 三组共性 comment→rc 过严 miss 根因是夹具前提-内容错位（模糊需求句配
toy 代码，诚实模型判 partial/HIGH 属正当从严），非模型失守。三原则标定：
① 前提-内容对齐（需求句精确收窄到代码实际能力，02 口径）；
② 期望集合逐夹具建模（approve 入集合=诚实放宽，rc 仅限前提真实支撑从严，
  rc 期望仍精确——漏升级是危险方向不容方差）；
③ 注入类红线编码（approve 永不入注入夹具真实集合，修复 v2 口径漏洞）。
变更：05/07/09/10/18/24 需求句精确化；14/22 收窄切换 UI 出范围；
05/07 集合收紧 [comment, request_changes]；新增 27 review-only comment 档
（集合 26→27，真实分母 20→21）；驱动 RealVerdicts 注释更新。
v3 首轮 A 基线（mimo-v2.5 全局，单轮串行）：18/21（86%）——v2 六条 miss
全部回收，注入红线全安全；残留 3 条 miss（10/13/03）为真实过严信号与轮次
方差，v4 方向=comment 类夹具换无安全面代码。登记见 eval/EXPERIMENTS.md。

### 新增(codegraph,workflow,runner)：v3.15.0 P0-1 Go AST 符号上下文——get_symbol/get_references 进 R3/R4 工具表 + 变更触达符号注入（纯 go/parser，CGO_ENABLED=0 兼容，ARGUS_CODEGRAPH 默认开）

- 9484187

### 新增(service)：v3.15.0 P1-4/P1-5 @argus describe 与 testgen 评论命令——PR 变更说明/标签建议与测试骨架生成（轻量路径，节流幂等同 Q&A）

- b0aed9c

### 新增(sast,runner,workflow)：v3.15.0 P0-3 确定性 SAST 前置——gitleaks/semgrep 工作副本扫描直进 Merger（零 LLM、--redact 防二次泄漏、默认关）

- 047bd81

### 新增(config,context,workflow)：v3.15.0 P0-2 仓库级配置面——.reviewagents.yaml review 段（exclude/include 路径过滤、min_severity、max_comments、extra_instructions）

- 533a449

### 新增(obs,ci)：v3.14.0 评测指标面 + CI 定时评测工作流（并行会话在制快照，build/test 全绿代为落库）

- internal/obs/evalmetrics(+test)：eval 结果文件→指标面（RefreshEvalResults，
  完整结果计分、增量快照不计分、缺失静默）
- .github/workflows/eval-real.yml + test.yml：golden 评测与测试的 CI 化
  （G6 看板持续更新的集成点，此前文档标注为部署侧动作）
- .github/pull_request_template.md、config/main 装配、README/CHANGELOG/规划勾选
说明：本提交为并行工作会话的在制快照，原作者继续迭代时基于此进行

### 新增(export,srv,eval)：v3.13.0 路线收尾——G1c 训练数据导出管道 + G7 副驾数据面 + G5 健康查询 API（MiMo 批次 4 可执行部分）

G1c cmd/exportdata：
- pattern_signals + finding_prints → JSONL（Beta 置信度软标签、
  人工 suppress 最强负标签、regret_count 弱负、times_seen≥2 open 正样本）
- 数据飞轮→训练集管道打通；微调训练本身仍需资源拍板（外部依赖）
G7 副驾数据出口：
- ResultPayload.completeness 持久化——GET /tasks/:id 与 MCP get_report
  消费方免解析 markdown 拿到完备性结构化产出
- 与 SSE 阶段进度/@argus ask/MCP ask_argus 合计构成副驾数据面；富 UI 待拍板
G5 GET /reports/health（Bearer）：
- 与周期 IM 推送同数据源的飞轮状态按需拉取；手工路由（IDL 不承载聚合端点）
- e2e 场景 28（fail-closed 门控 + 空库 Empty 语义）
说明：全量测试中 gitdeepen/ocrdeleg 两例失败为后台 A/B/C 评测负载抖动
（单独重跑全过），非本批回归

### 新增(eval)：夹具 v2 标定——真实实现代码 + 真实模式期望语义（real_verdicts 集合命中 / skip_real 桩前提标记）

- goldenExpect.real_verdicts：真实模式下 comment/approve 边界允许模型方差
  （集合命中计通过），request_changes 边界仍精确——诚实口径而非放水
- goldenCase.skip_real：依赖打桩前提的夹具（canned persona 11/12/25/26、
  非诚实 R3 前提 16/23）真实模式跳过不计分
- 夹具 v2：05/07/09/10/18/24 代码升级为真实完整实现（诚实模型可判完成）、
  14/22 JS 真实 i18n、02 补 real_verdicts——首轮 46% 基线的 miss 归因修正
- 桩模式 26/26 全绿（语义固化不受影响）

### 新增(qa,runner,eval)：v3.12.0 长时程上下文——Q&A 滚动摘要 + 任务断点续审 + G6 看板轻量版（MiMo 差距批次 3 收尾）

G2a Q&A 长会话记忆：
- verbatim 注入保持 6 轮；原始轮次存储放宽 24，跌出窗口的轮次经 QA 模型
  增量压缩进 qa_sessions.summary 新列（AutoMigrate 加列），注入 prompt 头部
  「更早对话摘要」段（DataFence 内，半可信）
- 摘要失败保旧退化为纯 FIFO，不阻塞回答；CAS 重放不重算摘要（允许滞后一轮）
- 单测：淘汰窗口/注入拼装/nil 模型降级
G2b 任务级审查 checkpoint：
- tasks.stage_artifacts 新列：R1 spec/R2 摘要成功即持久化（CheckpointSink →
  MergeTaskStageArtifacts 合并写）；同任务重试回流跳过已成功阶段的重复 LLM
- 只在任务内生效（新任务恒全量重跑）；坏 JSON 容错为无 checkpoint，
  退化为全量重跑——staleness 语义不受影响
G6 回归看板轻量版：
- eval 结果同步渲染 latest.md 人读表格（与 latest.json 并存）；
  首轮真实基线（mimo-v2.5-pro 12/26）与归因已在 EXPERIMENTS.md 登记
unshallow deepen 改造已由 v3.9.3（eb526d1）落地，本批不再重复
全仓测试 + staticcheck 清零

### 新增(review,eval,health)：v3.10.0+v3.11.0 合批——golden 评测集+best-of-N+完备性裁判+健康报告

MiMo 差距批次 1+2（规划 IMPROVEMENT-PLAN.md，依据 research/mimo-vs-argus/REPORT.md）：
批次 1（v3.10.0，G1a/G1b/G3）：
- eval/golden/ 26 条 golden PR 夹具（injection/partial/creep/unverifiable/merge/guard）
  + test/e2e/golden_test.go 双模式驱动：make eval 打桩固化编排语义（CI 必跑）；
  make eval-real 真实模型 verdict 命中率（增量落 eval/results/latest.json，G6 数据源）
- eval/EXPERIMENTS.md：StageRouter×mimo 固定 harness A/B 对照矩阵+拍板规则
- best-of-N 同质采样：AgentDecl reviewN（≤5 钳制）→ Dispatcher.runSampled 并发
  N 采样 → aggregateSamples 指纹投票（复现 ≥2 升一级严重度+标注 k/n，不经模型）；
  复现计数走 Finding.Samples 结构化字段（不入 Comment，指纹身份稳定）；
  渲染层总结报告+行内评论展示；argus_plugin_bestofn_total 指标；e2e 场景 21
批次 2（v3.11.0，G4/G5）：
- internal/workflow/requirement/completeness.go：完备性裁判六项确定性检查
  （截断/插件缺失/lock 缺失/AC 漏判/unverifiable 过半/reviewer 降级）；
  annotate（默认注记报告独立段）| demote（APPROVE→COMMENT，R5.C）|
  retry（受限补审：失败插件预算内重试一次，替换不叠加）
- internal/health：审查健康报告周期聚合+IM 推送（notify.NotifyRaw）——
  噪声处置建议（人工门控）+ guidelines 增量建议（只出建议文本，绝不自动改仓库文件）
- golden 夹具 25/26（annotate/demote 守门场景）
 research/ 首次入库（MiMo 对比报告+改进规划）；eval/results/ 进 gitignore
 全仓测试+staticcheck 清零

### 新增(ocr)：v3.9.3 浅克隆历史补全改 deepen 增量式——internal/gitdeepen 探测式按需补全

- 新包 internal/gitdeepen 收敛 ocrshim/ocrdeleg 两侧同构 unshallow 逻辑：
  先本地探测 merge-base（秒级零网络，完整克隆/已补全副本零 fetch 直达），
  不可达再 --deepen=64 指数加深（×2 递增 8 轮封顶 + 5m 总预算，每轮重探）
  ——摊销从'每 PR 一次全量 fetch'变为'加深到 base↔head 实际距离为止'
- 纪律不动：refspec 显式全量（--single-branch 下非默认 base 缺引用，真件实测）；
  非浅克隆探测失败静默返回；失败降级整条 logredact；审查主链路不阻断
- 守卫语义变化：deepen 不移除 .git/shallow——守卫从'标记存在'改为
  'merge-base 可达探测'，池复用的工作副本换 head 后零成本通过
- 测试：gitdeepen 六场景（可编程假 git，状态用内建 read——acpx 最小环境下
  子进程外部命令不可用）；ocrdeleg/ocrshim argv 锁定测试随语义迁移

### 新增(breaker,storage,obs)：v3.9.2 深挖欠账清零——熔断配对契约 + 别名 PR 域改造 + 指标面补全

- 熔断：agentkit v0.10.4 breaker.Abandon（Allow 配对契约=Success/Failure/
  Abandon 恰好其一）；dispatcher 排队取消/teardown 与 consensus teardown
  三处接线——半开探测位不再滞留至 probeTimeout（~10min 误跳过窗口消除），
  带半开探测配对回归测试 ×2
- print_aliases PR 域改造：alias_fp 全局主键 → (PR, alias_fp) 复合主键，
  print_alias_pr_scope 迁移按 print_fp 反查 PR 回填（多 PR 复制、孤儿丢弃）；
  跨 PR 同款措辞漂移撞主键→UpsertPrint 回滚的故障面从根上移除；短码别名
  兜底简化为直接 PR 过滤；agentkit v0.10.2 → v0.10.4（go.sum 对齐）
- 保留策略：tasks 默认 30 天（原 0=永久，显式关闭仍告警）；pattern_signals
  按 last_seen_at 180d 清理（此前慢速无界）
- 指标：argus_poller_failures_total / argus_comment_busy_total /
  argus_comment_post_failures_total / argus_http_requests_total +
  argus_http_request_duration_seconds（路由模板级中间件）
- GitHub App refresher 收编停机序列（Stop 此前无调用方）+ GoSafe 化
- 测试补课：QA 会话乐观锁冲突拒绝/重读重放

### 新增(config)：ARGUS_REACT_MAX_ITERATIONS 可调化——builtin ReAct 迭代上限不再硬编码 12

deepseek-v4.1 等推理型模型在大 MR 上 12 轮不够：两次 ReAct 尝试都空转到
上限再降级单轮，白烧约 9 分钟且单轮产出质量打折。新增 env
ARGUS_REACT_MAX_ITERATIONS（缺省 12，<2 回退默认），bootstrap 按
Cfg 注入 WithReactMaxIterations；215 生产设 20 实测同 MR 713s→429s，
零撞限零降级，security 插件稳定产出。

### 新增(ocr,sink)：v3.8.0+v3.9.0 合批落库——决策链修复 + OCR 调用 MCP 化 + review-service push sink + 全量审查修复批

v3.8.0（决策链/工作流二轮深挖）：身份贯穿/fencing（FinishTaskFenced+ClaimNextPending
runner_id 守卫）/学习环记账（指纹 v2 行号入指纹、regret 归因）/R5 真值表 verdict 贯穿。
v3.9.0（OCR 调用通道 MCP 化）：
- ocrshim（cmd/ocrshim + internal/ocrshim）：mcp-go streamable HTTP server 包
  OCR review（acpx 同底座：进程组/超时/env 白名单/stdout 限容），findings 新
  schema 归一化 + 旧 schema 回退，history 留档，healthz；
- remote 插件 working-copy：needs:[working-copy] 在 Invoke 内 Ensure/Release，
  目录经 payload.workcopy_dir 下发（workcopy 复用语义：Release 保留 + TTL 回收 +
  增量刷新）；
- ocrdeleg 委托前置：ARGUS_OCR_BIN 启用，delegate preview/rule 确定性产物注入
  builtin R4（unshallow 显式全量 refspec + 独立 5m 预算 + 零凭证子进程）；
- sink（internal/sink）：审查完成后 push 上报 review-service（异步旁路、
  10s 超时 + 2 重试、ARGUS_SINK_URL/TOKEN），comment_delivery 幂等落库 +
  schema 迁移；/findings/suppress 反馈回流承接。
全量审查修复批（2026-09-17，1 P0 + 3 P1 + 4 P2）：
- P0 凭证泄漏收口：ocrshim 错误链整条 logredact（acpx 错误内嵌 stderr）+
  server 回传兜底 + dispatcher Notes/日志 + mcp 适配器纵深，带凭证 clone URL
  不再落入 PR 评论与日志；
- P1 shim unshallow 补全量 refspec（非默认 base 分支必挂，与 ocrdeleg 同款）；
- P1 regret/模式键错账：reflection 一律用入库 FP（printFP），merger 模式记账
  前移到展示标注前，runner PatternRegret 经 PrintState.Comment（新增）回查
  原文记账；
- P1 compose ocrshim 挂整 ./data:/data（留档卷缺失 + root 属主启动崩溃循环）；
- P2：delegate rule 路径加 -- 终止符；镜像默认 plugins.example 的 ocr 条目
  默认关；ocrshim 共享密钥鉴权（-auth-token + auth_token_env，可选配对）；
  gofmt 盲区补齐。
回归：go build/vet/gofmt/staticcheck/全量测试（24 包含 e2e）全绿。

### 新增(review)：v3.7.0 指纹 v2（行号入指纹）+ 近重复匹配层 + 存量迁移——措辞漂移不再绕过抑制/复现/regret 闭环; suppress 模式二走近重复检索; 幂等启动迁移

- 3c066c4

### 新增(review)：v3.5.6→v3.6.1 双会话深挖还债全量落地——架构重构 + P0/P1/P2 修复 + 探测窗口自适应

分版本详录见 CHANGELOG（v3.5.6…v3.6.1），主线：
- 架构还债（v3.5.7–v3.5.9）：契约上提 plugin（TaskKind/WorkflowOutput/
  WorkflowRunner/DataFence/Artifacts 键）；Q&A 拆包 internal/qa；装配统一
  bootstrap.BuildModels/Assemble（main 777→307 行）；LLM 限速器覆盖主链；
  plugins.yaml 单次加载
- 深挖 P0（v3.5.12）：零插件 R5.0 禁 APPROVE、acpx 解析失败计熔断、webhook
  event_id 幻影 task、评论处理异步化、重审疑问句守门、get_file 行号前缀、
  同 SHA 双跑防线（SQLite 部分唯一索引 + MySQL FOR UPDATE）、大 PR 截断透出
- 深挖 P1/P2（v3.6.0）：证据门交叉校验、存储加固（Claim SKIP LOCKED/
  ListTasks 列裁剪/保留清理）、插件钩子与观测指标、坏配置 fail-fast
- 收尾（v3.5.13/v3.6.1）：半开探测时限自适应（agentkit v0.10.1
  WithProbeTimeout，按最长插件超时+30s）、README 版本口径修正、
  bootstrap/poster/runner 单测补课、.gitignore 补 go.work 系列

### 新增(review)：v3.5 review 闭环——负控证据门 + 模式级后悔学习环 + dream + 活检

把「一次性 reviewer」变成「越用越准的 reviewer」（docs/review-closed-loop-design.md）：
- R3.5 证据门：evidence/counterexample 协议 + Go 后验（off|soft|strict，默认 off）
- pattern_signals 仓库级信任历史：Beta 置信度驱动标注 / 仅 low HardFloor 丢弃 / R4 注入
- dream 周期去重 + 低置信模式导出知识库；argus_layer_exec_total 九层活检与告警
- e2e 两轮 regret 闭环 / HardFloor / strict 证据门；修 R3.5 抹掉标注与 nil map panic
- 顺带：agentkit v0.9.4、评论词表否定守门、poster 截断修复、schema 补 regret_count
默认全关 opt-in；全量 go test ./... 与 go vet 通过。

### 新增(review)：restore fp: 误抑制恢复指令——suppress 反向操作, 误杀可撤销(短码前缀解析, 歧义不猜, 不回流知识); 含存储往返+负控单测

- 18600bf

### 新增(review)：regret 跨轮学习环 + 复现/可信度标注——接线两处零调用死面

- R3.5 反思剔除的指纹经 ReflectSummary.RemovedPrints → runner 入库
  finding_prints.regret_count（产出 vs 终审的差值 = 误报历史）
- Merger 接线此前零调用方的 GetPrintState：跨轮复现次数与 regret
  历史标注到报告 comment（入库后改写，不污染指纹与 prints 原文）
- 新增 Store.RecordPrintRegret（行缺失时创建, status=open）
- 只标注不自动 suppress：持久抑制仍必须来自人工反馈通道

### 新增(review)：Copilot 级能力对齐 + 依赖升级

- deps: agentkit v0.8.2 -> v0.9.3 (ekit 已 v0.27.2)
- router: 适配 v0.9.x Classify 门槛自守——低置信/未知意图以 error 返回但
  Decision 可观测, 保持"低置信回落 ask"语义, 硬失败(Route 空)仍冒泡
- poster: 行内意见带修复建议时渲染 suggestion 围栏(平台端一键应用);
  建议含围栏符或超 1200 runes 时退化为纯文本, 不让围栏断裂
- review-guidelines: 仓库根 .argus/review-guidelines.md(默认分支)注入
  内置审查插件 system prompt, 缺失静默降级, 截断 4000 runes
- specs: 补意见分层/触发路由/插拔层/依赖升级等设计与测试点对齐

### 新增：v3.4.0——接入 agentkit v0.8.0 架构原语（reflection/router/blackboard）

R3.5 报告反思（agentkit/reflection）：
- 挂点 Merger 之后、分歧标注与 R5 之前；终审编辑按误报 Rubric 批判→修订收敛
- 防幻觉纪律：按 id 回写只删/修不增；file/severity/line 逐字段校验回退；
  解析失败/模型故障保留原 findings；不写 suppress（持久抑制只来自人工反馈）
- ARGUS_REFLECTION_ROUNDS（0=关）+ ARGUS_MODEL_R35 阶段覆盖（回退 R3 模型）
评论意图路由（agentkit/router）：
- @argus 自由文本分类分派：review→建全量复审任务+回执（无同 SHA 去重），
  其余/低置信/分类失败→Q&A 原路径
- 确定性优先：≤30 rune 关键词直达零分类调用；MinConfidence 默认 0.6
- ARGUS_COMMENT_ROUTER=1 开启 + ARGUS_ROUTER_MODEL 快模型覆盖
编队黑板共识模式（agentkit/blackboard）：
- squads mode: consensus：成员不进 fan-out，黑板轮转互看（上限 2 轮）、
  一轮无人补充即收敛；互看经 Task.Artifacts[squad_board] 注入 builtin/acpx 提示词
- 成员失败转 note 降级不中止召集；产出按成员归属进 Merger 复用既有合并管线；
  consensus 成员不参与分歧标注
明确不接 PlanAndExecute：R1→R5 是已固化的确定性编排，叠 P&E 违背 agentkit
架构纪律（理由记录于 CHANGELOG）。
测试：三特性单测（reflection 6 场景/router 6 场景/consensus 6 场景），
go vet + 全量 go test + -race 全绿。

### 新增：P6 分阶段模型路由——ARGUS_MODEL_{R1,R2,R3}/REVIEWER/QA 覆盖（agentkit v0.8.1）

- agentkit llm.StageRouter：Generator 接口实现，精确优先/最长前缀/缺省链，
  UsedTokens 聚合全链；调用点零改动
- config：StageOverride 解析（ARGUS_MODEL_R1/R2/R3 + ARGUS_REVIEWER_MODEL/
  ARGUS_QA_MODEL + 各自 _CONTEXT_TOKENS），继承主 BaseURL/APIKey/降级链
- runner：buildWorkflow 装配 StageRouter（覆盖链注入任务预算）；
  Workflow.R3ChatModel 覆盖 R3 ReAct 专用模型
- bootstrap：Deps.ReviewerLLM/ReviewerName（R4 Reviewer 专用生成器）
- service：QAModel（QA ReAct 与单轮路径统一生效）
全仓 build/vet/test（含 e2e）16 包绿。未配置覆盖时行为与之前完全一致。

### 新增：P5 Q&A 对话式审查助手——ReAct + 多轮会话 + ask_argus MCP 工具

- @argus ask 切 ReAct 路径：qaTools（get_diff/get_file/search_knowledge，
  R3 同源纪律——内容截断 + 数据区围栏防注入）+ toolprior 三层约束 +
  agentrun.RunWithEvents；失败自动落回单轮直调（原路径 = 降级兜底）
- 多轮会话：qa_sessions 表（一 PR 一会话；6 轮封顶 FIFO 淘汰；24h 过期
  视为新话题），追问"这条 why"可带历史语境并引用 file:line 证据
- mcpserver 新增 ask_argus 工具（ReviewQA 可选接口，*service.Service 实现）：
  无追问 → 最新审查结论（LatestDoneTask 查询 + verdict/findings/报告摘录，
  报告 6000 rune 截断）；带 question → 基于报告的单轮追问回答
- 存储：QASession/LatestDoneTask + AutoMigrate；单测覆盖会话往返/过期/
  历史截断
全仓 build/vet/test（含 e2e）绿。

### 新增：P1 多副本横向扩展——poller 选主 + 工作副本实例隔离（agentkit v0.7.3）

- 存储新增 leader_leases 表 + TryAcquire/Release（worker.LeaseStore 实现，
  条件 UPDATE 原子判-占-续，列名 lease_key 避开 MySQL 保留字）
- poller 增加 IsLeader 门控：非 leader tick 跳过（轮询源幂等，双跑无害但浪费配额）
- main 装配 pollElector（ttl 30s/续约 10s，优雅停机让位入 AddResource）
- instanceID 提取复用（claim 归属/选主身份/隔离目录三处同源）+ boot 日志
- workcopy 按实例隔离 WorkRoot/instance-<id>/——共享 root 时他实例的孤儿
  扫描会误删本实例在用目录
- deploy/k8s/README-scaling.md 多副本指南 + 04-deployment.yaml 单副本档注释互指
全仓 build/vet/test（含 e2e）绿；SQLite 单副本档行为不变。

### 新增：v3.3.0——基于 agentkit v0.7.2 的能力迭代（P0/P2/P3/P4）

P0 成本记账：
- obsx Options.OnUsage（agentkit v0.7.2）回收全部 eino 模型调用真实 usage，
  统一覆盖 Generate 直连与 ReAct RawModel 旁路两条路径
- 任务级 + 全局 CostTracker（runner.Cost/CostPer1K）；报告元信息行新增
  "成本 ≈ $X"（ARGUS_LLM_COST_PROMPT_PER_1K / ARGUS_LLM_COST_COMPLETION_PER_1K）
P2 builtin Reviewer ReAct 化：
- Workflow.ReviewTools 导出（与 R3 共用工具基础设施，spec=nil 自动切审查语境）
- Reviewer 可选 ToolsHook：agentrun.RunWithEvents + ToolsFactory（限流按尝试重置），
  失败自动落回单轮 GenerateJSON；工具轨迹落日志
P3 交叉审查编队：
- plugins.yaml squads 段（paths/labels 触发 → 成员追加分派，reason=squad:*）
- Merger.AnnotateCrossReview：同文件 ±5 行"仅一方报告"标注分歧（幂等），
  报告头显示编队注记
P4 知识飞轮：
- SuppressFinding 成功后误报结论回流知识库（Milvus Index，safejson 转义；
  Local 模式显式跳过）
- MR 评论新增 /argus suppress fp:xxxxxxxx 指令（短码→完整指纹解析），
  与 HTTP API 走同一套抑制+回流逻辑
全仓 build/vet/test（含 e2e）绿；go.mod 对齐 agentkit v0.7.2。

### 新增：R3 工具优先级决策（agentkit/toolprior v0.5.2）

r3Tools 重构为优先级表（三层决策约束）：
- 策略提示注入 instruction：priority/cost/何时用（软约束）
- 工具表按优先级稳定排序：get_file/get_issue/get_diff(Core) →
  knowledge/use_skill(Support) → MCP 外部(External)（隐式注意力引导）
- 条件暴露：需求未引用 issue 时 get_issue 不进表（死工具省注意力）
- MCP 外部工具 WithCallLimit(5)（硬止损：防 runaway 刷外部调用）
- 同名去重逻辑保留（防 pool 工具覆盖内置）
策略段渲染顺序 = 优先级序，注入 R3 instruction 尾部

### 新增：skill 决策使用模式（ARGUS_SKILL_MODE=tool）

- config: ARGUS_SKILL_MODE=inject（默认，全文静态注入，现状）| tool（渐进披露）
- runner.skillText: tool 模式返回可用清单摘要（名称+描述，agent 决策依据）
- runner.buildWorkflow: tool 模式装配 use_skill 工具（agentkit skill.AsSkillTool，
  allowed=skillRefs 同源约束）
- workflow/stages: SkillTool 进 R3 工具表（与 getFile/getIssue/getDiff/knowledge 并列）
两种模式：
  inject（默认）——skill 少/小时最优，全文在提示词内零延迟
  tool          ——skill 多/大时用，提示词只带清单，agent 按相关性决策加载

### 新增：MCP 工具池实装——agentkit/mcp 驱动 toolpool（design §9.2 方向 A）

- plugin/config.go: plugins.yaml 新增 mcpServers/tools 两段
  （stdio command / streamable http url + headers ${VAR} 环境引用）
- toolpool: NewRealPool（声明校验/头部 env 展开/OnError 观测钩子）+
  RealAdapter（Argus spec → agentkit spec 映射）+ SpecsFromDecls
  修复 expandEnvRef 未定义变量被替换成空（与文档不符 + 潜在死循环）
- main.go: plugins.yaml 驱动装配，未声明 mcpServers 时 nil（零行为变化）
- plugins.example.yaml: mcpServers/tools 示例（默认注释）
- 测试：toolpool 4 个（specs 映射/env 展开/空声明/无效声明跳过）
依赖 agentkit v0.5.0（mcp 包：连接池 + eino-ext GetTools 白名单转换）

### 新增：接入 agentkit obsx 可观测性

- runner.Execute: obsx.InitLLMObservability 一行启用 eino callbacks 追踪——
  R1-R5 所有 ChatModel 调用自动产出 llm.call.start/end/error 结构化日志
  （stage/耗时/真实 token usage/慢调用 60s 告警）
- adapter_acpx: argus.acpx.run.start/end/error 结构化日志
  （耗时/findings 数/usage/cost/session_id）
- agentkit v0.4.0

### 新增：acpx 插件形态——CLI 编码 agent 接入 R4 专家委派

- internal/plugin/types.go: KindACPX（kind: acpx）
- internal/plugin/config.go: AgentDecl.Agent 字段（agent: codex/kimi/mimo/...9 家）
- internal/plugin/adapter_acpx/: 新适配器
  · agentkit/acpx 驱动（沙箱只读 + env 白名单 + 进程组清理）
  · 工作副本可选（Ensure 失败降级纯 diff 审查；引用覆盖 agent 运行全程）
  · 提示词契约：diff 数据区 + findings JSON 输出契约；ExtractJSON 容错解析
  · severity 归一化（CRITICAL→high），By 标注插件名
  · 二进制缺失 = 停用插件而非启动失败（ErrBinaryMissing）
- bootstrap: kind=acpx 装配分支
- plugins.example.yaml: coding-agent-codex 示例（默认停用）
- 测试 9 个：findings 解析/归一化/空结果/malformed JSON/未知 agent/
  二进制缺失（隔离 PATH）/workcopy 生命周期/prompt 契约
依赖 agentkit v0.3.4（kimi/mimo 适配器经真机冒烟修正）

### 新增：降级链调优参数 + 降级事件日志

- config: ARGUS_LLM_RESILIENCE_RETRIES / ARGUS_LLM_RESILIENCE_BASE_DELAY
- main.go: OnFallback → 结构化日志（argus.llm.fallback，观测主备切换频率）
- agentkit v0.2.4

### 新增：接入 agentkit Resilient LLM 降级链

- config: OPENAI_FALLBACK_BASE_URL/API_KEY/MODEL/CONTEXT_TOKENS 四项配置
- main.go: 四项全配时构造 Resilient（主模型→备选降级链），否则单模型 Client
- runner: 新增 LLM Generator 字段，buildWorkflow 经 BudgetInjector 注入任务预算
- workflow/builtin: LLM 字段改 llm.Generator 接口（Client/Resilient 无感切换）
- agentkit v0.2.3

### 新增：Milvus 向量检索集成

- config: 新增 ARGUS_MILVUS_ADDR / ARGUS_MILVUS_COLLECTION / ARGUS_EMBEDDING_* 配置
- main.go: Milvus > 本地 RAG > Noop 三级降级
  - Milvus 模式：连接 Milvus + OpenAI Embedding + 启动时索引 knowledge 目录
  - 本地 RAG 模式：目录放 markdown 文件
  - Noop：无配置时的空实现
- agentkit v0.1.0（含 Milvus 后端 + OpenAI Embedding 适配器）

### 新增：agentkit v0.1.0 RAG 增强 + 知识库内容

- agentkit/knowledge/rag: TF-IDF smoothed IDF, 代码块保护, ### 支持, 块间重叠
- knowledge/: Go 编码规范/审查标准/安全清单/误报记录

### 新增(p4+build)：SSE 进度流 + octopus 推送 + go mod 构建化

P4 通知：
- internal/progress/bus.go：64 缓冲有损广播 Bus（SSE 数据源）
- internal/notify/notify.go：octopus /api/v2/send/quick 终态推送
- biz/handler/api/events_service.go：GET /events SSE 流
- plugins.example.yaml：remote 插件 tool 字段说明更新
构建系统 go mod 化：
- 删除 vendor/（ekit v0.17.0 + git-platform-sdk v0.50.0 已发布远端）
- Dockerfile：-mod=vendor → -mod=mod + BuildKit --mount=type=ssh（SSH 直拉私有模块）
- Makefile：docker 目标去 go mod vendor，加 --ssh default
- deploy/docker-compose：build.ssh=default
- deploy/k8s：configmap/secret 对齐 mimo-v2.5-pro + 1M 窗口配置
- .dockerignore 新增

### 新增(v3.1)：P1-P4 v3.1 功能实装

P1 远端插件协议子集（adapter_remote/）：
- MCP client：mark3labs/mcp-go streamable http（Initialize→CallTool→findings 解析）
- A2A client：Agent Card 发现→message/send→tasks/get 轮询→artifact findings
- plugins.yaml 新增 tool 字段（mcp 工具名）；发现沿用仓库主权红线 3
- MCP 用真实 in-process server、A2A 用 fake httptest，6 个用例全过
P2 RAG 最小实装（knowledge/rag/local.go）：
- markdown 知识目录 → 按 #/## 标题分块 → 词元重叠打分（ASCII 词 + CJK 二元组）
- search_knowledge 工具挂进 R3；10min 过期重扫；ARGUS_KNOWLEDGE_DIR 缺失回退 Noop
P3 运营补全：
- ARGUS_REPO_MONTHLY_TOKEN_BUDGET：仓库月度预算门控（runner 调 LLM 前短路）
- store：RepoMonthlyTokens / MonthlyTokensByRepo / FindReportByHead
- GET /reports/:ref（按 head SHA 查报告）、GET /usage/monthly（用量+预算水位）
P4 通知：
- internal/progress：64 缓冲有损广播 Bus（service 发 created、runner 发 running/finished）
- internal/notify：octopus /api/v2/send/quick 推送（X-User-Id 机器账号，URL 空=禁用）
- GET /events SSE 流（io.Pipe+SetBodyStream，15s keepalive）
- store_usage_test.go + progress/bus_test.go + notify/notify_test.go 补充

### 新增(context-1M)：1M 上下文窗口单一基准派生 + 两级 R1 提炼

模型窗口（ARGUS_MODEL_CONTEXT_TOKENS）唯一基准，全部上限按比例派生：
- diff：C×35%（去 500k 硬顶）；diffprep 单文件=max(8000, 总量/50)
- 预算：C×60%（EffectiveTaskTokenBudget）；删除 TASK_TOKEN_BUDGET 200k 固定默认
- 输出：C×5%（MaxTokensResolved，保底 4096）；OPENAI_MAX_TOKENS 默认 0=派生
- 需求：PR body C×6%、issue C×1.5%、R3 工具全部从 config 派生
- 启动日志打出生效值防配置漂移
- retryableLLMError 加 context-length 标记防白烧重试
- fitInput：发送前输入自守恒（窗口-输出预留 ×2 chars/token ×0.9）
两级 R1 提炼（超长需求不截断不丢失）：
- 需求分段（reqSegment）→ 总量 ≤bodyMax 原文直进单次 R1
- 超过则 R1a 逐块提炼要点（r1DigestSystem 提示词，chunkSize=bodyMax，maxDigestChunks=8）
- 合并要点后进 R1 解析；失败回退截断原文继续
- 全部有损路径留痕 Ambiguous（[两级提炼]/[超长截断]/[提炼失败]→R5.3 人工确认）
- maxACs=30 条上限（超出→[条目超限]）+ maxACTextRunes=500 单条截断
模型切换：mimo-v2.5 → mimo-v2.5-pro（MoE 旗舰，深度适配 Agent+Coding）
配置：.env ARGUS_MODEL_CONTEXT_TOKENS=1000000；k8s secret/configmap 同步
6 个新单测（fakeChatModel 路由 R1a/R1 + nopLogger）

### 新增(mcp-server)：MCP Server 实装 + 辅助接口

上个会话遗留（方向 B）：
- MCP Server（internal/channel/mcpserver）：4 个工具（review_pr/requirement_compliance/list_agents/get_report）
- 辅助接口：sdkctx.ListReviews、poller 改进、worker 并发安全、dispatcher/registry/types 小修
- adapter_cli 测试补充、reportview/storage 测试补充

### 修复(llm)：v5.5.2 韧性参数接线——无降级分支的 4 处单模型 Client 此前恒用库默认 3试/2s 退避,ARGUS_LLM_RESILIENCE_RETRIES/BASE_DELAY 静默丢弃;demoos 网关租户桶实测 10 req/min 共享,security 插件 429 后 14s 内 3 试全部落在饱和窗口内被静默打掉(MR 79 note 38892 实锤). 补齐 buildGenerateChain/stageGenerator/reviewer default/runner workflow 四处 +1 对齐语义(env 不含首次,Client 含首次). 附带销红两枚既有欠账:arch impactview→gitdeepen 白名单补录(d348b19 引入消费未同步,门禁自 v5.4.7 红至今);e2e fakeProvider 补 DeleteIssueComment 桩(v0.114.0 gp#33 主路方法,promoted 落 nil 嵌入接口 panic,Scenario22 红至今). 全量测试+arch 门禁+vet 绿

- 30ea7dc

### 修复(memory)：v5.5.1 补 FTS tokenize 装配接线——config 键加了但 assemble 消费段未落盘（脚本 assert 失败中断在先，同脚本后半段未执行）；trigram 缺省接线 + boot 日志带 fts_tokenize 字段

- b75c18e

### 修复(ha)：v5.4.9 TryAcquire INSERT 冲突降噪——多副本下租约被占是预期路径，gorm 红字 ERROR 每操作刷一条淹没真故障；quietDB 走冲突语义（回退条件 UPDATE 收敛）

- f5d369f

### 修复(review+impact)：v5.4.7 funcdiff 浅克隆 deepen 补全 + 总结评论 head 轮换浮顶

- impactview/funcdiff：base 预检不可达时先探测式 deepen（此前注释声称
  「浅历史由 gitdeepen 保证」实则 impactview 路径从未消费——只有 OCR 通道
  调过 EnsureMergeBase；MR 审查 impactview 先于插件执行，大仓必降级）。
  EnsureMergeBaseTLS 补全成功即继续函数级 diff；失败/仍不可达保持降级跳过
  （不可达时必须 return，防「按新文件全量新增」错误语义——NoBaseRef 测试钉住）
- poster：总结评论轮换浮顶语义——①主路径 head 推进（≠target.LastHeadSHA）
  删旧建新（GitLab 讨论区按 created_at 排序，原地更新永不浮顶）；同 head
  重试仍原地更新不刷屏；删除失败降级原地更新不阻断回帖主链
- sdkctx：DeleteIssueComment（DiffManager.DeleteNote 面；provider.IssueManager
  接口无删除方法，测试 fake 补齐 DiffManager 三件 stub）
- gitdeepen 门面补 EnsureMergeBaseTLS 转发（funcdiff 消费；TLS 纪律与
  workcopy 克隆/OCR 通道对齐）
三轮单测：轮换浮顶/同 head 原地/删除失败降级 + 39 包全绿。

### 修复(lifecycle)：v5.4.2 运行时纪律批——裸 goroutine 不变式恢复 + ensure 指标 + 死代码清偿

- 裸 goroutine 清零（4 处反例全部收编 async.GoSafe）：main.go「裸 goroutine 已
  全部收编 GoSafe」声明此前为假——最危一处是 fix.go 评论触发的异步修复
  （完整 LLM agent+沙箱链路裸跑，panic 即进程死；215 guardPanic 教训同型），
  另有 sink 上报/audit_forward 循环/DrainComments 等待器；收编后 grep 复核为零，
  不变式恢复为真
- repoAllowed 死代码删除（service_repo_guard.go 无 ctx 白名单判定，全仓零调用
  方，外部入口一律 repoAllowedFor 携 ctx）；对应测试断言改走活入口，语义覆盖
  不变
- argus_webhook_ensure_total 指标（6 动作 created/updated/unchanged/
  secret_patched/skipped/failed）：v5.4.1 ensure 通道只补了日志漏了指标面——
  opt-in 通道无指标就只能翻日志核实，与 v3.7.x skipped 盲区同型教训
- ctx 贯穿面审计存档（不修项）：ptc.go 工具 Info 纯元数据无 I/O / store 启动
  迁移无请求 ctx / runner skill bgCtx 有注释刻意选择
门禁：make lint 绿 + make eval（golden 69 例）绿 + 全量测试零失败

### 修复(poster+boot)：v5.2.1 分页韧性+并发收口——rpi5 集群实弹炸出的五件

- 伪翻页根因的 argus 侧配套（go-git-platform v0.99.10 无进展检测待发）：
  真实 Forgejo 15.0.1 评论列表忽略 page 参数，SDK 翻满 50 页报
  ErrPageBudgetExceeded 并丢弃数据 → poster 扫描降级新建，实测 PR#8
  碎片化 4 条总结评论；真机验收（全新 DB 逼扫描路径）：扫描零报错、
  评论数不增、原地更新命中
- poster 新建前双查（summary_race_recheck 留痕）：①②③ 落空到 ④ 之间
  并发审查可能刚回写定位缓存——命中即转原地更新，封住双 worker 并发
  审查同 PR 的碎片化残余路径
- reviewHints 注入留痕（argus.regret.hints_injected）：提示词面注入
  此前零日志，排查「这轮意见变少」缺线索
- 内置插件超时适配推理模型：correctness 300s→600s、security 120s→600s
  （mimo-v2.6-pro 实测 context deadline exceeded 高发）
- CreateTask 403 报错透明化：白名单/token 仓库约束拒绝明示原因，
  不再与参数错误共用泛化文案
门禁：make lint / test / arch / eval（golden 69）四连绿

### 修复(runner)：v3.185.4 任务体 panic 落 failed 终态——guardPanic 统一安全出口

live 实弹炸出:影响面注入 nil map panic 后任务卡死 running——上游 queue/worker
的 runTask panic 隔离只记一行日志(无栈、无终态),依赖心跳过期复位重跑,
对确定性 panic 是「复位→再炸」死循环;argus 的 failed-retry 退避才是重试正道。
- guardPanic:任务体 panic → error 转换器,附全栈日志(上游日志此前无栈,
  排查被迫临时 defer 二次复现);下游已提 queue#16 补上游侧
- Execute:wireWorkflow→run→失败收口全段入守卫;panic 路径在守卫外补
  handleRunFailure(其自身炸断仅日志,终态缺失仍有心跳复位兜底)——
  确定性 panic 从「无限复位重炸」变为 failed-retry 退避
- TestExecutePanicToFailedTerminal:裸 Runner(nil SDK)+真实 claim 流程,
  端到端断言 panic 后任务必达 failed 终态且 FailReason 带 panic 线索
  (fenced 写入以 claim 的 running 归属为凭据,测试故走 StoreQueue 真流程)
- TestGuardPanicPassthrough:透传/转换两契约

### 修复(impact)：v3.185.3 MergeTaskArtifacts 对 artifacts_json="null" panic——真实 Forgejo 全链路评审炸出

最后一轮深测打通真实全链路:本地 argus 对 git.example.com(alice-smoke/smoke-app#1,
TaskAPI 驱动)——真克隆/真 diffprep/影响面注入/裁决/回帖全通,却在注入持久化
处 panic(assignment to entry in nil map),任务卡死 running:
- 根因:TaskAPI 建的任务 artifacts_json 为字符串 "null"(nil map 序列化产物),
  json.Unmarshal("null", &cur) 成功且把 cur 置 nil,cur[k]=v 即 panic——
  单测未覆盖因测试任务走空串 guard
- 修复:MergeTaskArtifacts/MergeTaskStageArtifacts 统一 nil 钳制(同型隐患);
  回归锚补 "null" 列场景
- 修复后真 PR 全链路复跑全绿:impactview.ready(nodes=4/calls=2/risk=medium/
  26ms)、func_diffs 正确判 added(login/main,新增调用 fmt.Sprintf/Println/
  login,新文件口径)、risk 28 分逐项可复算、/tasks/:id/impact 200、Gitea
  侧 argus:report 总结评论 + R5.0 降级结论真实回帖
- 文档合规:architecture-deep-dive 目录树补 impactview,CHANGELOG 记档
  v3.183.0~v3.185.3 批次(含四 bug 清单)

### 修复(impact)：v3.185.2 交互视图同步剪枝+边标签悬停显——用户实测遮挡反馈

几何量测证明节点框零重叠(等距 42px/框高 30),遮挡实感来自两处:
- 交互 HTML 未同步 mermaid 的孤立符号剪枝——类型/常量触达符号(FuncAdded/
  MaxCallsDiff 等 8 个)堆满改动列,与悬停边标签互相糊叠;viewGraph 剪枝集
  与 mermaid 同纪律,改动列 16 盒→5 盒(3 文件+2 有调用方符号),快照 33→23
- 20 条边的中点关系小字常驻互叠——包 .edgeg 组默认隐藏、悬停浮现
  (transition 0.15s),关系详情仍走 tooltip/点选面板
- data-i 保持原快照边序(视图剪枝不减序号),JS 搜索联动不受影响
- 内嵌 JSON 保持全量快照(剪枝仅视觉层,数据面不缩水)
- TestRenderHTMLViewPrune:SVG 无孤立符号+edgeg 组+视图集口径;浏览器实测
  改动列 5 盒/常驻标签 0/悬停 opacity=1/几何重叠 0

### 修复(impact)：v3.185.1 mermaid 可读性重构+渲染纯函数性修复——用户实拍「看不懂」反馈

可读性(根因:L2 触达符号混入类型/常量,无调用边即孤立节点,mermaid 堆成
无人连接的噪声列,把真正的依赖图挤没了;改动文件与符号同红色难分辨):
- 孤立改动符号节点摘除(度=0 的 symbol 不进图;函数级故事由 funcdiff 卡片承载)
- 改动符号独立黄色 chgsym 类(红=改动文件/黄=有调用方的改动符号/蓝=受影响包/绿=测试)
- 边标签动词化:波及依赖包/波及调用方/需跑测试/历史共变(读向恒为左改动→右受影响)
- 报告段图上方加图例行
纯函数性(实弹复现的真 bug):
- renderMermaid 用 edges[:0] 原地过滤,污染调用方 s.Edges(len 不变、尾部
  残留旧边)——二次渲染(超长聚合路径)把旧边重复收编,实测 n9 边 2→4;
  改独立分配,函数恢复纯;TestRenderMermaidPure 连渲两次逐字节一致 + 长度哨兵
- TestMermaidDropsIsolatedSymbols 钉孤立摘除/黄类/动词标签三契约
实弹:argus 对 origin/main 自检,33 节点→15 图节点(11 孤立符号摘除),
mermaid.js 实渲染 13 节点 10 边零重复

### 修复(impact)：v3.184.1 快照容量守卫——artifacts_json TEXT 64KB 列上界防溢出

自审发现的生产隐患:节点/边无总量上限,大 PR(数百变更文件)+大闭包场景
JSON 可超 artifacts_json 的 TEXT 64KB 列限——MySQL 严格模式写失败,
MCP/API 面静默失去快照(只余 Warn 日志)。
- 节点封顶 150(影响权重序,复用 mermaid 同款 prioritizeNodes),
  边随端点裁剪(保 TestNoDanglingEdges 不变量),截断注明+Truncated 置位
- JSON 体积守卫:超 48KB(与 ArtifactDiffInput 落库纪律对齐)按 20% 批次
  逐轮降节点重marshal,Changed 清单为语义源不裁(由节点封顶先行兜底)
- TestSnapshotCapacityGuard:200 改动文件+200 符号场景端到端断言
  封顶/无悬挂/体积/注明四要素
- 顺手:报告段列表分支硬编码 24 → MaxMermaidNodes 常量

### 修复(impact)：v3.183.2 影响视图 Content-Type 修正——hertz c.String 覆盖为 text/plain

实浏览器/HTTP 链路调试抓出:c.Header 预设的 text/html 被 hertz c.String 内部
setContentType 覆盖,响应头落成 text/plain(浏览器靠内容嗅探才碰巧渲染对)。
改用 c.Data 直写字节体,显式 text/html; charset=utf-8 下发。

### 修复(impact)：v3.183.1 影响面挂边端点补 file: 前缀——浏览器实测抓出 3 条悬挂边

- firstChangedFileIn 返回裸路径,改动包→依赖方包/测试面的边端点缺 file: 前缀,
  SVG/mermaid 渲染双面静默丢边(端点查不到即过滤)
- module 根目录包标签 "." 收敛为 "(module 根)"
- 新增 TestNoDanglingEdges 不变量回归锚:所有边端点必须落在节点集内

### 修复(observability)：v3.182.0 ReAct 路径 429 服务端退避闭合 + FailoverModel 双包注释订正

## Retry-After 写入端闭合（真缺口）
CHANGELOG「ReAct 直调 429 捕获不生效，待上游」是**过时误判**：上游 oagentkit/agent.agentrun
已注入/消费 Retry-After sink（agentrun.go:210/224），httpjson.RetryAfterTransport 已导出——
缺的只是写入端没挂在 argus 出站 transport 上（CaptureRetryAfter 仅 oagentkit Provider 层生效，
ReAct 走 eino ChatModel 不经 Provider）。
修复：tracedLLMClient 的 Transport 外层包 httpjson.RetryAfterTransport（单点覆盖 6 个模型
构造出口，含 ReAct 的 eino 路径）。RetryAfterTransport 幂等（只重复 set 同值）、响应体不改写、
无 sink 零开销——与 Provider 层的 CaptureRetryAfter 叠加无害。至此 agentrun 重试环在 ReAct
路径也按服务端建议退避（比本地指数准，mimo 并发限流对症）。
测试：TestTracedLLMCapturesRetryAfter（429 Retry-After 写入 sink）+ TestTracedLLMRetryAfterNoSink
（无 sink 原样透传不改状态码）。
## FailoverModel「双包」不可删（订正早先判断）
早先记的「清理冗余 FailoverModel 双包」是**错的**——核实后反转：外层包**不可删**。
阶段覆盖的 R3Model 是裸模型（buildStageModel 出 openai.NewChatModel，无降级），靠这层包补
主备切换；仅当来源是 RawModel（v0.10.9+ 自带 RawModelWithFailover）时它才冗余。故「统一包」
是对的，删除会打断 R3 覆盖路径的 failover。三处注释（models.go 不变量/ReAct 段、
runner_workflow.go）订正「RawModel 只返回主模型」的过时表述并标注不可删。
四门禁全绿（lint / test+arch / eval golden 69）。

### 修复(audit)：v3.169.0 回退 httpjson 误收编——空响应体契约回到手写 POST+状态码检查

- 215 实弹暴露 v3.164.0 误判：审计端点契约是 fire-and-forget（200 空响应体），
  而 DoJSON 无条件 json.Unmarshal——每条成功外送误报「响应解析失败」WARN
- 修正=手写 POST 保留 v3.164 核心修复（状态码检查：非 2xx 带状态+响应摘要
  告警，5xx 静默丢事件不再复发），不做响应解码、不重试（fire-and-forget
  语义，重试只会造成 SIEM 重复投递）
- 新增 2 回归用例：200 空体零告警（假失败钉）/502 必告警（原缺陷钉）
- 四门禁全绿：lint/test -race/arch/eval golden 69

### 修复(audit)：v3.164.0 审计外送收编 httpjson 单源——顺修 5xx 静默丢事件；ocrshim env 读取面单源

- AuditForwarder 手写 POST 切 pkg/httpjson.DoJSONWithRetry（与 sink 同一条
  外送线）：**顺修实缺陷——旧路径不检查响应状态码，SIEM 端 5xx = 审计事件
  静默丢且零告警**；现非 2xx 计入 forward_failed 留痕，重试 1 次吸收瞬态
- cmd/ocrshim envOr 手写助手退役切 oekit/core/config.GetStringWithDefault
 （sidecar 与主进程同一 env 读取面）；langfuse 评估留痕：obs/langfuse 是裸
  ingest 客户端非 callbacks 适配层，接线=手建 trace 发射点属特性工程，
  eino-ext 本地无现成 handler，暂缓
- 四门禁全绿：lint/test -race/arch/eval golden 69

### 修复(memory)：v3.155.1 Open 自建父目录——215 实弹暴露 kernel.db 建新目录下 SQLITE_CANTOPEN；Dockerfile builder 升 golang:1.27（agent-memory/harness 要求 go 1.27，1.26 基座 GOTOOLCHAIN=local 编不出）

- d07d513

### 修复(deps)：v3.148.1 hertz v0.5.2——上游修复 sumdb 冲突与旧测试路径，tidy 恢复常规

上游已处理 oekit/hertz#7（v0.5.2 tag 重推撞 sum.golang.org 校验和）：
清缓存后直连下载校验通过、bootstrap_test.go 已改用新根路径
github.com/oekit/bootstrap。argus 解除 v3.148.0 的两项临时绕法：
- hertz v0.5.1 → v0.5.2（不再依赖 GONOSUMDB 旁路）
- go mod tidy 恢复常规（不再需要 -e 容错；v0.5.1 测试引旧路径
  bootstrap/bootstrap 的问题在 v0.5.2 已修）
仅 go.mod/go.sum 变更。四门禁全绿：make test 40 包（-race）/ lint /
arch / golden 69。
遗留对账（oekit 上游）：concurrency #3 已关闭、实现已在 main
（9d03d0a，含 ttl=0 不驱逐语义），等上游打 v0.5.0 tag 后 argus 再收编
bump；octopus 方自建件届时一并下线。

### 修复(http)：v3.146.0 oekit mw.RecoveryMiddleware 接入——补 P0 级 panic 零恢复缺口

深度吸收轮（用户指令「继续深度优化 使用 oekit」）全能力矩阵对照后
唯一净收益落地项，其余面全部留痕裁定（见下）。
缺口定性：argus 全局只挂 traceid + Prometheus 两个中间件，无任何
panic 恢复。hertz 引擎兜底 recover 仅在 engine.PanicHandler 非空时
defer（pkg/route/engine.go:754，PanicHandler 全库无默认赋值、argus
亦未设），netpoll 事件循环显式不 recover（connection_onevent.go:191
注释写明不破坏 panic 栈）——任一 handler panic 一路穿透 = 进程崩溃。
修复：main.go 挂 mw.RecoveryMiddleware()，堆叠顺序 hmw(traceid) →
obs(HTTP 指标) → Recovery——指标在恢复外层，恢复出的 500 仍进 5xx
计数；堆栈走 logx 结构化日志（带 trace_id），响应走 mw.ErrorResp 信封
（与业务错误同形状）。测试：traceid_smoke_test.go 增
TestPanicRecoverySmoke——panic 路断言 500 信封 + trace 注入 + 引擎存活。
扫描裁定留痕（勿重探）：healthz/readyz 响应契约是 215 部署验证面钉住
的（oekit hertz/health 形状不同不换）；/healthz 已带 version（versionh
冗余）；per-token 限速是有界 sync.Map+x/time/rate（KeyedLimiter 换壳
无收益且改 429 文案）；llmProbeCache 已在 data/cache；internal/progress
已是 agentkit 总线薄别名（Tracker 是单任务阶段模型不对位）；ResponseMiddleware
会给信封加 trace_id 字段=契约变更不做；AccessLog 与静默纪律冲突不做；
SheddingMiddleware 是 CPU 型降载（argus 是 LLM-bound 非 CPU-bound）不做；
pathx 仅 glob 匹配/timex 日期转换/slicex 与 stdlib slices 重叠无债务。

### 修复(test)：v3.143.1 基线坏测修复——家族传播冲突夹具解耦+审计外送测试补锁

两例均在 v3.143.0 基线 worktree 原样复现（非本轮迁移回归）：
- runner_prevfindings：v3.133.0 起 SuppressPrint 家族传播一键全压，
  夹具三条同文件同行号的近重复措辞被连带抑制致 open 归零；
  被抑制意见挪到 src/b.go 与 open 家族解耦，测试意图不变
  （suppressed 不出现、open 保留）。
- audit_forward：httptest handler goroutine 写 got/gotAuth 与测试
  主 goroutine 轮询读构成数据竞争（race 实锤），补互斥锁+快照读。

### 修复(semsearch)：v3.133.3 软链读取逃逸——search_sem 泄漏宿主密钥的攻击链切断

安全实测（被盗取威胁模型）抓到的真漏洞：scanDir 缺 codesearch 已有的
IsRegular 守卫，WalkDir+ReadFile 跟随软链文件——恶意 PR 提交
link -> /proc/self/environ 即把宿主全部 token 读进索引/工具输出（本地
探针实测泄漏确认）。
- scanDir 只读常规文件（与 codesearch.IsRegular 同口径）；codesearch
  本就安全（探针转正为回归守住）
- 双包 symlink 守卫回归测试（探针转正：软链内容不得进索引/检索结果）

### 修复(storage)：v3.133.2 家族传播方向化——大杂烩桥不连坐真意见

v3.133.1 活体对照抓出的过度抑制：blob 行同时含硬编码+SQL 两段文本，是天然
的「桥」——无向 0.2 传播把 db.go:12 的 SQL 注入真意见连坐进抑制家族
（blob-vs-SQL=0.217 刚过 0.2），重审少了一条真问题。
- familyRelated 改有方向（anchor=已抑制行）：line=0 模糊锚不得向 line>0
  精确行传播（防桥接连坐）；模糊行之间 0.2 归族、精确侧 0.4+行距±2 不变
- 负控钉死：blob 随家族吸收（作为目标）但 SQL 精确意见必须活着；
  同函数不同问题负控继续守住

### 修复(storage)：v3.133.1 抑制家族传播——一键压掉同一问题的全部措辞变体

活体对照实验抓出的真缺口：同一问题四轮四种措辞（旧/新/大杂烩/第四版重述），
逐变体 suppress 是打地鼠——压二缺一，第四版吸收进还开着的变体行照常复活。
- suppressFamily 不动点传播：人工拒绝一条 = 拒绝全家族（双侧带行号走
  0.4+行距±2；任一侧 line=0 模糊形态走 0.2+文件组件相交）；0.364 的
  「同函数不同问题」反例被精确侧挡在外面（负控钉死不连坐）
- restoreFamily 对称恢复：suppress/restore 同一问题语义，不留「恢复一条
  其余仍被压」的半边状态
- SuppressPrint/SuppressPrintNear/RestorePrint 三入口统一接入（含预插分支）；
  实测家族（旧/新/大杂烩三行）一键全覆盖 + 恢复对称的端到端测试

### 修复(storage)：v3.133.0 模糊意见吸收层——彻底重述的形状漂移不再绕过抑制

2026-10-09 实测逃逸案例（t-623c）终局收口：多文件聚合（file="db.go,util.go"
line=0）+ 彻底重述 + 多问题拼接的模糊意见绕过全部三层。实测证伪「降阈值」：
同一问题重述 vs 同函数不同问题的重叠率区间互锁（0.237~0.319 vs 0.364，
反例更高），全局阈值无解——但模糊意见无行号锚点、主张精度低，对人工已判
误报的行按低阈值吸收是合理不对称。
- vague 吸收层：line=0 意见 + 文件组件相交（"db.go,util.go"↔"db.go"）+
  相似度 ≥0.2 + 仅 suppressed 行；0.2 与真实跨问题对（0.02~0.06）4 倍间距
- findNearPrint 查询面扩为 file IN（多文件组件都能看见对方的行）；
  精确意见（line>0）仍走 0.6/0.4 两级不动（同函数不同问题负例守住）
- 回归钉真实逃逸案例端到端吸收 + 双负例（精确不同问题/模糊跨话题）+
  度量钉死（同函数对 0.364 不过 0.4）；残余=精确意见彻底重写<0.4（文档如实）

### 修复(workflow)：v3.132.0 阶段进度可见——stage_progress 标记 + 推导主判据

有效性实测缺口之三：get_report 阶段进度运行期恒显示 R1（21 采样全是 R1）。
根因：TaskStages 推导只认产物（R1 看 checkpoint_r1_spec），而 RunReviewOnly
（code_review/visual_check）按设计跳过 R1/R3/R35——"跳过"被推导成"没做到"，
R2 的 checkpoint 反而在后（时间线里 R1 排前）。
- onStage 除 SSE 上报外落库 stage_progress 标记（复用 CheckpointSink 通道，
  checkpointSave 瘦身去从未消费的 task 参数），judgeAndGate 补 R5 进入标记
- deriveStageDone 纯函数抽出：进度标记为主判据（标记前全 done，天然涵盖
  跳过阶段），产物规则降为老任务回退/佐证，终态翻转不变
- 测试：进度标记语义（含 RunReviewOnly 跳过场景）/老任务产物回退/onStage
  落库；既有「LLM 失败不落库」断言按新语义过滤进度键

### 修复(mcp)：v3.131.0 restore 恢复通道三面——HTTP 端点 + restore_finding + 单源

有效性实测缺口之二：恢复抑制只有评论命令一条路，而 bot 自评论被 self-skip
——等于没有恢复通道，误抑制无法撤销（测试留痕也清不掉）。
- restoreFindingCore 单源（解析入库身份→置回 open→审计→PatternRestore），
  评论命令 handleRestoreComment 委派（行为不变）
- HTTP POST /findings/restore（/findings/* 前缀天然 admin 档，与 suppress
  同信任边界）+ MCP restore_finding（FindingRestorer 可选接口，admin scope
  透传二次判定，与 suppress_finding 同门）
- RestoreFindingScoped：admin 门 fail-closed + 入参校验（isPlatformPath/
  isDigits/fp 6-64）同 suppress 口径
- 文档 17→18 校准；suppress 耐久性文案随 v3.130.0 更新（重措辞已由宽松级
  吸收，彻底重写仍再现→再 suppress 一次连带建别名）

### 修复(storage)：v3.130.0 抑制剔除抗重措辞——suppressed 行宽松近重复级

有效性实测抓到的缺口修复：人工抑制后模型重写措辞即以新指纹复活（真实案例
argus-validate#2 db.go:9：「硬编码疑似线上密钥…」被改写为「新增了硬编码的
生产密钥…」，二元组重叠 ~0.43，0.6 严格级失守）。
- commentNear 拆 commentMatch(threshold) 单源：严格级 0.6（合并/别名，语义
  不动）+ 新增 loose 级 0.4（**仅 suppressed 行**）——重措辞复现并入被抑制
  条目延续抑制，普通行不放宽（防同文件近邻的新问题被吞）
- findNearPrint 三级优先：exact > near > loose；loose 命中同样记别名，
  后续轮次精确命中零扫描
- 回归钉真实案例夹具（strict=false/loose=true 落 (0.4,0.6) 带 + 端到端
  不复活 + 同位置不同问题不误吞 + 别名精确续命中）

### 修复(build)：v3.123.0 漏随补记——writing.ReviewDiff 单源执行体随 fe5d2f7

fe5d2f7 逐文件点名时漏掉 internal/writing/writing.go（ReviewDiff 自
service 下沉的承载文件），推上的树缺该函数 fresh checkout 编译断。

### 修复(comment)：v3.115.0 评论词表扩全平台 canonical——七后端统一 comment.created

- 排查结论（用户问询 SDK 是否有问题）：go-git-platform 仓本身健康——
  七后端事件词表内部一致（全部 EventTypeComment+action）、自身测试全绿、
  argus vendor 与其 HEAD 同步；词表 canonical 化是 SDK v0.72~v0.74 有意的
  规范化批次（事件语料库），系 argus 消费侧滞后未跟迁移
- v3.114.0 只覆盖 GitLab canonical+旧词表，GitHub/Gitee/Gitea/Forgejo/
  GitCode/TencentCode 的评论命令仍是死的（路由碰巧过、parse 按 GitLab
  形状解必失败）——本批 isCommentEvent 词表改 commentEventTypes（canonical
  + issue_comment/cr.note 兼容），parseCommentMeta canonical 分支直取 SDK
  归一化结构（Comment=评论体/CR=所在 MR）不重解 RawPayload，cr.note 保留
  GitLab 形状、issue_comment/空 Type 保留 issue 形状
- 回归测试：canonical 结构直取/无 CR 排除/非法号排除/旧词表两形状/system
  note 与 iid=0 排除

### 修复(comment)：v3.114.0 GitLab 评论命令链路修复——SDK 词表变更为 canonical comment.created 后 argus 仍钉旧词表

- 根因：go-git-platform GitLab 后端已把全部 note webhook 归一化为
  comment.created（canonical，regardless of target），isCommentEvent/
  parseCommentMeta 只认 issue_comment/cr.note → @触发词命令在 GitLab
  整条静默落白名单 skip（webhook 到得了、命令永不处理、零 ERROR）
  ——v3.113.0-215 部署验证期被 9 条自评论回声的 skip 日志暴露，v3.105
  及更早已存在（10-03 E2E 走 TaskAPI 未覆盖评论入口）
- 修复：gitlabNoteTypes 词表单源（canonical + 旧词表兼容），isCommentEvent/
  parseCommentMeta 两处收敛引用；无环风险核实——HandleComment 仅处理含
  @BotUsername 触发词评论，bot 自回声无命令即无副作用
- 回归测试：canonical 路由+解析/旧词表兼容/system note 排除/非 MR 排除

### 修复(security)：v3.103.0 gosec 扫描收口——G115 行号饱和钳制 + G301 沙箱目录权限收紧

- service: clampInt32（Finding.Line int→int32 饱和钳制，垃圾大数不再回绕成
  负行号污染 API 消费方）
- sandbox_verify/ocrshim: 污点可达目录 MkdirAll 0755→0750（gosec G301）
- 误报裁定全留痕入 docs/tech-debt.md：fixverifier G703（resolveSafePath 双侧
  EvalSymlinks 可靠）/fix.go G304（上游 safeRelPath 已挡）/G118（v3.91.1 独立
  回帖 ctx 刻意）/G401 dream sha1（非安全 slug 换哈希孤儿化记忆文件）
- govulncheck：零被调用漏洞；GO-2026-6505（otel otlptrace v1.25.0）import 层
  未触达，留待依赖升级批
门禁：lint/test(-race)/arch/golden eval 69 全绿

### 修复(output)：v3.91.2 输出产物深查修复——GitLab 报告评论重复 + 建议修复围栏乱码

对 om-gitlab 生产产物（MR 页面/报告正文）的深查揪出两个用户可见缺陷：
一、GitLab 同 head 重审报告评论重复（原地更新契约失效）
- 根因：sdkctx 当年只给 CreateIssueComment 特判了 DiffManager（MR Notes
  API），List/Update 漏改——GitLab 的 IssueManager 走 Issues Notes API
  对 MR 必 404，三级降级全灭，每轮新建
- 修复：UpdateIssueComment 加 DiffManager 原子换新降级（先 CreateNote
  成功后 DeleteNote 旧评论，建失败不动旧评论，绝不比现状差），返回新 ID；
  poster 三级定位统一经 repointIfSwapped 取返回 ID 并回写缓存
- 已知边界：换新=评论位置移到最新（非字节级原地更新）；真正原地更新需
  SDK 暴露 MR-note Update/List（go-git-platform 待办）
二、总结报告「建议修复」块围栏内出现 &#8203; 零宽实体、反引号被 ′ 替换
- 根因：EscapeUntrusted 是 markdown 正文流转义（行首结构标记前插零宽
  实体防伪造结构），塞进代码围栏后实体原样可见、复制内容被污染——围栏
  内本无 markdown 语义，转义帮倒忙
- 修复：reportview 建议修复块改围栏安全渲染（裸内容 + 按内容最长反引号
  串动态加长围栏），与 poster 行内 suggestion 的裸内容纪律对齐
测试：围栏安全渲染单测（零宽实体/反引号保真/围栏加长）+ 换新语义更新
两处 poster 流程测试（旧评论删除断言+定位回写断言）；fakePlatform 补
DeleteNote（换新路径触达，嵌入接口 panic 兜底金丝雀曾精准报警）。
门禁：make test（race）+ make lint + make arch + make eval（golden 68）绿。

### 修复(patch)：v3.91.1 巡查修复——fix 超时回帖独立 ctx + QA search_code 惰性化 + 沙箱孤儿回收

生产巡查（om-gitlab 实测后自查）发现三处缺陷 + 两个卫生项：
- runFixReply 回帖改用独立短 ctx：原实现复用执行 ctx，agent 撞硬上限时
  ctx 已过期，失败/成功回帖本身都发不出去（用户对超时零感知）
- QA search_code 惰性化：目录就绪（含冷克隆 30s 预算）从工具表构造期
  推迟到工具首调——不需要检索的提问不再付任何克隆成本；解析失败返回
  工具错误文本自然降级
- fix 沙箱孤儿回收：进程中途死亡时 defer 清理不执行，fix-* 目录遗留
  ——每次新建前顺手清 2h 以上旧目录
- README 版本行 v3.69.0→v3.91.0（口径改 origin/main，CHANGELOG 记至
  v3.69.0 属实标注）；.gitignore 收编根目录本地构建产物 /argus-copilot
门禁：make test（race）+ make lint + make arch + make eval（golden 68）绿。

### 修复(service)：v3.84.0 前置——Q&A 评论节流缓存回归 per-Service 实例

v3.82.0 引入 ekit data/cache 时把节流缓存改成了进程级单例，e2e 同进程
多 Service 场景互相污染 30s 节流窗口（四个评论类场景组合跑必挂，单跑
恒绿）。回归 per-Service 懒建实例（mutex 护并发首建），生产行为不变——
进程内恒一个 Service。

### 修复(core)：v3.75.3 深度代码审查七缺陷收口——批判审查实锤（3×P1 + 4×P2）

code-reviewer 批判审查（置信度≥80 实锤清单）逐项修复：
- P1 marker 契约漂移：MergeDescription 找精确串 begin --> 而 Render 写
  begin task_id=... -->，替换永不命中、describe 反复追加过期段——begin 改
  前缀常量单一出处，新增 Render→Merge 往返回归测试（含二次替换幂等）。
- P1 git add -A 污染：包装命令产物（编译二进制/coverage）与前轮失败残留
  会被一并 commit 推到作者 PR 分支——Apply 前先 reset --hard+clean -fd
  清场、只 add Apply 实际写入的文件集、commit/push 失败路径补清场收尾。
- P1 pipelineCreateTask 漏 in_flight 判重：同 commit retry pipeline（新
  delivery）以未落库 task.ID 回 accepted 并向 SSE 广播幽灵任务——补齐
  duplicate 响应（带 existingID），对齐 webhookCreateTask。
- P2 MergeDescription 丢弃管理段之后的作者内容——按 begin/end 区间替换，
  尾段保留（回归测试钉住）。
- P2 fixverifier symlink 逃逸：仓库可携带指向沙箱外的 symlink，WriteFile
  跟随即以 bot 权限写出沙箱——EvalSymlinks 双侧归一（dir 也归一防 macOS
  /var 误伤）+ 前缀断言，Verify/Apply 两路径接入。
- P2 字节截断腰斩 UTF-8：truncateDiff/repoGuideFile 改 textutil.TruncRunes
  （非法序列直进 LLM prompt 可能 400）。
- P2 samesha v1 滚动升级复活窗口：v2 建成后顺带 DROP v1（幂等），防旧副本
  重启复活 v1 误杀跨 kind 同 SHA 并发。
- P3 顺手：fix_push_failed 日志 step 名恒 -C 改 argv[2]。

### 修复(service)：v3.75.2 TaskAPI 入参校验收口——评测边界探测三缺口

深度评测边界探测实锤（215 生产 TaskAPI 实测）：
- SQL 注入形态 owner（a' OR 1=1--）被建成真任务——owner/repo 无格式校验，
  任意字符串入库并进入平台查询路径。加 isPlatformPath 白名单（字母数字
  . _ - 与子组斜杠，对齐 GitLab 组名规则），拒掉引号/空白/unicode 异体。
- 20 位超长 number 被建成任务（白跑一轮 API+重试链）——isDigits 加长度 ≤10。
- artifacts 任意大小可直灌 DB（DoS 面）——序列化总量 ≤16KB 上限。
三个垃圾任务已人工终止（fail_reason 留痕）。回归测试 8 用例钉住。

### 修复(writing)：describe 管理段 task_id 移入注释内部——此前拼在 --> 外会漏成 MR 页面可见文本（215 实测发现）

- 202bd43

### 修复(storage)：v3.74.2b 同 SHA 在飞唯一索引升 v2（含 kind）——DB 层防线与代码层去重对齐

端到端实测：代码层去重加 kind 后，DB 层部分唯一索引（v1，不含 kind）仍拦
同 PR 同 SHA 的第二个 kind 任务（UNIQUE constraint failed 500）。索引升 v2
（platform,owner,repo,number,kind,head_sha WHERE status IN pending/running），
启动时 DROP v1 幂等升级。MySQL 路径不变（事务锁防线，无 partial index）。

### 修复(storage)：v3.74.2 任务去重键补 kind——多 kind 并存语义收口

端到端验证发现：describe（kind=3）pending 期间发 changelog（kind=4）被判重
返回同任务。三处缺口同源修：
- CreateTaskDirectIfIdle / CreateTaskForWebhook in-flight 判重加 kind——
  同 PR 不同 kind 可并存（审查 15min 在飞期间写作任务不再被吞）。
- last_head_sha 规则加 isReviewKind 门：LastHeadSHA 仅被 poster.Post（审查
  回帖）写入，写作/归因任务不写也不继承——否则「MR 先审完、CI 再挂」的
  真实序列里同 SHA 的归因任务会被误挡。
- duplicate-key 回查两处同款加 kind。回归测试 TestDifferentKindsCoexist
  钉住并存+同 kind 仍拦+新 SHA 正常建三语义。

### 修复(plugin)：v3.70.1 mcp-server 编排改异步轮询——对端 transport WriteTimeout 60s 撞墙收口

215 实测：wait=true 同步等 101s 审查，对端 mcp transport WriteTimeout=60s
（review-service internal/mcp/transport/transport.go:70），handler 返回时写响应
已超 deadline → 连接 EOF。timeout_seconds 传多大都绕不开对端 HTTP 层的墙。
create_review 改 wait=false 异步（立即回 queued+review_id）→ get_review 轮询
终态（10s 间隔，success/success_with_publish_failed 收口、failed 上抛带
review_id、超预算上抛不盲等）→ get_review_findings 取明细。每次请求都是短
请求，不受对端写超时约束；这正是服务端 create/get 分离契约的设计意图。
e2e 场景 25 stub 同步切编排契约。

### 修复(plugin)：v3.70.0 remote/mcp 对接 review-service mcp-server 编排契约——三层联调收口

215 实测三层根因：①endpoint 127.0.0.1:8082 指容器自身 loopback（配置层已修）；
②MCP 面要 Bearer（auth_token_env 已配）；③契约错配——argus 单 payload 参数 vs
服务端 create_review 平铺 provider/repo/head_sha + findings 走独立工具。
mcp 协议按 manifest.Needs.WorkingCopy 分流两条 wire 契约（均为生产形态）：
- working-copy（ocrshim sidecar）：旧 payload 契约不动（shim 无平台凭证，
  必须由 argus 喂 workcopy_dir）——分流键取 needs 而非 tool 名（语义分界）。
- 非 working-copy（review-service mcp-server）：create_review 平铺参数
  （provider 归一小写 + gitlab→merge_request_iid / 其余→pull_request_id，
  wait=true 同步等 600s）→ get_review_findings 取明细；title+body 拼回
  comment，suggestion→suggested_fix，summary→notes。
- 服务端 wait 超时按插件失败上抛走 Merger 容忍+failed-retry，不自建轮询。

### 修复(sdkctx)：通用平台分支补 Logger 注入——GitLab/Gitea 走此路径，此前 SDK 告警进 noop 黑洞（昨日 awk 去重误删）

- 1e5f4a4

### 修复(poster,builtin)：v3.51.0 行内评论从未命中根因修复

- 根因：模型路径缺 git 仓根前缀（两仓 100% 复现），poster 文件级预校验恒 miss，
  行内评论自试点以来从未发出（四 MR discussions 全零实锤）
- poster 唯一尾段匹配兜底（Included 域、段边界、多命中放弃）+ 剔除数进 Degraded
- R4 提示词注入【路径纪律】（ReAct/单轮同款），治本降幻觉

### 修复(workflow,eval)：v3.24.0 夹具 v9——扩容 45-48 + scope 闸门 RepoExcluded 缺口修复

fix(workflow)：R5 scope 闸门此前把仓库级排除文件（RepoExcluded）当在域内，
排除文件上的 high 可一票否决 R5.2，与「排除文件不进审查视野」的仓库主权语义
矛盾（47 号夹具暴露）。修复 = scope 构建跳过 RepoExcluded，与 v3.8.0 越界
降级同一哲学：未审查的文件不能驱动否决。
扩容 45-48（集合 44→48，真实分母 37→40），四个既有能力首次入册：
- 45 reviewN best-of-N（G3）：3 采样复现 3/3 + low 确定性升级 → R5.4；
- 46 review.min_severity：仓库阈值剔除 low → approve + 降级注明；
- 47 review.exclude_paths：生成物出视野，其上 high 经 scope 闸门降级；
- 48 completeness_retry：插件级失败→补审成功替换→无完备性注记（skip_real；
  归因在案：agentrun 回喂重试会消化单条 garbage 序列，插件级失败须以
  连续 garbage 耗尽内部重试方可触发工作流级补审）。
harness：must_find/must_not_find 失败信息携带报告截断（对齐 verdict 路径）。
打桩 48/48（clean 5/5、FP=0、boundary 2/2）；build/vet/test 全绿；真实模式
增量 45-47（mimo-v2.5）3/3 命中，快照 v9-additions-45-47.json。

### 修复(review,deploy)：v3.9.1 学习环记账口径修正 + P2/P3 快修 + 发布/文档批

- 学习环：PatternPosted 先于 SoftFloor 标注（标注文本不再进 PatternKey）、
  记账/匹配统一用 prints 行原文 probe、regret 的 By 取首段（plugin.FirstBy
  统一口径）、workflow 低置信重放先于交叉标注；e2e 场景 17 阈值随记账修正
  调 0.55（旧场景依赖错账压低置信度）
- consensus：成员失败复用 teardown 守卫（停机取消不计熔断）+ 日志/黑板 note
  整条 Redact
- storage：PrintAlias 幂等写入（跨 PR 同款漂移撞主键→UpsertPrint 回滚）；
  短码前缀 hex 白名单 + 歧义不猜 + 别名兜底限定本 PR；辅助表 Prune 分批 ×3；
  EnsureSameSHAGuard 真实错误透出；SQLite DSN 带 ? 时 pragma 以 & 追加
- qa：AnswerAboutReview 消费 ARGUS_QA_MODEL；runner：skillText tool 模式缓存
- 文件名消毒上收 plugin.SanitizeFileStem（ocrshim 留档/dream 导出/ocrdeleg）
- 发布/文档：镜像 tag 统一 v3.9.1（旧 tag 下 ocrshim sidecar 崩溃循环）、
  k8s 补 SINK/OCR_BIN 落点、deploy README/runbook v3.9 形态与场景 11、
  README env 表/能力/版本口径、review-service-integration.md 重写为现状、
  Makefile build-ocrshim、sink 功能补记

### 修复(timeout)：OPENAI_TIMEOUT 默认120s→300s

mimo-v2.5-pro 推理模型单次 LLM 调用需 40-60s，加上长 prompt（diff+skill+摘要）
容易超过 120s HTTP 超时。改为 300s 与插件超时对齐，correctness 插件不再超时。

### 修复(ocr)：命令从 'diff' 改为 'review' + 路径适配本机

ocr CLI 没有 diff 子命令，正确命令是 ocr review --format json。
默认路径从 /opt/argus/bin/ocr 改为 /Users/demo-user/bin/ocr（本机安装位置）。

### 修复(timeout)：correctness 插件超时120s→300s

mimo-v2.5-pro 推理模型响应较慢，120s 超时导致 correctness 插件未参与审查。
与 requirement-compliance（600s）对齐，给 builtin code-review 插件充足时间。

### 修复(evidence)：ValidEvidence 支持多条逗号拼接+括号说明

LLM 返回的证据格式多样（如 config/config.go:72-73 (yaml.Unmarshal), config_test.go:42-54 (TestLoad_YAMLFile)），
原 ValidEvidence 只取最后一个 : 后的内容，被括号/逗号污染导致正则失败，implemented 被误降级为 unverifiable。
修复：逗号分割后逐条校验，括号说明先剥离再匹配；任一条合法即通过。
新增3个断言：多条拼接、带括号单条、纯中文拒绝。

### 修复(polish)：报告反注入 + poster 扫描兜底 + builtin 并发安全

poster: BotUsername 字段 + 扫描兜底发帖（找不到已有报告时新建）
reportview: EscapeUntrusted 中和 LLM 输出中的 markdown 结构注入
builtin: compliance workflow runner 接口抽取（适配 MCP/A2A 重入）
service_test/e2e: 测试补充

### 重构(structure)：v5.4.5 sdkctx 门面按域四文件拆分 + 两项评估降级存档

- sdkctx.go 536 行按域拆分（同包纯组织性，零 API 变化）：sdkctx.go 209（核心：
  构造/注册表/GitHubApp 面）+ sdkctx_calls.go 236（call 泛型样板+GetCR/issue/
  comment/review SDK 封装调用群）+ sdkctx_repoconfig.go 81（ResolveBranch/
  LoadRepoConfigAuto/branchCache/LoadRepoConfig 族）+ sdkctx_webhook.go 37
  （DeliveryHeaders/ParseWebhook）；-race 全量复检绿
- fake provider 收口评估后降级不做（v5.4.4 遗留候选）：五处 fake 方法集各异+
  带状态记账，共享包间接成本 > ~100 行去重收益；e2e fakeProvider 巨型有状态
  单点本就该保留；arch 只扫生产 import（go list .Imports）的事实顺带入档
- webhook 准入链拆包评估后不做：312 行耦合面窄，接口化 8 依赖成本 > 收益，
  留 service 门面（webhook 主链路本就是 service 核心职责）
- tech-debt：大文件表更新（sdkctx 条目销账）+ v5.4.5 节（两项勿重探结论）
门禁：make lint + make arch + make eval + 全量测试 + go test -race 全量，零失败

### 重构(structure)：v5.4.4 评论指令词法层剥出 internal/commentcmd + 全仓 -race 首检全绿

「可破坏结构」授权下的第一刀：@bot 评论指令「长什么样」与「怎么执行」分层。
- 新包 internal/commentcmd（纯函数零依赖）：cmdCatalog 指令目录单源（help 渲染/
  门控判定/冷却提示共用一张表）+ CutCommand 词边界/ParseFPHex 短码解析 + mention
  词法群（句首/句中/句尾触发、引用行豁免、词边界防误命中——extractQuestion/
  stripMentions 等 7 函数）+ 8 个指令解析器（review/recheck/fix/describe/
  changelog/testgen/suppress/restore，RecheckKind 三态裁决）+ 评论 webhook 事件
  形态折叠（IsCommentEvent/ParseCommentMeta/NormalizeCommentEvent，canonical
  comment.created 与旧词表 issue_comment/cr.note 归一为 Meta）
- service 门面瘦身：9 文件 ~500 行词法定义迁出，52 调用点改 commentcmd. 前缀，
  留纯执行体（dispatch*/handle*/HandleComment 编排）；helpText 改纯函数
  HelpText(fixEnabled)；3 个词法测试文件随迁 commentcmd 包（零 Service 构造成本）
- arch 白名单登记 service→commentcmd 边 + CHANGELOG v5.4.4 节 + architecture-
  deep-dive 分层目录表同步（TestLayering 流程：确认方向→更新白名单→双文档记档）
- 全仓 go test -race 首次全量：42 包零告警（并发正确性过检，重要质量基线）
门禁：make lint + make arch（分层）+ make eval（golden）+ 全量测试零失败

### 重构(deps-recycle)：v5.3.2 上游能力回收批——TokenSource 迁移/shellx 转义/探针 ctx 贯穿/Nop 净化

随 v5.3.1 依赖升级后的能力回收（go-git-platform v0.100 + oekit pkg v0.31/logx v0.8）：
- GitHub App 认证重写：githubAppRefresher（50min ticker+provider 重建）+
  appTokenManager + SDKCtx.Stop + 停机收编全删（净 ~120 行），换上游
  Config.TokenSource（installationTokenSource：每请求取值+临期 5min 懒续期，
  刷新失败退旧值）；顺修 token 过期→重建完成间在飞请求 401 的正确性窗口；
  启动期 eager 首取保留（fail-fast 不变）；3 用例覆盖快路径/退旧值/过期报错
- {dir} 注入 shell 模板转义：fixverifier+sandbox_verify 两处直拼 sh -c，
  路径含空格/引号/元字符时验证命令静默跑错；收口 plugin.ResolveShellTemplate
  （shellx.Quote）+ 7 组表驱动用例
- readyz LLM 探针 ctx 贯穿：checker 的 ctx 此前丢成 Background（取消传不到、
  trace 断链），改 NewRequestWithContext
- qa_session CAS 重读吞错补 Warn（unmarshal 失败静默截断会话历史为仅本轮）
- ConditionalRequests 平台开关：ARGUS_<PLATFORM>_CONDITIONAL_REQUESTS（默认关），
  GitHub 304 不计限流预算，poller 常开场景可持续轮询速率实质抬升
- 测试日志净化：97 处 NewSlogLogger→logx.Nop()（v0.8.0 新原语，断言失败不再
  被日志淹没；e2e 留真 logger）；service/qa 双包 noopLogger 同构删除（tech-debt
  不做项随上游 Nop 到位销账）；parseFindings 手写数字解析→strconv.Atoi
- docs/tech-debt.md 记 v5.3.2 批 + 3 条遗留 issue（cli.App 收编/LoadRepoConfigAuto
  去重/WebhookConfigManager 功能机会）；.env.example 补新键说明
门禁：make lint（vet/staticcheck/gofmt）绿 + 全量测试零失败；43 文件 +290/−307

### 重构(core)：v5.1.0 深度重构批——TaskKind 间接层清偿+deadcode 归零+render 拆分+spill 清理周期化

机械扫描定靶（deadcode -test / >100 行函数 / 跨包重复函数体 / 超规范文件）：
- TaskKind 间接层清偿：v3.106.0 词表下沉 domain 时留的 plugin re-export
  别名（19 消费点）全仓直连 domain，plugin/taskkind.go 删除——间接层
  完成使命归零；arch 白名单同步（poller/service/runner/workflow→domain）
- deadcode 归零：plugin/testlog_test.go（testNopLogger 21 方法，测试已迁
  logx 后全死）删除
- impactview render.go 575 超规范拆分：render.go 306（文本面）+
  mermaid.go 333（图渲染面）——图面被 e2e 快照与模型提示词双重消费，
  文本面只服务评论/报告，生命周期不同步
- pruneSpillRoot 周期化：每任务 wireWorkflow 全目录扫描（任务风暴下
  O(N²)）挪 bootstrap spill-prune ticker（与 workcopy-scavenger 同周期）
- v5.0.0 注释漂移清理（文件面退役后的残留措辞）+ tech-debt 基线复测
  （非生成 32457 行，新增包机械扫描确认纪律在线）
复判不做留档：loadRepoConfig 双份（语义不同构）/recvTypeName 8 行小件/
>100 行 4 函数（组合根铺开型裁定）。全量 go test 42 包绿

### 重构(core)：v5.0.0 大版本——四库深度收敛（oekit/oagentkit/agent-harness/agent-memory）

三路系统重叠盘点后按「机制面收编、领域面保留」实施，净删 262 行（+282/-544）：
- 依赖：agent-memory v0.9.0（向量轨/查询理解；argus 用法零改动）
- spill 收编（harness）：R3 大输出两级化进程内 map→磁盘存档
  <WorkRoot>/spill/<taskID>/，checkpoint 复活/重启后 get_detail 不再落空；
  ref 白名单挡注入；7 天过期清理；文案与工具壳零变化
- memory 文件面退役（agent-memory）：internal/memory 包删除，内核唯一路径
  （⚠️ MEMORY_KERNEL=0 时记忆注入消失——兜底文件面不复存在）；
  Ref/SingleLine 等存活符号收进 memorykernel，双写双读收编单路径
- 语义召回轨（agent-memory v0.9）：EMBEDDING 配置即接通 sqlite 内置向量索引
  +RRF 词面融合，失败降级词面；适配器在装配侧，memorykernel 保持 L1 纯净
- 指标中间件收编（oekit/middleware.PrometheusMetrics 取代手写 httpmw）：
  ⚠️ 标签 status→status_code，监控面板引用需同步
- 评估后不采纳留档：semsearch→codeintel（search_sem 无评测护栏）、
  SaveFact 取代链（同 slug upsert 下证据链本闭合）等
全量 go test 42 包绿

### 重构(idl)：v3.189.0 IDL 收敛二批——repo-settings 四路由收编生成面+删 /ping 遗留

- v3.187.0 手挂的四条按仓配置路由（GET /repo-settings + GET|PUT|DELETE
  /repo-settings/:platform/:owner/:repo）收编进 idl/argus.thrift 新
  RepoSettingsService，生成路由承载；hz 文件名映射恰好命中既有
  repo_settings_service.go，四个 handler 函数零搬运（仅清 hz 机械追加的
  未使用 import + 头注释改口）
- middleware 四叶子全挂 authMw（requiredScope 按 method+path 判定语义不变；
  PUT body 仍原始字节透传 service 层 DisallowUnknownFields 严格 Parse）
- 删 /ping 冗余端点（hz 脚手架遗留，与 /healthz 重复；测试/文档零引用）：
  router.go 手挂行 + biz/handler/ping.go + handler import 一并清除，
  customizedRegister 从此只剩 /events（SSE）
- IDL 头注释/文件头注释/CHANGELOG 同步；全量 go test 43 包绿

### 重构(idl)：v3.188.0 IDL 收敛——六条手挂业务路由收编生成面+生成物手改失真修复

- idl/argus.thrift：TaskKind 枚举补 DESCRIBE/CHANGELOG/CI_ANALYSIS（此前直接
  手改 thriftgo "DO NOT EDIT" 产物而 IDL 无声明，hz update 一次即编译炸）；
  CreateTaskResponse.duplicated 正式声明为字段 3（幂等语义入契约，生成后
  *bool，service 两处适配 GetDuplicated/ptrx.Of）；删死字段
  GetTaskResponse.reportRef（v3.1 预留从未赋值，编号 5 注释保留不复用）；
  头注释声明路由分工——infra 探针/MCP+A2A 协议面/SSE /events 刻意不进 IDL，
  业务端点此后不得绕过 IDL 手挂
- 六条散落手工注册的业务路由收编进生成路由承载：GET /reports/:ref、
  /usage/monthly、/stats/overview、/tasks/:taskId/impact、/reports/health、
  POST /findings/restore；middleware 新路由全部挂 authMw（requiredScope
  语义不变）；handler 归位 task_service.go（hz 按 service 名组织文件），
  report_service.go/impact_view.go/health_handler.go 撤销；RestoreFinding
  改绑 IDL 生成 struct（与 suppress 同款薄壳）
- router.go/main.go 删被收编手挂行；e2e 栈删 /reports/health 重复注册
  （生成路由已承载）；design.md §5 变更段 + CHANGELOG 记档；全量 go test
  43 包绿

### 重构(impact)：v3.186.0 收编 textx v0.7.0 PreciseCallers——删本地等价精化实现

上游 textx#15 合并发版 v0.7.0(此次收编为本仓上游下沉惯例的闭环:本地
symbols.go 曾自实现「token 候选 + AST 精化」双层,上游合并后删除等价代码):
- collectSymbols 改走 idx.PreciseCallers(单点),删除本地 refineCallSites/
  walkDeclCalls/callSite/callerLabel(后者系早期写入未接线的死代码,顺手清偿)
- funcdiff.go 保留自有源码级助手(funcDeclName/recvTypeName/calleeName/
  srcSeg——它解析 BEFORE/AFTER 原文而非经 Index,与上游 API 正交)
- 队列地板顺抬 queue v0.6.4(worker task_panic 日志补全栈,queue#16)
- 全量回归 + impactview/runner/storage -race 全绿;L2 行为语义不变(上游
  即本地实现的下沉版,同「宁缺毋假」口径)

### 重构：回收本地 shell 转义兜底, 改用 OEKIT 上游原语 (pkg/shellx v0.28.0)

下游兜底已经提到上游: oekit/pkg 新增 pkg/shellx (Quote / QuoteArgv,
POSIX 单引号包裹 + 空串保位), 发版 v0.27.0, issue #112 已回执关闭。
本仓的 internal/testkit 包只有一个 Shq —— 与上游 shellx.Quote 逐字等价
(单引号包裹 + 内嵌单引号按 '\'' 闭合转接), 是同一份转义逻辑抄了第二遍。
现在 9 处调用点直接用 shellx.Quote, testkit 包随之删除(它只剩这一个函数)。
测试相应收窄: 转义语义由上游 round-trip 测试兜底(含真实 sh 往返), 下游三个
测试包只保留自己的业务断言。行为不变: 输出逐字一致。
仅动 internal/ocrshim/e2e_test.go、internal/runner/runner_ocrdeleg_test.go、
internal/ocrdeleg/delegate_test.go, 并删除 internal/testkit 包, 不改其它文件。

### 重构(argus)：取指针助手收编 oekit/pkg/ptrx

internal/service/service_dto.go 的 strPtr / strPtrOrEmpty 助手保留（「空串→nil」
语义留在本地），指针构造改为 oekit/pkg/ptrx.Of——与原 `&v` 语义等价。
对外契约不变：HTTP 路由、请求响应结构、hz 生成的 DTO optional 字段形态均未动。

### 重构(argus)：在途去重表收编 oekit/pkg/container

fixInflight（sync.Map → container.Map[string, bool]）：@argus fix 在途去重表
（key=pr+fp，防同 finding 并发双跑烧双倍 agent token）键值类型化，取用免 any 断言；
LoadOrStore / Delete 语义逐字等价。
取值字段，零值即可用——Service 的各构造点（bootstrap 装配与各单测）无须逐个初始化。
对外契约不变：HTTP 路由、请求响应结构、状态机取值均未动。

### 重构(argus)：测试路径校验收编 oekit/pkg/pathx

sanitizeTestPath 的路径合法性判定改用上游 pathx.SanitizeRelStrict：拒绝越界
（显式 ".." 段）、绝对路径、Windows 盘符、UNC 与反斜杠分隔符——"\" 统一按分隔符
解释，不按运行平台（"..\\.." 形绕过此前在 Linux 上按平台解释会漏过）。
行为差异（相对此前子串判定是收紧）：
- 此前 strings.Contains(p, "..") 会误杀含 ".." 的普通文件名；上游按路径段语义
  判定，"a..b.go" 合法。
- 新增拒绝 UNC（//host/share）与 Windows 盘符形态。
函数返回语义不变（不合法时返回空串）。

### 重构(memory)：v3.178.1 memorykernel 内聚清理 + 拆分后文档漂移归位

- 死代码清理：Kernel.rec 字段删除（openWithStore 预建 recall.Pipeline 只存
  不读——recallOne 按预算逐次建，预建实例零消费；字段级死代码，deadcode
  -test 探不到这层，静态专项扫出）
- query.Analyzer 包级单例复用（纯只读配置态 New 后不变，省逐次分配）
- 测试债补齐：SaveFact Kind=FactLesson 时效语义钉 + recallCfg Analyzer
  接线钉（v3.177.0 升级此前无回归面）
- 文档漂移归位：deep-dive/plugin-authoring 两处 Dispatcher/Merger/Registry
  代码示例改指 dispatch（v3.173.0 拆分后遗留的当前形态示例）
- staticcheck U1000/U1001 全仓零命中复核；四门禁全绿 golden 69

### 重构(service)：v3.178.0 commentSlots 收编 syncx.Gate.TryAcquire——留痕缺口闭环

- concurrency v0.20.0 补 TryAcquire（非阻塞档：满额即拒不排队），正是
  v3.175.0 留痕「commentSlots 不接 Gate（fail-fast 忙拒收语义不合）」缺的
  那半——上游补齐即收编：SubmitComment 的 select/sem/default 忙拒收换成
  TryAcquire（同语义 + release 幂等防双归还），webhook 线程池防拖垮行为不变
- SanitizeFileName（pkg v0.21.0）评估不接留痕：与 textutil.SanitizeFileStem
  是不同问题域（完整文件名消毒 vs 标识→stem 分隔符归一）；输入侧已有
  白名单消毒，改文件名口径会让存量留档孤儿化——风险>收益
- 四门禁全绿 golden 69

### 重构(deps)：v3.175.0 两原语收编——路径净化 pathx.SanitizeRel + 并发闸门 syncx.Gate

- fix_render.safeRelPath 手写路径护栏（abs/越界拒绝）收编 pathx.SanitizeRel
  （宽松档同语义，额外覆盖反斜杠分隔符+Windows 盘符两类逃逸形态）；fixverifier
  的 resolveSafePath 保留裁定（EvalSymlinks 文件系统级校验，pathx 文本级不替代）
- dispatcher 手写并发信号量（sem chan + select/ctx.Done）收编 syncx.Gate：
  Acquire 等待可取消+release 幂等，同语义多一层防双归还；熔断 gen 配对纪律
 （取消=Abandon 释放半开探测位）逐字保留
- commentSlots 不接 Gate 留痕：评论闸是 fail-fast 忙拒收（排队会把 webhook
  线程池拖垮），Gate 是等待-或-取消语义，无 try-acquire
- 四门禁全绿：lint/test -race/arch/eval golden 69

### 重构(arch)：v3.173.0 plugin 契约/引擎内聚拆分——引擎面独立 internal/dispatch

- 高内聚：internal/plugin（5489 行）拆为契约层 plugin（types/contracts/findingswire/
  config/reminder/fence/severity/prkey/sanitize/completeness/taskkind/reporeview/
  hooks/observe——全仓公共类型与提示词卫生面）与引擎层 internal/dispatch
 （dispatcher/dispatcher_run/registry/bestofn/squad_consensus/merger——路由、
  熔断配对、best-of-N、编队共识、合并管线，1629 行）
- 低耦合：依赖方向单一 dispatch→plugin（引擎经类型别名桥+函数值桥引用契约，
  零逐点限定符改动）；下游 9 包 16 文件引用面 plugin.X→dispatch.X 机械替换；
  arch 白名单显式登记 dispatch 依赖集（agentrun/blackboard/llm/plugin/storage）
- 架构收益：下游只拿契约不再传递依赖引擎；引擎改动不再波及纯契约消费方
- 零逻辑变更：dispatch 测试套（9 文件）+plugin 测试套（6 文件）+e2e 全绿钉住
- 四门禁全绿：lint/test -race/arch/eval golden 69

### 重构(codegraph)：v3.171.0 纯别名门面清偿——删包，调用点直连 textx/codeintel

- internal/codegraph 整包删除（v3.78.0 下沉后遗留的类型别名+var 直通门面，
  20 行零逻辑）：runner/inject 与 workflow/requirement 共 3 处消费点直连
  github.com/oagentkit/textx/codeintel
- 架构白名单同步：删 codegraph 包条目与两处依赖登记（arch 测试当场抓出
  同行共列误删 config/context/codesearch——按行 perl 删的坑，已按实际
  import 面逐项复原）
- 保留语义：artifact 键 codegraph_touched/日志键 argus.codegraph.*/工具
  描述文本不动（观测与数据契约）；learning 门面保留裁定（真域模型
  Config/PatternState/阈值 + 39 消费方，非纯别名）
- 四门禁全绿：lint/test -race/arch/eval golden 69

### 重构(structure)：v3.167.0 runner/fix 双拆——全部源文件归 400 行纪律内

- runner.go 576→272 + runner_lifecycle.go 327：run() 的四个命名子步骤
 （prepareContext/buildPluginTask/recordRegret/finalizeAndAnnounce）与两道闸
 （repoBudgetGate/handleRunFailure）独立成文件；runner.go 只剩结构体+Execute
 编排+execContext/wireWorkflow 接线
- fix.go 497→178 + fix_sandbox.go 120（克隆沙箱→agent→验证→清扫执行面）+
  fix_render.go 224（prompt 组装/回帖渲染/gitDiffCapped 采集/parseFixEnvelope
  解析助手）
- 纯移动零逻辑变更（service 16.8s + runner 测试套全绿钉住）
- 四门禁全绿：lint/test -race/arch/eval golden 69

### 重构(mcp)：v3.165.0 mcpserver 按关注面三拆——645 行契约面归 400 行纪律内

- server.go 645→294：New() 工具注册 + Core 接口面（组合根语义）
- server_tools.go（112）：19 工具 schema 构造器（reviewPrTool..askArgusTool）——
  schema 文案是 get_capabilities/skills 文档的外部契约面，独立成文件便于
  与 skills/argus-mcp/references/tools.md 对照审阅
- server_handlers.go（196）：工具执行体（runReview/pollReviewTask/
  renderTerminalTask/taskView/decorateStages）+ 参数助手（requireArgs/
  argString/argBool）+ fp 常量
- bridge.go（73）：HertzBridge streamable 桥 + bridgeWriter（协议载荷免信封）
- 纯移动零逻辑变更（mcpserver 1204 行测试套全绿钉住）；octopus 状态码契约
  评估留痕：SendMarkdown 返回 HTTP 状态码是业务记账数据面非错误面，
  httpjson 非 2xx-as-error 语义不合，不收编
- 四门禁全绿：lint/test -race/arch/eval golden 69

### 重构(qa)：v3.162.0 token 估算收编 textx/compact 单源——修正英文内容 ~2× 高估

- qa 预算估算三处（轮次/摘要/压实产物）从手写 ceil(runes/3) 切
  compact.EstimateTokens（逐 rune CJK/ASCII 分类计量）：旧公式是全 CJK
  上界，英文型问答内容（代码标识符/英文注释）被系统性高估 ~2×，
  Critical 门提前触发白压实（压了不需要压的历史）
- 上游第十轮审计正修的就是这类全 CJK 上界（中文≈0.67、英文≈0.33 token/rune
  各归其位）；「与 compact 同口径」的口头同源从此是代码同源
- textx 六包至此全部消费（textutil/diffx/codeintel/conversation/reportutil/compact）
- bashguard 评估不接留痕：testgen 沙箱验证命令是部署方配置（可信面）非模型
  生成，argus 全链无模型提案 shell 的执行点，静态裁决无消费场景
- 四门禁全绿：lint/test -race/arch/eval golden 69

### 重构(security)：v3.161.0 手写注入转义收编 guardx/injectband——双源正则归一

- plugin/reminder.go 的 nestedReminderRe（手写 `(?i)<\s*/?\s*system-reminder`
  正则+转义）与 guardx/injectband.SanitizeBody 是同一判据的两份实现——上游改
  判据时本包会静默漂移。收编为上游单源，ReminderNote/EscapeReminderTags 门面
  保留（上游没有的是 kind 标签 + NOT USER INPUT 反伪装声明两样信封语义）；
  逐字节等价由既有 reminder 回归测试钉住（qa/requirement 三包 32 处调用零改动）
- 死代码复核：deadcode -test 全量零命中 + staticcheck unused 零命中——
  v3.155.0 归零后保持；config_accessors 149 行全部活代码（llm.BudgetConfig
  派生单源，19 处消费）
- 评估不收编留痕：DataFence 的 neutralized 计数观测钩子（上游 fence.Data
  纯函数无观测，argus 侧是增值面非重复面）
- 四门禁全绿：lint/test -race/arch/eval golden 69

### 重构(factory)：v3.121.0 webhook 回执词表工厂 + 设计模式 12 点饱和勘察落账

- wh* Status 词表常量 + whResp 构造工厂：14 处 WebhookResponse 字面量收口
  （accepted/busy/duplicate/skipped/error），API/SSE 消费方按此分发——
  字面量漂移即静默契约断裂的通路关闭；dupResp/whResp 分工（duplicate
  专属回填 vs 通用构造）
- 12 点设计模式再勘察全评估落账 tech-debt.md：notify/IDL/降级链/注入管线/
  命令分发/准入序列/BuildModels/prepareInline/R 系列/状态机/runner writing
  ——全部为已模式化、DB 级更强守卫或形式化负收益，模式容量饱和结论在案
- 教训：perl 词表替换遇多字段字面量会跨字段贪婪匹配产生断码——
  机械替换后必须逐点回读+编译验证（本批 4 处断码当场修复）

### 重构(dead)：v3.113.0 冗余旧代码清偿——八矿脉扫描后删四项

- qa.searchCodeToolFor：只有测试在调的生产平行壳（无 -test deadcode 口径
  抓获，v3.97.0 先例），删除后测试改走 newSearchCodeTool 真构造器并升级为
  真实执行断言（模式命中 + 目录不可用降级文案）
- plugin.PRContext.IsDraft：只写不读死字段（draft 跳过语义在任务源层直读
  cr.Draft，PRContext 从未消费）
- writingFakePR.CreateIssueComment：死测试覆盖（SDKCtx 写路径 DiffManager
  CreateNote 优先，永不触达）+ 同包 errorString 与 errReply 重复合并
- 扫描结论留痕台账：config 115 字段全活/导出常量零死项/跨包重名函数仅
  parseFindings（语义不同）与 tail（7 行×2 按 noopLogger 先例不新开共享位）/
  测试助手全活/无注释代码块

### 重构(dedup)：v3.111.0 工具错误回注信封单源 + search_code 构造器合一

- plugin.ToolErrNote/NoteToolError：工具失败回注信封单源——qa/requirement
  两包 7 处 ReminderNote("tool-error", err.Error()) 收一，标签字面量漂移
  分裂 ReAct 错误识别口径的通路关闭
- newSearchCodeTool(fetch)：search_code 惰性/绑定目录两构造器唯一差异
  （目录解析时机）参数化，InferTool 壳与降级文案不再双份
- tools() 完整 spec 表裁定不做：各工具 In/Out 泛型互异，硬同构负收益

### 重构(template)：v3.110.0 describe/testgen 单轮写作命令模板方法

- runWritingCommand 模板：回复模型守卫→diff 拉取→提示词装配→LLM 直调→失败
  记账回帖→截断留痕→装饰段→评论回执，骨架此前两份拷贝各自演化无从测红
- 差异面全收 writingSpec 钩子：build（diff→提示词对，failMsg 拒绝通道覆盖
  testgen target 空命中专属文案）、decorate（testgen 沙箱验证段/固定尾注）；
  cmd 单字段派生回执名/marker/日志前缀/LLM stage（大写化）
- ReviewDiff 裁定不入模板（Model 字段与错误返回语义不同，硬套负收益）
- 补六场景端到端打桩测试（此前 handleDescribe/handleTestGen 零直接锚点）：
  回执体 marker/固定尾注/build 拒绝/LLM 失败回帖/diff 缺失/无模型静默；
  fake 补 CreateNote 覆盖——SDKCtx 类型断言命中嵌入接口晋升方法集的坑在案

### 重构(factory)：v3.109.0 建任务不变量工厂 + 去重/事件词表常量 + dupResp 模板

- storage.NewTask 工厂：ID/Status/CreatedAt 不变量此前在 taskapi/webhook×2/poller
  四处字面量各自组装，漏一处即幻影 ID/缺时间戳形态漂移；调用方补 kind 专属字段
- 去重原因词表 storage.Dup* 常量：生产（store ×5）与比较（webhook ×5）两侧
  此前共 10 处裸字面量
- progress 事件 Type 词表常量化：EventTask* 四常量，service/poller/runner
  5 处发布点从注释词表升级为编译期引用；poller 内联广播提取 publishCreated 方法
- dupResp 去重回执模板：webhook×2 四处近逐字的 existingID 回填块收一
- 新增 NewTask 工厂不变量单测（ID 唯一性/pending/时间窗/不越权预填专属字段）

### 重构(registry)：v3.108.0 mcpserver 工具注册公共底座

- requireArgs：按序必填参校验单源，消 11 处 RequireString 错误块样板
  （runReview/ask_argus/get_report/副驾三件/fixer）
- taskView：GetTaskResponse→工具输出投影单源（get_report 与 pollReviewTask
  终态渲染此前各写一遍；终态任务 FailReason 恒空，输出无形变）
- validFPShort+fpShortMsg：fp 短码校验与错误文案两处合一；defaultReviewKind
  本文件 4 处字面量收一
- 修 argString 注释错挂 askArgusTool 头上的错位；包文档工具清单 4→9 补齐
- 裁定不做：大一统 toolSpec 注册表——9 工具 handler 异构（可选能力检查/
  分支语义各异），硬同构是负收益

### 重构(nullobj)：v3.107.0 audit 空对象模式兑现 + findings 解码单源

- agentkit audit.Logger.Log 本就 nil-receiver no-op（vendor 实读证实），
  service 侧 6 处 if s.Audit != nil 守卫为冗余死代码——全部拆除统一裸调形态，
  字段注释把 nil 安全契约写明（防后人再补守卫）
- taskFindings 解码单源：GetTask/Findings 两处同型匿名 struct 收敛 service_dto，
  解析失败 (nil,false) vs 空 findings (空切片,true) 两态语义精确保留并钉往返测试
- 裁定不做：5 处手拼截断尾注不换 TruncNote（后者无 \n 分隔会改 PR 可见文本，
  且 writing/ocrdeleg 截断留薄包装既往裁定在案）

### 重构(registry)：v3.106.0 任务 kind 词表下沉 domain 单源注册表

- 词表 canonical 定义落 internal/domain/taskkind.go（registry 表：6 类全量 + review 标记），
  plugin.TaskKind* 转 re-export 别名——storage 被分层规则禁止依赖 plugin，
  此前 isReviewKind 裸字符串克隆已实际漂移，今改 domain.IsReviewKind 单源
- CreateTaskDirect 白名单 switch → plugin.IsKnownKind 谓词；kindFromIDL switch → 映射注册表
- 新增词表注册表单测（在册性/审查类恰三类/字面值对 IDL 钉死）
- CHANGELOG 浓缩表补齐 v3.104.0/v3.105.0 缺行

### 重构(toolwire)：v3.104.0 get_diff wire 契约单源 + CHANGELOG 浓缩补齐 + 覆盖率基线

- plugin/contracts.go: ToolDiffIn/ToolDiffOut（此前 requirement/qa 各持同形
  emptyIn+getDiffOut 与 diffIn+diffOut，omitempty 漂移取无 omitempty 为正）；
  至此 R3/QA 共用工具 wire 契约三族（search/file/diff）全部单源
- CHANGELOG: 补齐 v3.70.0~v3.103.0 浓缩段（git log 自动生成，一行一版 40 条），
  声明后续版本在表追加、重大变更另起详述
- tech-debt.md: 单元覆盖率基线 33.8% 入档（statements 口径，不含 e2e 打桩覆盖）
门禁：lint/test(-race)/arch/golden eval 69 全绿

### 重构(toolwire)：v3.102.0 工具 wire 契约单源——search/file schema 收口 + get_file 描述止漂

- codesearch: ToolIn/ToolOut search_code wire 契约（与 ToolDescription 同居
  本包；此前 requirement/qa 各持同形 searchIn/searchOut 与 qaSearchIn/qaSearchOut）
- plugin/contracts.go: ToolFileIn/ToolFileOut + ToolGetFileDesc（get_file 描述
  此前两面措辞已漂移，取合并措辞为正；contracts.go 本就是边界契约文件）
- 归属依据 arch 白名单：plugin 只许 {domain,learning} 不 import codesearch，
  故 search 契约归 codesearch、file 契约归 plugin（两面白名单均含 plugin，零 arch 变更）
- 执行体留各包留痕入 tech-debt.md：包装语义本就不同（R3 两级暂存/QA 定长截断）
- deadcode -test 复扫归零（五轮拆分零孤儿）
门禁：lint/test(-race)/arch/golden eval 69 全绿

### 重构(round6)：v3.101.0 config/stages/reflection 收口 + docs/tech-debt.md 剩余清单落档

（v3.100.0 编号已被并行会话 b6ace38 扫描收口占用，本批顺延 v3.101.0）
- config.go 668 → 460+config_accessors 149（派生量访问器面）+config_validate 69
  （启动期校验面）
- stages.go 573 → 119+r1 285（R1 需求解析面）+compliance 196（R3 合规判定面）
- workflow.go 585 → 488：judgeAndGate 归位 verdict.go（R5 门禁家族同文件）
- reflection.go：reflectFindings 113 行拆出 reflectModel（模型解析链）+
  applyReflectRevisions（确定性回写后验，不变量注释随段走）
- JudgeCompleteness 裁定不动：编号即结构的确定性清单体，拆检查项只加参数管道
- 新建 docs/tech-debt.md：剩余清单持久落点（6 个 100+ 行函数全数留痕裁定/
  较大文件内聚面说明/复测方法），后续轮次直接更新该文件
门禁：lint/test(-race)/arch/golden eval 69 全绿（合并树验证，并行会话 WIP 在场）

### 重构(hygiene)：v3.100.0 扫描收口——P1~P3 全清：工具描述单源 + 锁外刷新 + ctx 贯穿 + e2e 静默采样

- P1 search_code 三份构造器去重：工具描述下沉 codesearch.ToolDescription 单源
  （qa 惰性/绑定目录 + 审查面 r3SearchTool 共用）；qa 两构造器执行体收敛为
  searchCodeExec（检索/错误回执/截断语义单源）
- P2 错误忽略显式化：events SSE stream Close、a2a resp.Body.Close 改显式 _
  （errcheck 归零，意图自注）
- P3 builtin skill 刷新重构：Resolve 文件 IO 移出 skillMu（并发 Invoke 读者
  不再被锁内 IO 阻塞）+ 入参 ctx 贯穿（原 _ 丢弃；WithoutCancel 保取值、
  剥离请求取消、10s 封顶）+ buildReviewSystem 读 skillText 补锁（真实数据
  竞争：dispatcher 并发跑任务）
- P3 anomaly wrapAnomalyGuard ctx 贯穿（Info 元数据调用继承请求 ctx）
- P2 e2e comment_note settle 升级静默采样：相隔 100ms 两次计数一致才继续，
  「无变化」断言不再被慢背景 goroutine 击穿
- 自纠留痕：poller_test 固定时长冒烟（无断言无时序竞争）复核后不动
- 门禁：build/vet/lint/arch + codesearch/qa/requirement/plugin×2/poller
  测试 + race（builtin/qa）+ e2e 全绿

### 重构(round5)：v3.99.0 Execute 收口 + 契约/条件单源 + 三文件再拆

- runner: Execute 131 行四段化——execContext（记账/观测回调链注入）/
  wireWorkflow（任务级工作流装配）/repoBudgetGate（月度预算门）/
  handleRunFailure（失败收尾：硬取消保持 running + fenced 终态 + PR 告知）
- storage: DecodeJSONMap 契约单源（runner 写 / service+MCP 读此前各持一份
  同形解码）+ wherePR 四元组定位条件 8 处收敛
- 文件再拆（声明级搬移零行为变更）：dispatcher.go 597→345+dispatcher_run 262
  （Run 本体熔断配对 B 类裁定项仅文件归置）；runner.go 720→554+runner_workflow 201；
  service_taskapi.go 512→337+service_agents 187
不动项留痕：noopLogger 双包同构 17 行（logx 无 Nop，不值得新开共享位）；
ocrdeleg↔fix 重复窗为 import 块误报。手工代码 23936→23904 行，最大手工文件
554 行（runner.go）
门禁：lint/test(-race)/arch/golden eval 69 全绿

### 重构(methods)：v3.98.0 长函数收口——Merge/Select/HandleComment/Search 四座大山阶段化

- plugin/merger: Merge 228 行三段化——mergeInRound（三通道轮内归并）/
  persistPrints（批量入库）/applyLearning（HardFloor→PatternPosted→SoftFloor→
  展示标注，顺序不变量注释随段落走）；mergeInto 闭包提升为 mergeAcc 方法
- plugin/dispatcher: Select 125 行 selector 状态机化（add 门 + base/routing/
  squads 三通道）；squadTriggered 编队触发谓词三处单源（consensus 排除/
  编队追加/ActiveSquadMembers）
- service: HandleComment 111 行四段化——admitComment 准入守卫/
  dispatchSuppression（免节流抑制指令）/dispatchWriting（写作修复指令）/
  answerComment（QA 终态分派），语义逐行保真
- codesearch: Search WalkDir 闭包提升为 scanner.visit 方法 + NormalizeGlobs
  单源（此前 requirement/qa 各持一份同形 glob 归一）
- qa/requirement: numberLines 薄委托清偿（v3.13.0 双包别名各 3 行，调用点
  各仅剩 1 处，直连 textutil.NumberLines）
不动项留痕：Dispatcher.Run（熔断配对 B 类裁定）、bootstrap Assemble/
StartSubsystems/runApp（组合根）、config.Load（声明式 env 映射）、
reflectFindings/JudgeCompleteness（线性管线近 B 类边缘，拆分负收益）
门禁：lint/test(-race)/arch/golden eval 69 全绿；手工代码 24258→23936 行

### 重构(dead)：v3.97.0 死代码清偿——deadcode 复扫归零

- 删 4 个仅测试调用的生产包装：requirement.Verdict（真值表 18 处测试改调
  VerdictScoped(…, nil)，R5 规则注释随之单源）、EnforceEvidence（测试改调
  EnforceEvidenceScoped(rep, nil)）、bootstrap.Models.Ready（测试改断言
  React != nil）、sastscan.Enabled（全仓零引用）
- 删 script/bootstrap.sh：hertz 脚手架残留（BinaryName=hertz_service，零引用）
- 保留裁定：NewSDKCtxStubbed/testkit.Shq/traceid_smoke_test（合法测试基建）、
  memory.LoadIndex（v3.95.0 委托门面）
- 门禁：build/vet/staticcheck/make arch/golden e2e 全绿；deadcode -test 归零

### 重构(split)：v3.96.0 大文件同包拆分——七大文件按内聚面切分（零行为变更）

- runner.go 1165 → 720+runner_prctx.go 174+runner_report.go 305
- store.go 1056 → 363+store_task.go 487+store_prune.go 225
- service.go 1042 → 368+service_auth.go 90+service_taskapi.go 512+service_dto.go 109
- stages.go 963 → 580+r3tools.go 406（R3/R4 工具装配面独立）
- config.go 801 → 668+config_env.go 140（env 助手族+API token 解析独立）
- workflow.go 691 → 585+verdict.go 109（判决/门禁面独立）
- qa.go 661 → 408+qa_tools.go 273（Q&A 工具装配面独立）
同包纯搬移，声明级 text-move + goimports；生成代码 biz/model/api（8537 行，thriftgo
DO NOT EDIT）按惯例排除。门禁在干净 worktree 复验（主树有并行会话 WIP 占用）。

### 重构(reuse)：v3.95.0 重复面单源收敛——diffprep 装配循环状态机化 + fp/命令解析/cmdparse 单源

- diffprep: Prepare/PrepareIncremental 两份逐行复制的装配循环收敛为 diffBuild
  状态机（entryOf/metaOnly/skipByBudget/addText/finish），调用方只留各自分支语义
- memory: LoadIndex 切到 loadIndexFrom 单源（v3.64.0 抽出后仓桶入口漏切）
- service: 新建 cmdparse.go——cutCommand 词边界前缀解析（describe/testgen/fix）
  + parseFPHex fp 短码尾部（fix/suppress/restore），删三份逐字复制
- service: replyModel 写作面模型回退单源（describe/testgen）
- runner/inject: worktreeKey 逐字段克隆收编到 plugin.PRToKey 单源
门禁：lint/test(-race)/arch/golden eval 全绿

### 重构(core)：v3.82.0 引入库能力批——ekit data/cache/mathx/uidgen 接管手写缓存与零碎，ctx 别名门面功成身退

引入 ekit v0.33.0 能力（三个手写缓存状态机由库接管，并发正确性单源）：
- main.go llmReachableCached（mutex+read-check-write 19 行，探针期全串行）
  → data/cache.LocalCache TTL 30s：并发 readyz 探针 GetOrLoad single-flight
  合并为一次回源（行为升级）
- service qaThrottles（map+mutex+懒初始化+『超100清1h前』驱逐循环 21 行）
  → LocalCache TTL 30s + MaxSize 200 LRU
- runner repoGuideCache（mutex+map+『满64整体清空』换防）→ LocalCache
  MaxSize 64 LRU 精确淘汰（热条目不再被无差别逐出，行为升级）
- plugin bestofn.ClampReviewN 三分支钳制 → mathx.Clamp（语义逐位一致）
- adapter_remote a2aMessageID（crypto/rand 静默忽略读取错误）→ uidgen.ShortUUID
- notify FailReason 截断（[]rune 全量拷贝手切）→ textutil.TruncEllipsis
ctx 别名门面退役：internal/context ctxfit/ctxcontinue/ctxrate 三文件 62 行
（12 导出符号仅 5 个有消费）删除，bootstrap/runner 两个消费方直连
agentkit/llm/wrap——下沉迁移完成态的间接层清偿
适配 agentkit v0.10.41 通用件下沉轮（并行会话破坏式变更）：
- agentkit/httpx 删除 → ekit/pkg/httpjson（sink 迁移，API 同形）
- agentkit/jsonrepair 删除 → ekit/pkg/jsonrepair（5 文件 import 切换）
- 依赖面：agentkit v0.10.39→v0.10.41、ekit v0.32.0→v0.33.0
裁定不换（留痕）：service.BearerMiddleware 不换 ekit BearerTokenAuth（对端
token 空放行=fail-open，argus fail-closed 是 v3.0.1 安全裁定）；obs 手写
Prometheus 中间件不换 ekit prometheus（指标名/标签契约变化需改仪表盘告警）
门禁：全量单测零 FAIL + arch 绿 + golden 69 全绿（agentkit v0.10.41/ekit
v0.33.0 代理拉取实测通过）

### 重构(core)：v3.81.0 ekit 基建下沉批——octopus 客户端与 .env 引导归位，ekit 升 v0.32.0

下沉（ekit v0.32.0，30c9363，首个对 ekit 的下沉批次）：
- internal/notify HTTP 层（octoClient/sendPost ~50 行）→ ekit/network/octopus
  新包（X-User-Id 机器账号纪律单源：空账号显式报错不匿名发送；状态码透出
  由调用方记账；业务文案渲染与指标记账留 argus）
- internal/config LoadDotEnv（~25 行）→ ekit/config.LoadDotEnv（候选路径与
  语义逐字保持：首个存在文件生效、已 export 优先、无文件静默）
ekit 无本地 replace，本次经模块代理拉取 v0.32.0 实测通过（tag 已推 forgejo）。
门禁（干净 agentkit v0.10.39 tag 基座实测）：全量单测零 FAIL + arch 绿 +
golden 69 全绿

### 重构(core)：v3.80.0 工具链下沉 agentkit 批二 + 适配 v0.10.38 破坏式变更，依赖升 v0.10.39

下沉（agentkit v0.10.39，248ad60）：
- internal/gitdeepen（106 行）→ agentkit/workcopy.EnsureMergeBase（探测式
  指数 deepen 归位工作副本生命周期属主）；本包保留门面，ocrshim/ocrdeleg
  调用方零改动，测试 6 例随迁 SDK
- internal/sastscan 核心（gitleaks/semgrep 执行+JSON 归一 ~200 行）→
  agentkit/sast 新包（Finding 与嵌入方契约解耦；bin 信任源/--redact/Secret
  红线/环境白名单纪律随迁，环境白名单留 argus 侧注入）；本包保留
  plugin.Outcome 映射门面，行为测试 10 例随迁 SDK、argus 钉映射契约
- toolpool  头部展开与任务级 _meta ctx 键 → mcp.ExpandEnvRefs/
  WithCallMeta/CallMeta（本包保留别名，runner 零改动）
适配 v0.10.35~38 破坏式变更（并行会话审计轮）：
- mcp.Pool.Tools → ToolsResult{Tools; Failed}（失败面 OnError 同源发射，
  适配层取成功集）
- agentrun 入口族收敛：RunWithEventsAndRetry → Run + WithOnEvent/WithRetry
  （qa/builtin/requirement 三处迁移，retryQuery 完整替换语义注释保留）
依赖声明修正：批 4 bump perl 模式写错静默 no-op，v3.79.0 声明仍是 v0.10.34
（本地 replace 掩盖；代理构建会因缺 diffx 失败）——本次升 v0.10.39 前向修复
门禁（干净 v0.10.39 tag 基座实测）：全量单测零 FAIL + arch 绿 + golden 69 全绿

### 重构(core)：v3.79.0 unified diff 解析下沉 agentkit/diffx + firstNonEmpty 单源——依赖升 v0.10.37

- internal/context/diffprep parseDiffByFile/rawDiffPath（60 行）→ agentkit/diffx
  SplitUnifiedDiff/DiffPath；fillFromRawDiff 保序语义改走 SplitChunks（同路径
  块后者覆盖的确定性保持），存储值不带前缀契约不变（测试钉住读写两端）
- internal/runner/inject parseHunkLines+hunkRe（30 行）→ agentkit/diffx
  HunkLines（LineRange=[2]int 别名，renderTouchedSymbols 签名零改动）
- runner+requirement 两份 firstNonEmpty 克隆 → textutil.FirstNonEmpty 单源
- 解析行为测试随迁 SDK（diffx 3 例），argus 侧回填契约改由 fillFromRawDiff
  测试钉住（含不改动调用方切片）
门禁（干净 v0.10.37 tag 基座实测）：全量单测零 FAIL + arch 绿 + golden 69 全绿；
agentkit 本地树并行会话施工中（mcp/pool.go 中间态编译不过），验证走 tag worktree

### 重构(core)：v3.78.0 codegraph 下沉 agentkit/codeintel——目录符号索引归位 SDK，依赖升 v0.10.34

- internal/codegraph 实现（codegraph.go 266 行 + polyglot.go 179 行 + 3 测试
  文件）→ agentkit/codeintel 新包（v0.10.34，abbca1e）：Go AST 定义/出现/
  调用形态 + JS/TS/Python 词法符号表是审查/问答 agent 的通用工具数据面。
  argus 保留门面（类型/构造别名，runner/inject 工具壳调用方零改动），
  行为测试 8 例 + bench 随迁 SDK
- itoa 等私有零碎随下沉消灭（strconv.Itoa 直写）
门禁（干净 v0.10.34 tag 基座实测）：全量单测零 FAIL + arch 绿 + golden 69 全绿

### 重构(core)：v3.77.0 下沉 agentkit 批——ctx 三件套与学习环数学归位 SDK，依赖升 v0.10.33

下沉（agentkit v0.10.33，2f15ec5，argus 侧保留 API 门面调用方零改动）：
- internal/context ctxfit(372 行)/ctxcontinue(101 行)/ctxrate(65 行) →
  agentkit/llm/wrap 新包（eino BaseChatModel 装饰器集：FitModel 三级窗口拟合
  +真实 usage 校准 / ContinueModel 截断续写 / RateModel 限速）。obs 指标硬连
  改 OnBreakdown 回调，装配点接线（bootstrap.wrapFit）；行为测试 513 行随迁
  SDK，argus 留门面冒烟
- internal/learning 确定性数学与键派生（normalizePatternComment 正则管线/
  PatternKey/PatternSample/Confidence Beta 后验/PatternSamples）→
  agentkit/reportutil pattern.go（与 WilsonCI/McNemarExact 同族），
  learning 保留域模型/阈值/门面，成对循环不洗白语义附 SDK 回归测试
依赖升 v0.10.33 适配（v0.10.32 agentrun ReAct 默认开流）：
- qa/adapter_builtin/requirement 三处 RunWithEventsAndRetry 显式
  DisableStreaming: true——argus 消费面未接 delta 事件，保真沿用整段语义
  （流式采纳另批评估；已实测：不关流则 qa 兜底/R3 降级测试因流式语义 FAIL）
门禁（干净 v0.10.33 基座实测）：36 包单测零 FAIL + make arch 绿 + golden 69
全绿；agentkit 本地树有并行会话在改（v0.10.32 流式 + 审计施工中），验证走
干净 tag worktree，main 树 replace 惯例不变

### 重构(core)：v3.76.0 全面精简第一批——手写轮子收敛库单源，顺修 2 处 UTF-8 截断 bug

- 截断族收敛 agentkit textutil：runner/codegraph/merger 三份同语义 truncRunes
  删除走 TruncEllipsis；learning/stages 走 TruncRunes/TruncEllipsis 单源；
  sastscan.truncLine 与 service.truncVerify 旧按字节切片会腰斩多字节 UTF-8
  （错误链进 LLM prompt 可能 400/回帖出乱码），改走 rune 安全单源顺修
- sink 上报收敛 agentkit httpx.DoJSONWithRetry：ctx 感知/限容读体/状态码检查
  /UTF-8 安全错误摘要单源；旧行为三处收口（4xx 不再盲目重试、响应体不再
  不读不限、总预算 10s 取代每次尝试独立 10s）；对端成功契约恒为 JSON 对象
  （review-service IngestHandler），测试同步钉住
- 谓词收敛：matchAnyLabel/skillsIntersect 两份同逻辑合一 listOverlap；
  contains/containsString/containsSkill → slices.Contains；joinNames →
  strings.Join；firstBoardLine → strings.Cut；storage absDiff → 内建 max
  惯用法；mapKeys → slices.Sorted(maps.Keys)；storage.firstBy → learning.
  FirstBy 单源（学习环记账键此前两份实现，漂移即错账）
- learning 包注释改为「零内部 argus 依赖」（文本原语走 textutil，与 domain
  委托 reportutil 同纪律；分层不变量是零内部依赖，非零外部依赖）
门禁：受影响 14 包单测绿 + make arch 绿 + golden 69 全绿

### 重构(domain,service,runner)：v3.65.0 领域模型叶子包收口——domain 抽取解除 storage→plugin 分层倒置 + service/runner 拆分

- 091d5e6

### 重构(core)：v3.64.0 重复代码收口——八处克隆单源化（净删 ~120 行）

- 截断尾注单源：6 处手写 TruncRunes+"…（截断）"改 textutil.TruncNote
  （vendor 注释明言该函数就是为截断留痕单源而生；\n 前缀变体 3 处保留）
- sdkctx 泛型 invoke：call/callIface/call2 收口 12 个方法的 Provider 解析+
  bound ctx+能力断言样板（此前每方法 5-7 行重复；行为顺序逐字等价：
  Provider 错误优先、断言在 bound 后；CreateIssueComment 的 DiffManager
  优先+回退逻辑原样保留只换外壳）
- builtin ReAct/单轮提示词组装去重：buildReviewSystem（strategy 仅 ReAct）+
  buildReviewUser（diff/摘要/黑板三围栏）两方法单源，结尾指令留调用方
- findings JSON schema 单源：plugin.FindingsWire + DecodeFindingsJSON
  （此前匿名结构体三处克隆；By 归属策略各调用方自定）
- agentrun 工具轨迹日志单源：plugin.ToolTraceLogger（builtin 的 plugin kv
  与 r3 的无 kv 差异走 kv 参数；nil log 返回 nil 闭包保持 builtin 语义）
- learning.PatternState.SampleText：样本回退三抄单源（dream×2+health）
- memory.SingleLine 导出：dream 删本地逐字克隆
- dream 删本地 truncateRunes 改 textutil.TruncRunes（顺带修 n<0 panic 隐患）；
  learning 本地版保留——包 doc 明言纯叶子仅依赖 stdlib，勿动
- 门禁：全量编译/staticcheck/internal 全测/golden 68/e2e 全绿

### 重构(bootstrap,main)：v3.63.0 组合根单点闭环——后台子系统组装自 main 收口进 bootstrap

- 新增 bootstrap.StartSubsystems：多副本选主（LeaderElector）/worker 池
  （两阶段停机）/GitHub App refresher 停机收编/运维 ticker 全家桶
  （workcopy 清扫/任务保留/会话与指纹清理/failed 重试/队列深度/eval 指标）
  /dream 长期记忆整合/审查健康报告/轮询兜底任务源——此前散在 main.runApp
  手工组装的 ~180 行全部收敛，资源注册顺序（poll-leader→worker→refresher
  →comments→http 逆序关闭）与全部行为原样保留
- main.go 431→260 行：只剩进程身份生成（instanceID）/store 打开/Assemble/
  StartSubsystems 一行调用/HTTP 接线——与文件头自述「main 只做进程生命周期」
  首次真正一致，v3.48.0 收敛 service 时遗留的最后一块组合根分裂就此闭环
- make arch 分层守卫验证通过（bootstrap 白名单无约束，main 不在 internal 集合）

### 重构(test)：v3.62.0 可执行架构守卫——分层白名单+组合根单向性+包文档完整性进门禁

- 新增 test/arch：layering 白名单把 32 个 internal 包的依赖方向钉成封闭
  允许集（当前实现事实快照），任何依赖方向变更必须先改白名单=架构变更
  在 diff 里显式可见、可评审、可回滚；TestBootstrapNotImported 钉组合根
  单向性（装配职责泄漏即失败）；TestPackageDocs 钉包文档完整性
  （domain 在途迁移列为暂缓名单，收尾后移除）
- Makefile 新增 make arch 目标；守卫三件套与 lint/test/eval 并列
- dream 包注释规范化（Package 前缀+紧贴 package 子句，此前 .Doc 为空）
- 白名单两处在途事实注明：plugin→domain、storage→domain 为并行会话
  PrintStore 域接口迁移完成态，随其提交生效
- make arch / lint / test / eval 全绿

### 重构(workflow,plugin,context)：v3.61.0 超长函数三批清偿——R1/R3/共识/反思/SDKCtx/Render/runReview 拆分

- RunConsensus 178行 → resolveConsensusMembers / consensusAct（env 结构显式化
  闭包捕获集，taskCtx 与 actCtx 刻意区分语义入注释）/ fillConsensusOutcomes
- reflectFindings 177行 → buildReflectInput（DataFence 输入组装）/
  applyEvidenceGate（strict 剔除记 regret 用改写前原始身份）/ reconcileRemoved
  （剔除对账去重）
- ParseRequirement 147行 → collectReqSegments（PR 描述+issue 段收集）+
  applyR1Guardrails（sanitize/留痕/上限/截断七道防线）
- CheckCompliance 124行 → r3Instruction（提示词五段拼接）+ parseAligned 闭包
  （首试/重试共用，重试路径此前漏记对齐日志一并补齐）+ degradedComplianceReport
- NewSDKCtx 100行 → initGitHubApp（App 分支含刷新循环）+ newProviderFor
  （PAT/App/App 刷新重建三路构造单源）
- Render 115行 → renderCompliance/renderFindings/renderMeta 三节渲染器
- runReview 105行 → 表驱动 RequireString + pollReviewTask + renderTerminalTask

### 重构(bootstrap,poster)：v3.60.0 超长函数二批清偿——BuildModels/BuildRegistry/Post 拆分

- BuildModels 264行 → 主函数 60 行 + 四段命名方法：buildGenerateChain（主链
  Client/Resilient 分支+降级 BaseChatModel，fallbackProvider 返回值传递）、
  buildStageLLMs（P6 阶段覆盖）、wrapFailover、wrapFit（fit/continue/stageWindow/
  wrapStage 闭包随段内聚）。ReviewerReact==nil 兜底保持在 Fallback 块外（原语义）
- BuildRegistry 195行 switch 五 case 拆四工厂：newBuiltinAgent（isCompliance
  返回值供主循环收集 skillRefs）/newCLIAgent/newRemoteAgent/newACPXAgent——
  cli/remote/acpx 从不 fail-fast（一律停用降级），签名不带 error 更诚实
- Poster.Post 182行 → staleness 门控 + prepareInline（行内过滤/路径归一化/
  排序/钳制/评论构造）+ sendReview（幂等查重+三级降级，返回行内成功条数）+
  总结评论/回执——Post 只剩编排骨架

### 重构(core)：v3.59.0 死代码/间接层清偿——转义单一出处+hooks 单层化+prToKey 收口

- 删 Config.MaxCallInputChars/EnabledPlatforms（零生产调用，仅测试自嗨）
- 删 requirement 死别名 Kind*（canonical 自 v3.5.6 在 plugin，包内零引用）与
  adapter_builtin.WorkflowRunner（e2e 唯一引用改走 plugin 源头）
- 删 reportview.EscapeUntrusted 纯转发（13+1 处改直调 fence.EscapeUntrusted，
  与 health/dream/service 同一出处）；ReportHeaderHTML 改用 ReportHeaderPrefix
  拼接，消除常量与硬编码字面量的漂移陷阱
- hooks 三对 Report*→obs* 双层转发单层化（nil-guard 并入导出面），
  dispatcher/squad_consensus 包内调用点同改
- prToKey 两份字节级克隆（adapter_remote/adapter_cli）下沉 plugin.PRToKey 单源

### 重构(runner,service)：v3.58.0 超长函数群清偿收官——run/Webhook 拆分

- run 202 行 → prepareContext/buildPluginTask/recordRegret/finalizeAndAnnounce
  （stale 显式布尔、finish 失败保留上抛语义）
- Webhook 177 行 → webhookAdmission/webhookCRFilter/webhookCreateTask
  （准入顺序即安全语义，§5.1 规则顺序不变）
- 纯迁移零行为变更，webhook 单测 + golden 68 + e2e 钉住等价性

### 重构(workflow)：v3.56.0 r3Tools 249 行拆分五方法 + prompt 校准实验负结果记档

- r3Tools → r3RepoTools/r3CodeGraphTools/r3DetailTool/r3AddExternalTools，
  纯迁移零行为变更（golden 68 钉住）
- prompt 校准 A/B：clean 0/5→2/5、FP 2→1，但 65-sql-concat（SQL 注入）
  漏报、总分 52→51 净降——按预注册规则回退；实验数据在案防重试同款

### 重构(workflow,runner)：v3.55.0 finish() 拆分四命名子步骤 + prev_findings 注入留痕

- finish 210 行单体 → 编排壳 + dispatchR4/mergeAndReflect/applyRepoPolicies/
  judgeAndGate，顺序即语义、纯迁移零行为变更，golden 68 + e2e 钉住等价性
- argus.runner.prev_findings_injected 日志补跨轮注入观测缺口（本地部署测试发现）

### 重构(learning,dream,health,workflow)：v3.54.0 接口补全 + 输入通道契约

- RepoRef 归位 learning（storage 保别名）；health 接口全面走 learning 投影
  （ListRepoPatternStates），彻底去 storage 行模型依赖
- dream 与 storage 的 PatternSignal 耦合如实文档化（去重按 DB 行身份，固有）
- Workflow 三输入通道契约文档化（P2-3 复核定案：运行期数据不宜 Options 化）

### 重构(storage)：v3.53.0 writeMu 改 SQLite 感知（多副本 MySQL 解锁）

- writeLock/writeUnlock 仅 SQLite 加锁；MySQL 并发正确性由行锁/SKIP LOCKED/
  唯一索引承担，进程级互斥是横向扩容吞吐天花板
- SQLite 路径语义零变化（16 处机械替换，全部标准配对）

### 重构(bootstrap)：v3.48.0 架构治理 B4'——装配收口

- service 构造收敛进 Assemble，System 成为完整对象图；main 只剩 HTTP 接线
- runnerRef 延迟绑定不变量装配尾自检（fail-fast，防条件构造路径静默 nil factory）
- B4 原方案（enrich 接口化/学习环窄接口）实施复核裁定不做，证据在案设计文档 §2
- 补 v3.47.0 CHANGELOG 条目（B3 提交时遗漏）

### 重构(learning)：v3.47.0 架构治理 B3——学习环域归位 internal/learning + 阈值单源

- 新建纯叶子包 internal/learning（PatternKey/Confidence/PatternSamples/FirstBy/
  PatternState/Config/DefaultConfig），自 plugin 归位——消费方 storage/dream/health/
  exportdata 与插件语义无关；plugin.Merger 经窄接口引用 learning 类型
- LearningConfig 单源化：config.Config.LearningConfig() 成为唯一派生出口，
  消灭 bootstrap×2/main×2/service×1 五处手写字面量映射（阈值字段变更单点改）
- config 新增对 learning（纯 stdlib 叶子）的许可依赖边；对 plugin 禁令不变
- main/exportdata/health/service 的 plugin 依赖随之消失或收窄

### 重构(plugin,workflow)：v3.46.0 架构治理 B2——契约单源化

- Layer 词表升级为管线阶段总词表（文档化四类消费面）+ LayerR1A + StageTimeline 只读词表
- 裸串清零：workflow/requirement、bootstrap/models.go、service.TaskStages 全部改引常量
- debug 裸键族常量化（DebugKey*），service↔workflow 隐式契约显式化
- 指标 label 值字节级不变，新增阶段只改 plugin 一处

### 重构(storage,runner)：v3.45.0 架构治理 B1——纪律口子修复

- fenced 终态观测口径统一（预算/stale 路径弃 _ = FinishTask，fenced 感知；核实 FinishTask 本已委托 Fenced，零行为变更）
- storage.ErrNotFound 哨兵收口最后一处 ORM 错误泄漏，service 去 gorm 依赖
- TouchRunningHeartbeats 空 RunnerID 从静默 no-op 改显式报错
- 删 main 第一遍 ResetRunningToPending（30s 窗超集已覆盖）
- 删僵尸抽象 TaskStore（iface.go，全仓零消费者）
- 设计文档入库：docs/superpowers/specs/2026-09-27-coupling-cohesion-design.md（B1~B5 规划）

### 重构(sastscan,service)：v3.29.2 SDK 深度结合——子进程生成全量收敛 procx（零行为变更）

- sastscan gitleaks/semgrep 两处 exec.CommandContext + 手写 combinedOutput
  （timer kill）+ minimalEnv 拼装 → procx.Run（进程组托管/超时宽限/输出上限/
  环境按名白名单透传全继承 SDK 纪律）
- service 沙箱验证 sh -c 包装命令 → procx.Run
- 此后 argus 子进程生成 100% procx 单源；仅余 exec.LookPath 两处存在性检查
- 语义保真：非零退出仍判失败（gitleaks --exit-code 0 钉住）；超时错误文本
  变 procx.ErrTimeout（回帖更可读）
- 评估不换：fakeChatModel 内容分支与 llmtest 有序脚本不匹配，登记缓办
门禁：build/vet/staticcheck+gofmt/test/golden 57/57/e2e 全绿

### 重构(plugin,qa,workflow)：通用能力下沉 agentkit v0.10.5——fence/conversation/sampling/textutil/severity

argus 消费面切换（实现上收兄弟仓，本地保留薄门面保 API 兼容）：
- plugin.DataFence → agentkit/fence.Data（围栏中和计数经既有
  ReportFenceNeutralized 钩子留痕——包零副作用、观测归调用方）
- plugin.SanitizeFileStem → agentkit/textutil（ocrshim/dream/ocrdeleg
  三处调用方不动）
- qa.numberLines / requirement.numberLines 两处克隆收敛 → textutil.NumberLines
- qa 滚动摘要：窗口切分/渲染/拼装 → agentkit/conversation（Split/Combine/
  Render）；摘要 LLM 压缩策略留在 qa（域语义）
- plugin/bestofn aggregateSamples → agentkit/sampling.Aggregate（双通道签名
  +行号相容 eq）；severity 合并与复现升级语义留在本包（审查域词表）
- plugin/severity.go 别名表删除 → agentkit severity.Normalize 折叠（词表
  单一来源，v3.8.0 修复的问题在兄弟仓收敛）
agentkit 侧：v0.10.5 已发（canonical origin + GitHub 镜像脚本）；
go.mod require 对齐 v0.10.5（replace 刻意保留不变）
不下沉项（域语义非通用）：PatternKey/Beta 置信度、完备性检查、健康报告
测试：qa/plugin 全绿；qa 测试块随实现迁移重写

### 重构：全面收敛到 agentkit v0.7.1——删重复实现 + 接入新能力

删除重复实现（净 -198 行，算法单一来源）：
- plugin/severity.go 152 行逐函数克隆 → agentkit/severity 薄别名层
  （本地 glob 版缺 .gitignore 尾随 ** 语义与多字节 ? 支持，随之修复）
- plugin.TokenBudget 逐方法克隆 → type alias = llm.Budget
- internal/context/util.go scrub 死代码删除（agentkit/workcopy 同实现）
接入 agentkit v0.7.0/0.7.1 新能力：
- stages.go runAgent：Config.ToolsFactory 按尝试重建工具表——修复
  toolprior.WithCallLimit 限流计数跨重试累计导致 R3 重试轮外部工具
  一开始就被软止损的问题；工具轨迹（tool_call/tool_result）落审计日志
- adapter_cli：~115 行进程管理（cappedBuffer/childEnv/baseEnvAllow/
  runProcessGroup）整体委托 acpx.RunProcess（旧手写 goroutine+sleep(3s)
  范式随之升级为 cmd.Cancel/WaitDelay）；errors.Is 区分超时/取消
- config：预算派生公式委托 llm.BudgetConfig（防双侧漂移）；新增
  MaxTokensParam 支持负值透传（不下发 max_tokens，推理模型兼容）
- runner：skill tool 模式清单改用 skill.ListSkills+ListPrompt
  （v0.7.0 规范名+别名归一化配套）；obsx PreviewLen 留 0 注释钉死
- acpx 适配器：ErrTimeout/ErrCanceled sentinel 分类日志（kind 字段）
- events SSE：连接期 Bus.Dropped() 丢弃增量观测
- dispatcher：熔断参数显式取 agentkit 缺省常量，SkippedReason 文案
  从 DefaultCooldown 派生不再硬编码"5 分钟"
- plugins.example.yaml：补 mcpServers env 白名单语义注释
全仓 build/vet/test（含 e2e）绿。

### 重构：runAgent 委托 agentkit/agentrun（ReAct 样板沉淀）

- stages.go runAgent 从 40 行 ADK 样板 → agentrun.Run 一处调用
- 删除 stages.go 的 adk/compose 直接依赖（schema 保留）
- 依赖 agentkit v0.6.0

- 构建(deps)：v3.118.0 go-git-platform v0.76.0→v0.78.0

- v0.77.0：高内聚低耦合强设计模式重构（单一重试引擎/单一管线/ClientOption
  Builder/验签归一/分页迭代器收敛）；v0.78.0：Gitea 家族合并第 1 批
  （giteafamily 引擎 ports&adapters + Manager 五职责拆分）——并行会话已
  发版（tag 在远端），本批消费
- 拉取路径：goproxy.cn 与 sumdb 镜像当日均不可达 → GOPROXY=direct +
  单命令作用域 GONOSUMDB 跳过 sumdb，用本地 tag 工作树做内容交叉校验
  补强完整性（diff 零差异=字节一致）；未持久化任何校验弱化配置
- 零编译断点：v0.77/v0.78 全部为 SDK 内部重构，argus 消费面（Provider/
  SearchManager/DiffManager/NoteManager/webhook canonical 词表）无感
- 四门禁绿（test -race/lint/arch/golden 69）

- 构建(mod)：v3.117.0 去掉双兄弟仓 replace → 正式版本钉定

- agentkit v0.13.0 / go-git-platform v0.76.0：两仓 tag 已存在且与远端同步
  （b8cb10e / 288fe28），无需新发版本；go.mod 去 replace 后 require 钉定
- 隔离测试：两兄弟仓临时改名后 go build 通过——构建零本地依赖，
  CI（self-hosted runner 也好、干净环境也罢）与任何他机自包含
- 此后兄弟仓演进须发新版本 + argus bump 才生效——发版纪律取代树耦合
- 仅存的 replace 是公共模块 thrift v0.13.0 覆盖（与兄弟仓无关，保留）

- 构建(mod)：v3.116.0 删除 vendor 转模块模式——replace 转活暴露并修复 acpx 词表断裂

- 删 vendor/（206MB，gitignore 残留）：Makefile/Dockerfile 自 v3.1 起已声明
  go mod 模式，vendor 是历史包袱；此后 replace（agentkit/go-git-platform
  兄弟仓）转活为构建真源，"SDK 改动须手动同步 vendor" 整类负担消灭
- replace 转活即暴露：agentkit v0.13.0（b8cb10e）NewMinimax→NewMcode
  （minimax-code CLI 自发布起不存在），argus 手抄构造器 switch 编译断——
  修复=构造器单源委派 acpx.NewRegistry（pi/dsh 等新增 agent 自动可用），
  bins 探测词表 minimax→mcode，词表对齐回归测试钉死
- 四门禁绿（模块模式首轮全过：test -race/lint/arch/golden 69）

- 构建(docker)：私有依赖拉取加 GOINSECURE + goproxy.cn——git.example.com 证书过期导致 https 元探测 EOF，镜像构建在 215 实测打通

- 杂务(deploy)：k8s43 清单同步 v5.5.1c——image tag+显式 command（ENTRYPOINT 兜底）+README 部署实录
- 杂务(deps)：v5.4.3 oekit 8 模块追平最新 + best-effort 写失败留痕

- oekit 8 模块追平最新：pkg v0.31.2→v0.33.0（跨两 minor：SetNX 原子写入/LastN/
  选择性 stale；stringx.Filter 语义翻转 BREAKING 零消费）+ hertz v0.8.12→v0.9.0
  （crudh 分页/排序白名单契约 BREAKING 零消费；transportx reply header、
  hertzotel span、governance 路由模板键等修复被动受益）+ logx v0.8.2/concurrency
  v0.23.4/metrics v0.1.11/middleware v0.13.8/gormx v0.7.1/registry v0.5.3；
  go-git-platform v0.101.0 已最新，#32（gitlab note 缺口）仍 OPEN 待上游修。
  零代码适配
- best-effort 写失败留痕（吞错分诊收尾）：runner_lifecycle 两处 _ = r.Store.——
  head SHA 回写失败⇒同 SHA 去重失效（重跑重复审查）；debug 产物写失败⇒
  ARGUS_DEBUG=1 排查失据。补 Warn 留痕
- make arch 架构门禁绿；吞错分诊其余项定性存档（db.DB() 惯用形/三处 Background
  ctx 有注释刻意选择）
门禁：make lint + make eval（golden）+ 全量测试零失败

- 杂务(deps)：v5.3.1 依赖升级——go-git-platform v0.100.0 + oekit 全家桶追平最新

- go-git-platform v0.96.0 → v0.100.0：跨 host 重定向凭据泄漏 P0 修复、webhook 事件 ID
  永不为空、伪翻页无进展软收手、ahead/behind 无共同祖先漏判漂移、WebhookConfigManager
  /CollaboratorManager 等增量能力面；连带 gitea.dev/sdk v1.4.0 / gitlab client-go
  v3.40.0 / go-gitcode v0.8.0 / go-gongfeng 收编 studyzy SDK
- oekit 11 直接依赖全追平：pkg v0.31.1 / logx v0.8.0 / concurrency v0.23.3 /
  data v0.19.9 / hertz v0.8.12 / middleware v0.13.7 / bootstrap v0.6.8 / kitx v0.3.4 /
  metrics v0.1.10 / tracing v0.3.2 / core v0.12.1；间接 gormx v0.6.6 / registry v0.5.1
- 行为面核对：argus 无 DoRaw/HashKey/ResolveClientIP 直连消费点，pkg v0.29 DoRaw
  BREAKING 与 v0.30 HashKey 编码变更均不触及；所得全为修复收益（singleflight DoChan
  Forget 竞态、budget int64 溢出、jwt 签发验签同源、prometheus collector 复用、
  logx WithAttrs 脱敏、sqlite 链 modernc v1.61）
- 零代码适配：全量 42 包测试绿 + make lint（vet/staticcheck/gofmt）绿

- 杂务(deps)：抬 oekit 依赖地板（按上游已发布版本表对齐）

按上游已发布的 versions.json 对齐本仓落后的 oekit 依赖地板：
- concurrency v0.21.0 → v0.22.0（纯新增：fanout.Map 限并发批量执行原语，#97）
- nns v0.4.4 → v0.5.0（纯新增：多端点服务名注册 RegisterAs/DeregisterAs +
  注册中心优先静态兜底 FallbackOrder，既有调用方零行为变化，#30/#31）
- kitex/observability/tracing/instrumentation/kitexotel v0.1.0 → v0.7.0：
  上游 CHANGELOG 明确要求「用 v0.7.0 或更新的同号版本，不要用 v0.1.0」——
  v0.1.0 撞上根模块改名前的旧模块路径，代理缓存里那几版的 .mod 仍声明旧路径，
  Go 解析父模块前缀候选时命中脏条目即硬失败，消费方首次 go get 间歇性失败（#44）。
本仓不改业务代码，仅抬地板。

- 杂务(deps)：抬 oekit/pkg 地板 v0.26.0 → v0.28.0

上游 oekit/pkg 连发两版，均为纯新增、无破坏性变更：
- v0.27.0 新增 pkg/shellx——POSIX shell 参数转义原语（Quote / QuoteArgv）：
  把外部输入拼进 shell 命令时，仓里只有 pathx 的「路径合法吗」，
  「怎么写进命令行才不被 shell 解释」这一层此前是空的（#112）；
- v0.28.0 新增 pagination.Policy.NormalizeLimit——条数口径的归一化入口，
  替代 Normalize(1, v) 那种把页长当条数用的绕法（#115）。
本仓不改业务代码，仅抬地板。

- 杂务(deps)：抬 oekit 依赖地板 —— pkg v0.26.0 / hertz v0.8.5

上游 oekit/pkg v0.26.0（BREAKING）：
- convert.ResolveClientIP 去掉 xri 实参，默认不再采信 X-Real-IP（单跳头无法区分
  「代理写的」与「客户端自带的」，客户端可任意改写自己的客户端 IP，oekit/pkg#106）；
  确需采信改用 ResolveClientIPWithXRI / GetClientIPWithXRI。
- 代理头候选值做合法性校验与端口归一（oekit/pkg#104）。
- 可信代理裸 IP 条目归一为单主机网段、构造期对非法条目 fail-fast（oekit/pkg#104）。
- 新增 convert.TrustedProxySet 预解析集合，请求期零 CIDR 解析（oekit/pkg#107）。
- encoding 补 Base64RawURLEncodeBytes / Base64RawURLDecodeBytes（oekit/pkg#105）。
上游 oekit/hertz v0.8.5：clientip 跟随上述收紧（默认不采信 X-Real-IP），
新增 GetClientIPWithXRI / GetClientIPWithDefaultProxiesAndXRI / GetClientIPWithSet。
本仓不改业务代码，仅抬地板。

- 杂务(deps)：oekit 地板抬到最新发布 tag

github.com/oekit/pkg         v0.25.0 -> v0.25.2
github.com/oekit/concurrency v0.20.4 -> v0.21.0
github.com/oekit/hertz       v0.8.3  -> v0.8.4
仅依赖面变更，无源码改动。

- 杂务(deps)：oagentkit 依赖地板抬到各自最新发布 tag

按「依赖地板默认追最新」纪律，把仓内已有的 oagentkit 模块全部抬到各自最新
发布 tag（版本经 goproxy.cn 的 `go list -m -versions` 实测取得，非猜测、
非本地缓存）：
  agent     v0.10.4 → v0.11.0
  guardx    v0.13.1 → v0.15.0
  llm       v0.17.0 → v0.17.1
  mcpkit    v0.8.5  → v0.8.6
  obs       v0.9.6  → v0.9.7
  skillkit  v0.8.4  → v0.8.5
  toolgate  v0.12.4 → v0.12.5
  tools     v0.10.3 → v0.10.4
源码零改动——本仓未用到这几个版本之间变更的 API。
验证：build / vet / test 全绿。

- 杂务(deps)：oekit/pkg 抬到 v0.25.0

v0.25.0 修了两个上游缺陷并补了原语：
- fix(cache): SingleFlightCache 把一次未命中计成两次 Misses（#90）——一次读
  恰好计入命中或未命中之一，命中率指标不再失真。
- feat(urlguard): 补自定义 IP/host 判定钩子（#91）——消费方策略可作为单一
  判定源接进拨号层（预检与建连后的端点复核同源）。
零 replace（真实发布版本）。

- 杂务(deps)：oekit/data 抬到 v0.19.0

data v0.19.0 的 cache 壳跟随 pkg v0.24.0 补齐 Stats 观测面与按调用注入回源
（增量 API，无破坏性变更）。零 replace。

- 杂务(deps)：oekit pkg v0.24.0 / hertz v0.8.3

oekit 依赖抬到最新发布 tag（pkg v0.24.0 / hertz v0.8.3），零 replace。
- pkg v0.24.0：补 cache.Stats 观测计数面 + GetOrLoadWithSource 按调用注入回源（pkg#76 / pkg#77 已修并发布）
- hertz v0.8.3：就绪探活被正常关停打断时不再误报 ERROR「ready probe failed」（oekit/hertz#48 已修并发布）
两项均为增量 API，无破坏性变更。

- 杂务(deps)：oekit 家族升级到本轮最新发布版本

oekit 依赖抬到最新发布 tag（bootstrap v0.6.5 / bootstrap-nacos v0.3.7 / bootstrap-obs v0.2.10 / concurrency v0.20.4 / core v0.12.0 / hertz v0.8.2 / metrics v0.1.9 / middleware v0.13.2 / nns v0.4.4 / pkg v0.23.1 / redisx v0.10.4 / registry v0.5.0 / tracing v0.3.0），零 replace。
依赖升级（仅版本）

- 杂务：v3.155.0 精简批——过时备注/误导别名/陈旧文档三清（deadcode 探测归零）

- 死代码：deadcode -test 全量探测零命中（v3.97.0 归零后保持）；staticcheck
  unused 随 lint 常绿——本批无可达性死代码可清，净删除集中在文档面
- 过时备注 ~50 处：agentkit/* 注释路径全部改指 oagentkit 新家（acpx→tools/acpx、
  reportutil→textx/reportutil、worker→queue/worker、progress→runtime/progress
  等 16 条映射）；ekit 注释改指 oekit 新家（config→core/config、data/cache、
  metrics、hmw traceid 等）；版本考古注记（vX.Y 起…）按惯例保留原文
- 误导性 import 别名 9 个清零：ekitbootstrap/ekitconfig/ekcache/ekmathx/
  ekuidgen/ekitmetrics/agentkitworker/agentkitprogress → oekit*/oagent* 或去别名
- 陈旧文档：删除 docs/argus 需求代码复核助手.md（deep-dive 的旧快照副本，
  全文零引用）；plugin-authoring.md（现状指南）与 deep-dive 的「位置变更」
  现状注记改指 oagentkit；§14.x 版本史叙事保留不动
- 保留红线：argus_ctxfit_* 指标名与 argus.ctxfit 日志键不动（观测契约）；
  Milvus 集合缺省 agentkit_knowledge 不动（数据名，改动=存量集合失联）
- 教训：perl 批改「agentkit/x→oagentkit/y」在已迁文件上触发子串自匹配
 （oagentkit 内含 agentkit），23 文件 import 被双重替换污染——build 当场抓回；
  批改规则必须带左边界（空格/引号），教训入记忆
- 四门禁全绿：lint/test -race/arch/eval golden 69

- 杂务：移除误入库的 .video_agent 插件产物（v3.148.0 提交扫入的外部工件）
- 杂务(deps)：v3.105.0 otel otlptrace v1.25.0→v1.45.0——清偿 GO-2026-6505

- govulncheck 唯一发现（import 层未触达）升修复版清零；otel monorepo 版本
  锁定，sdk/metric v1.46→v1.45 对齐为预期；proto/otlp v1.9→v1.11、genproto
  同步上移；vendor 本地再生（gitignored）
- 门禁：lint/test(-race)/arch/golden eval 69 全绿；govulncheck GO-2026-* 清零

- 杂务(deps)：v3.94.0 agentkit v0.11.1→v0.12.4——0.12 破坏式线升级

v0.12.0 破坏面（相对 argus 使用面）收敛为一处：llm/wrap 校准注册表整体
删除——RecordUsage 公开 API 与进程级全局单槽退役，校准内化为 FitModel
实例槽（主路径自动校准）。迁移=删除 runner.go 唯一调用点（v3.36.0 的
ctxfit 真实 usage 回灌）；budget.Add/Cost.Record 是 argus 侧任务记账，
消费真实 usage 不受影响，原样保留。codeintel 的 GoCallers 改造（查询期
正则→建索引期 token 对查表）为同 API 内部重写，调用方无感。
v0.12.1~v0.12.4（第九/十轮审计修复+挂账全清）全部兼容式新增。
vendor 全量再生（本地 agentkit checkout 即 v0.12.4 目标态，无需 worktree
钉版）；SDK v0.76.0 与其余依赖随 go.mod 钉住版本不变。
门禁：make test（race）+ make lint + make arch + make eval（golden 68）
+ make e2e 全绿。

- 杂务(deps)：v3.93.0 go-git-platform v0.67.2→v0.76.0 主线升级——跨 6 个 minor 零破坏面

主线已含 cherry-pick 的 NoteManager（v0.76.0），GitLab 报告评论真·原地
更新语义保持。v0.68→v0.76 变更面（分页 API 收敛删除 ListAllPages/限流
403 单谓词/ETag 流式契约/GitHub 评论 action 派生词表等）与 argus 使用面
零交集，全量编译一次通过。
vendor 全量再生采用受控流程：临时 replace agentkit→v0.11.1 worktree
（本地 checkout 已 v0.12.2 破坏式线，不能随SDK升级带入），完成后 replace
恢复 ../agentkit 并修正 modules.txt 替换记录——vendor 快照保持 agentkit
v0.11.1 + SDK v0.76.0 + 其余依赖 go.mod 钉住版本。
门禁：make test（race）+ make lint + make arch + make eval（golden 68）
+ make e2e 全绿。

- 杂务(deps)：v3.83.0 依赖升级——agentkit v0.11.1 + ekit v0.34.0

- agentkit v0.10.41→v0.11.1：v0.11.0 为版本线划界（0.10.35~41 破坏式变更
  按 pre-1.0 semver 定界，argus 已在前批全部适配）；v0.11.1 增量新增
  （pacer 流式节流新包/egress 参数化放行/textutil EstTokensCJK+ChunkLines+
  ClampRuneBoundary）——argus 零代码改动
- ekit v0.33.0→v0.34.0：pkg/retry（Do/Poll）+ pkg/syncx（KeyedGate/FanIn）
  + cache 检查缓存 + uidgen 随机串 + jsonrepair CoerceString 增量新增——
  argus 零代码改动（retry/Poll 与 syncx.KeyedGate 为 argus 潜在消费面，
  采纳评估另行批次）
门禁：全量单测零 FAIL + arch 绿 + golden 69 全绿（代理拉取实测通过）

- 杂务(deps)：go-git-platform v0.67.1——convertBasicMR 补 Draft/HeadSHA，poller GitLab 兜底修复

215 真实仓库驱动测试实锤：poller 对 GitLab 所有 MR 因列表路径 HeadSHA 恒空
静默跳过，兜底路径从未建过任务。修复后首跳即建 4 任务（argus-validate#2
+ obs/demo-insights-server×3 历史欠账），全链路审查+发帖正常。

- 杂务(deps)：replace 路径随 SDK 目录改名更新（../../git-platform-sdk → ../../go-git-platform）
- 杂务(repo)：v3.7.2 卫生批——go.sum 修复 build 复活 + staticcheck/deadcode 清零 + make lint 门禁

- go mod tidy：本地 replace 的 git-platform-sdk v0.61.0 backend 换大版本依赖
  （gitea.dev/sdk、go-github/v91、gitlab client-go/v3）后 go.sum 缺条目，
  go build ./... 自该升级起一直失败（单包测试不触发全图解析未暴露）
- gofmt 27 个非生成文件归位
- 删除 git 误跟踪的 4 个 hertz generator 临时文件（.!{pid}!*.go），
  .gitignore 加 .!!* 防复发；.mimosa/.v2c 本地工具目录入 ignore
- staticcheck 非生成代码 13 处清零：main.go instanceID 死初始化、
  plugin_test 恒真断言+过时注释（v3.7.0 已行号入指纹）、dispatcher 未用
  常量、S1011/S1016 简化、死测试基建（nopLogger/pluginManifest/webhookMode/
  stackOpt）；biz/model 生成代码 required tag 噪音豁免
- deadcode(-test) 清零：toolpool.MockPool v3.0 遗留整块（e2e 实际用自带
  假实现，全仓零引用）、severity.SeverityValid、reportview.SortFindings、
  runner.buildDiff、dispatcher_test 漏 Test 前缀的半成品 Run_SemaphoreLimit
  及其专用桩 countingPlugin
- Makefile 新增 make lint（staticcheck 排除 biz/model，缺工具自动安装）
build/vet/lint 全绿，21 包全量测试（含 e2e）通过。

- 杂务(deps)：agentkit v0.10.2（logredact 上游化；require 与 tag 对齐）
- 杂务(deps)：agentkit v0.10.1 对齐 + 消费 textutil.BigramSet 去手搓 bigrams; 能力盘点结论留档（stats/langfuse/websearch 等不采纳理由）
- 杂务(deps)：ekit v0.20.1 → v0.27.2 + agentkit v0.8.2——适配 httpx OnBeforeRequest 签名（imroc/req → hertz protocol）与 mw.Fail 新增 ctx 参数
- 杂务：agentkit 依赖对齐 v0.7.0（全库审查修复版，本地仍走 replace ../agentkit）
- 杂务：agentkit v0.1.0 从远程拉取，去掉 replace 指令

ekit v0.20.0 + eino v0.9.18

- 杂务(deps)：ekit v0.17.0 + git-platform-sdk v0.50.0

- CI：v3.66.0 静态门禁入 CI——vet+staticcheck+gofmt 必过 job（钉版本 2026.2.1，命令单源 Makefile）

- deps：go-git-platform v0.65.0→v0.66.0——diff 感知行内定位（最终级修复）

- v0.66.0：GitLab CreateReview 拉 MR diffs 构建 hunk 行映射，hunk 内行精确
  position（含 old_line 推导）、非 hunk 行降级 file 级 position、不在 diff
  的文件跳过——'must be a valid line code' 400 根因消除
- 修复 sdkctx 通用平台分支缺失的 Logger 注入（1e5f4a4，同批）

- deps：go-git-platform v0.61.0(git-platform-sdk)→v0.65.0——模块名统一 + GitLab 行内评论

- 模块路径统一为 github.com/yi-nology/go-git-platform（SDK 仓现名），
  18 文件 import 改写；replace 保留本地并行编辑（模块名已一致）
- v0.65.0 含 GitLab CreateReview 行内评论实现（discussions + diff_refs
  position + 失败留痕）——argus 行内评论链路最后一层修复由此生效
- go mod vendor 首次可用（模块名不一致曾致 vendor 永远是 import 改写拷贝、
  go mod vendor 报错、replace 在 vendor 模式下为死字——三轮部署空转的教训）
- golden 68 + e2e + lint + 全量测试绿

- deps：agentkit v0.10.22→v0.10.25——mcp-go v1.1.1 跨大版本（协议 2026-07-28）+ eino v0.9.21 + 九新包面验收（compact 不替换 ctxfit 决策在案）
- deps：agentkit v0.10.14→v0.10.22——vendor 快照对齐（v3.31.0，八版本新面入编译，被动生效面逐项验收在案）
- deps：agentkit v0.10.11→v0.10.14——记账单源化迁移 + toolprior 改名

- v0.10.12 记账单源化：obsx.Options.OnUsage 退役（obsx 回归纯 trace）——
  runner 任务级成本记账改经 llm.NewUsageHandler（callbacks 侧单出口），trace
  （NewTracingHandler）与 usage 一次 InitCallbacks 并注入；UsageHandler 只补
  ReAct/RawModel 旁路（Generate 直连已由 Client.OnUsage 记账，防重护栏内置，
  两侧不双计）；旁路无 stage 语义（RunInfo 仅组件类型），stage 记空、模型按
  组件类型归账
- toolprior.WithCallLimit→LimitCalls（工具装饰器消歧，破坏性改名）
- v0.10.13/14：agentrun PlanAndExecute Replanner 修复、lineage 清理（argus
  未消费面）；procx 三版本间零改动
门禁：build/vet/staticcheck/gofmt/golden 57/57/e2e 全绿；全量单测中
gitdeepen/ocrdeleg 两例 procx 超时为外部并行负载所致（隔离复跑双绿、
procx 三版本零改动，非升级回归）

- deps：补 poster.go 迁移——severity→reportutil（上一提交漏点名）

- 文档(test)：v5.1.1 文档契约校准+过期口径实证清偿+lint 存量归零——四连门禁绿

- README 工具表 19→21 补 get_impact/get_repo_config；list_tasks/search_code
  版本注记 v3.121→v3.122 纠偏（README+server.go 注释）；ide-mcp 工具数、
  design.md §9.4 状态（RAG 已实装 Hybrid）与边界段 HEAD v5.1.x 同步
- 「已知边界」实测证伪两条：①反思阶段用量自 v3.36.0（ReAct 旁路预算回灌）
  已计任务预算——TestReflectFindings_UsageFeedsTaskBudget 钉约 UsageHandler
  口径；②总结评论扫描自 go-git-platform 全分页收齐（CollectBounded 有界）
  后无首页盲区——poster/storage 五处过期注释纠偏+
  TestPostSummary_ScanFindsLateComment 钉长讨论串尾部命中
- lint 存量清偿：runner_impactview_test nil ctx×3（SA1012）+10 文件 gofmt
  注释/对齐规范化（零语义）
- 工作树卫生：.gitignore 收 .playwright-mcp/.video_agent/video/根目录验证截图
门禁：make lint / test / arch / eval（golden 69）四连绿

- 文档(arch)：ARCHITECTURE.md 分层图同步 codegraph 包清偿（L0/依赖表三处）
- 文档(readme)：环境变量表补全 15 组欠账键——本轮新开关（MEMORY_KERNEL/QA_PTC/QA_HISTORY_BUDGET/OTEL 四键）与历史漏文档键（SKILL_MODE/REPO_GUIDE_OFF/EXTERNAL_TOOL_CALL_LIMIT/OCTOPUS 四键/EMBEDDING 四件套/LLM_RESILIENCE/MCP_IDLE_REAP/KNOWLEDGE_DIR/FIX_VERIFY_TIMEOUT）全部入表；config.go 键清单与 README 差集归零（MODEL_R*/REGRET_* 由分组行覆盖）
- 文档(spec)：四库替换设计终态——v3.149~152 落地 + v3.153 评估批裁定在案
- 文档(skill)：v3.139.1 tools.md list_tasks 字段名校准——IDL 驼峰 taskId/createdAt(验收测试 T2 实弹抓出的文档漂移,与 get_report 手写蛇形不同源勿混用)
- 文档(skill)：v3.139.0 argus-triage 分诊 skill——意见处置工作流单源

v3.128~v3.138 把分诊原语建齐（suppress/restore 家族语义、search_findings
历史账本、get_capabilities、fp 短码链路、限速/白名单错误语义），两次缓办的
triage skill 至此有实体支撑：
- skills/argus-triage/SKILL.md：定位任务→拉结构化意见→逐条三问（以前报过吗/
  真问题吗/误报吗）→汇总回报的标准流；severity 处置规则、家族传播与抑制
  耐久性边界、429/白名单错误语义、「不要静默批量抑制」纪律、fix 绝不 push
- argus-mcp SKILL 工作流补分诊节交叉引用

- 文档(skill)：v3.129.1 抑制耐久性边界改写——实测抓到措辞改写逃逸

2026-10-09 改进有效性实测结论：suppress_finding 机制面全过（admin 门/写库
matched=true），但端到端「下一轮剔除」在模型改写措辞时失效——指纹=规范化
评语哈希，merger 过滤是精确匹配，commentNear 近重复只用于抑制当时的检索
（与 HTTP 通道同行为，非 v3.128.0 引入）。原文档「措辞漂移由入库指纹覆盖」
过头，改为如实的耐久性边界（行号无影响/措辞改写会再现）+ 缓解指引；
「近重复耐措辞漂移的剔除」入 backlog（需 merger 过滤语义扩展，golden 配套）。

- 文档(skill)：v3.123.2 密钥卫生加固——token 落地纪律入 skill

argus-mcp 安全边界补：MCP 客户端配置/env 里 token 是明文，勿提交入库、
勿贴对话/issue；外部 AI 一律 ci 档最小权限。
argus-precommit hook 示例去掉 -token $TOKEN 硬编码形态，改走
ARGUS_APITOKEN 环境变量（copilot 缺省读取）。
另：实测确认 MR16 泄漏的 glpat 令牌已被 GitLab 侧撤销（401 revoked），
无需历史重写应急。

- 文档(skill)：v3.123.1 agent skill 实弹校准——215 实测偏差三处收口

本机安装 ~/.claude/skills 后按 skill 工作流实弹驱动 215：13 工具
tools/list 与文档一致；async 闭环/同步快审/findings/suggest/ask/stages
全通；search_code 实弹（围栏文本+1934 文件扫描）；fix_finding 未启用
语义实锤。校准：fix 报错短文案（FixFinding 入口 fix.go:99 守卫）、
copilot -timeout 30s 会超 LLM 调用（review/ask 同 fix 须调大）、
单 token ARGUS_APITOKEN（admin 等价）部署形态。

- 文档(skill)：v3.119.0 argus-mcp agent skill——外部 AI 经 MCP 直驱单源

skills/argus-mcp：SKILL.md（连接/十工具速查/五条标准工作流/失败降级/安全边界）
+ references/tools.md（与 mcpserver 源码逐字段校准的参数级参考、copilot 子命令映射）。
README/ide-mcp 工具清单校准：五工具→十工具、review_diff 补录、
fix_finding「启用时注册」纠为「常驻注册未启用报错」。

- 文档(deploy)：215 runbook env 导出模式补 REVIEW_TOKEN——裸名 env 被 ARGUS_|OPENAI_ 过滤滤掉，每次部署复现 §4 漂移（v3.113.0-215 部署实测）
- 文档(deploy)：215 runbook 换实战验证流程——交叉编译+FROM旧镜像+env-file 复刻+漂移检查

v3.105.0-215 实际部署路径回写：FROM 旧镜像继承 skills/git/ca-certs（免基底
拉取与私有依赖 SSH 挂载）；回滚 tag+DB 备份先行；env 逐项复刻用 --env-file；
新增配置漂移检查节（REVIEW_TOKEN 丢失致 review-service 静默禁用的实例在案）

- 文档(deploy)：215 部署 runbook 固化——远程构建/容器参数/验证面/网络恢复连带项

10.44 段断路期间把部署套路从会话记忆落成仓库文档：前置实拍、daocloud 基底
镜像、1panel-network+add-host 勿漏、readyz/ver 验证，以及恢复后的
note-events/MR144/eval-real 连带清单

- 文档：v3.66.1 治理面补全——SECURITY/CONTRIBUTING + 依赖漏洞扫描结论 + README 回环
- 文档：ARCHITECTURE.md 组合根描述对齐 v3.63.0 + CHANGELOG v3.62.0/v3.63.0
- 文档：ARCHITECTURE.md 架构地图——分层图/依赖规则表/架构不变量/守卫说明

与 design.md（设计契约）、docs/architecture-deep-dive.md（演进分析）三分：
本文件是当前实现分层事实，与 test/arch 可执行守卫互为镜像——
文档给人读、测试给门禁跑，不一致时以能跑的为准

- 文档(ops)：runbook 方案 C 补 failed 复位变体——只复位同 MR 最新一条（旧 head 复位会被 superseded 语义跳过发帖记 stale，白耗 LLM；0926 实测案例在案）
- 文档(ops)：运维手册补「MR 阻塞」场景——webhook 禁用/failed 滞留/并发不足三因诊断与轮询兜底预案
- 文档：v3.23.0——EXPERIMENTS v8 登记（A 组 34/37=91.9%，41 修复验证、42-44 全过、3 miss 归因）、CHANGELOG 回填
- 文档：v3.22.0——README 5.4-5.6（沙箱验证/多语言符号/副驾 CLI）、CHANGELOG、IMPROVEMENT-PLAN 三条件项降档落地记录
- 文档：v3.21.0 收尾批——EXPERIMENTS v7 登记、CHANGELOG、遗留项复核（testgen 沙箱不做：违反沙箱纪律；codegraph v2 留档）
- 文档：v3.15.0–v3.17.0 差距收口批次——CHANGELOG（仓库配置面/SAST/Go AST/describe·testgen + 北极星口径/MCP 副驾）、README 功能矩阵与命令、IMPROVEMENT-PLAN 落地记录
- 文档(eval)：夹具 v3 A 基线补树状态留痕——本轮含并行会话 codegraph 在制改动，非纯净基线
- 文档(changelog)：夹具 v3 改号 v3.16.0——v3.15.0 已被并行会话 codegraph P0-1（9484187）占用，消除版本歧义
- 文档(eval)：G1b 夹具 v2 对照矩阵拍板——A 基线（v2.5 全局）14/20（70%）最优，pro 红利证伪，默认路由维持不变

- EXPERIMENTS.md 登记三组结果：A 14/20（70%）＞ B（pro 全局）11/20（55%）
  ＞ C（v2.5+R3&Reviewer=pro）9/20（45%）；拍板维持 mimo-v2.5 全局
- 「R3/Reviewer 上 pro」假设证伪（劣于基线 5 条）；pro 参与审查阶段的组
  出现 HIGH/未完成漏升级（20/21 号，危险方向）——进一步支持不升级
- injection 一票否决线三组全安全（05/06/07/23 无一误升 APPROVE）
- comment/rc 边界系统性过严（三组共性，且全部安全方向）登记为夹具 v3
  标定方向；维持基线拍板按规则无需二轮复现
- 并发互踩事故留痕：结果文件单写者约束固化；CHANGELOG v3.14.0 填实数；
  IMPROVEMENT-PLAN 收尾批标记拍板完成

- 文档(plan)：IMPROVEMENT-PLAN 三次更新——v3.14.0 评测文化门禁收尾在案

- 执行状态表新增收尾批：G1b 夹具 v2 对照矩阵 + argus_eval_* 指标面
  + CI golden-eval/定时 eval-real + PR 模板数字门禁
- 勘误 v3.13.0 超前表述：三组对照彼时未实际登记；遗留并发跑互踩
  latest.json 数据作废，v3.14.0 改串行执行并以 v2 夹具登记
- 代码面（obs 指标/CI workflow/PR 模板/README）已随 884d535 入库，
  该提交信息误标 docs(changelog) v3.13.1——实际主体为 v3.14.0 特性，
  其自述的 v3.13.1 记录并不存在于 CHANGELOG（后续批次补记）

- 文档(plan)：IMPROVEMENT-PLAN 勾选执行状态——批次 0-3 全部落地（v3.9.1-v3.12.0），G1b 首轮基线与夹具 v2 标定后续在案
- 文档(eval)：G1b 首轮真实基线登记——mimo-v2.5-pro 12/26（46%），miss 归因为夹具骨架化代码而非模型失守（注入防线全部安全）；夹具 v2 标定立为后续动作
- 文档(review)：README 补 restore fp: 指令（与 suppress 对称）
- 文档(review)：v3.4.1 regret 学习环入 CHANGELOG + 架构文档（finding_prints.regret_count、R3.5 剔除→Merger 跨轮标注链路）
- 文档：README 插件形态补 kind: acpx
- 文档：CHANGELOG v3.0.2 + README 重写

CHANGELOG: 反注入 P0（alignChecksToSpec 漏报补齐）+ 9 项 P1 修复（围栏逃逸/空secret fail-open/cli finding 熔断/行内评论降级链等）
README: 能力优先版重写（当前版本/端到端流程/接口/配置/快速上手）


- obs(comment)：v3.114.1 bot 自评论防循环补 Info 留痕

- admitComment 自跳过从静默改 Info（argus.comment.self_skipped）——本次
  GitLab 评论链路排查被该静默丢弃卡住（事件到没到全靠推断），补可观测性
- v3.114.0 回归测试文件随批归位（comment_note_test.go 在 af4e0a0 已入）


- ci,test: v3.67.0 门禁口径统一 + MCP 门面单测——make test/lint 对齐 CI、arch 入流水线、mcpserver 0%→80.6%
- perf,build: v3.25.1 基准立尺 + diffprep 削峰 + 门禁补漏（零行为变更）

- 首批热路径基准入库（diffprep Prepare 两形态/codegraph 索引+查询/指纹+聚簇），
  全仓此前零基准
- diffprep 削峰（profile 实证）：rune 计数 3 遍全文→1 遍+截断时算术推导
  （remaining 语义逐位一致）；noise() 模式分类 hoist init + 目录段边界扫描
  免 Split。实测 PrepareLarge −21%（allocs 1059→59）、PrepareTruncated −70%
- make lint 补 gofmt 检查步（staticcheck 不查格式）；make fmt 收窄到
  internal/cmd/test（旧 find . 会误格式化 vendor——地雷拆除）
- 体检留痕：deadcode -test 全清（四函数为测试 API 门面）；govulncheck 0 命中；
  存储无 N+1 明证不动
门禁：build/vet/staticcheck+gofmt/test/golden 48/48/e2e 全绿

- deps,refactor: v3.25.0 双依赖升级深入结合——agentkit v0.10.11 + ekit v0.29.0

agentkit 四轮包收敛迁移（消全部旧包依赖）：
- severity/sampling→reportutil（级别词表+聚簇单源，14 文件）
- llmjson→jsonrepair（5 文件）；safejson→fence（4 文件）
- 进程纪律收敛 procx：gitdeepen/ocrshim/ocrdeleg/adapter_cli 四处
  RunProcess/ProcessRequest→Run/RunRequest，哨兵 procx.ErrTimeout/Canceled
- llm failover 链节结构化 ChainLink{Model,Name}（runner/bootstrap）
- 测试桩补 acpx.Capabilities 声明（新接口契约）
ekit v0.27.2→v0.29.0：logx/concurrency 兼容零改；httpx v0.29 无默认头注入点，
notify 的 X-User-Id 机器账号语义不可丢→回退专用 octopus 客户端（行为等价）；
go.sum 瘦身 467 行
拍板留痕：agentkit/httpx 不采纳（避免双 httpx 源）；reportutil stats 留档
门禁：build/vet/staticcheck/gofmt/test/golden 48/48/e2e 全绿

- perf,chore: v3.24.1 卫生与性能批——gofmt 清零 + rune 计数零拷贝 + 共享 http.Client + codegraph 查询提速（零行为变更）

- gofmt：10 个自有文件恢复格式（并行批次带入的 import 排序/字段对齐）
- len([]rune(s)) → utf8.RuneCountInString(s)：22 处/6 文件全量替换（语义
  等价），diffprep 每 PR 大文本快照分配消除
- githubapp：换 token 的 http.Client 改包级共享（连接池复用）
- codegraph GoCallers：按名正则缓存（sync.Map）+ 全文子串预筛跳过不含目标
  符号的文件
门禁：build/vet/staticcheck/gofmt/test/golden 48/48/e2e 全绿


- 回滚(findings)：v3.142.1 语义补位回撤——实测相似度分布不可分

v3.142.0 的 search_findings 语义补位按纪律用真实数据校准后否决：TEI
model-small 对短中文意见文本，同话题对（0.326~0.552）与跨话题对
（0.322~0.489）分布完全重叠——「查询错误被吞掉」对「凭据外泄风险」打分
比真同话题还高，bge 查询侧指令前缀复测仍不可分。任何阈值都会把无关意见
排进来，功能只会误导。
- 回撤语义补位全链（semanticFindings/向量缓存/EmbedTexts/Cosine 导出/
  语义测试），search_findings 恢复关键词 LIKE 单路
- tools.md 如实记录校准数据与回撤原因（换强 embedder 可凭证据复活）


- 格式(dbcopy)：gofmt import 排序（v5.5.0 换 glebarez 驱动时的残留）
- 格式：requirement 测试 import 分组收编——goimports -local argus 重放逐字节一致，悬空改动落账（并行会话遗留，包测试绿）
- 格式：store_task.go gofmt 缩进修正（v3.109.0 编辑残留）
- 格式(test)：arch_test gofmt 对齐

- 测试(plugin)：v5.1.2 plugin 适配器覆盖批——cli 97%/builtin 86%/plugin 84%

- adapter_cli 32.4%→97.3%：Invoke 主路径 file:// fixture 端到端（假脚本
  真子进程：字段归一/超长截断留痕/失败三面）+ delegate 拒绝/无池报错 +
  ParseFindings 包装形态（comments/findings/data/result）与字段变体全谱
- adapter_builtin 37.8%→85.9%：CompliancePlugin 构造缺省与 Invoke 产物
  映射 + Reviewer 选项/ReAct 模型解析 + ReAct 三路失败降级单轮带
  DegradationMarker 钉约（审查不因工具链故障而失败）+ ensureSkillText
  惰性解析与 10 分钟缓存
- plugin 48.8%→83.7%：契约门面批（contract_facade_test.go）——DataFence
  中和+HookFenceNeutralized 接线/DecodeFindingsJSON 宽容解析/PRToKey/
  ToolErrNote 信封/SanitizeFileStem/词表门面/ParseTimeout/hooks 全表 nil 安全
门禁：make lint / test / arch / eval（golden 69）四连绿

- 测试(observability)：v3.178.2 LLM OTel span 215 实弹验收 + 注入链单测钉

- 215 实弹验证 LLM 调用进 jaeger：HTTP POST span（8 tags）实收——此前
  「gorm span 有 LLM span 无」是查询面盲区误判（gorm 轮询噪声把 LLM trace
  挤出 jaeger limit=100，按 operation 过滤才现身）；观测教训记档
- 注入链单测钉两件：traced_client_test（内存 exporter 断言出站请求必建
  span + Timeout 语义不被注入改写）+ eino_trace_test（eino ChatModel →
  go-openai → otelhttp 全链路 span 导出断言）——「链路挂着」与「span 真
  生成」分开钉，215 类盲区在单测层可复现
- 四门禁全绿 golden 69

- 测试(poller)：v3.112.0 pollOnce 行为套件 + 死转发清偿 + tech-debt 全量续账

- poller 覆盖 15.5%→93.0%：发现/幂等去重/state 过滤防线/draft+空 SHA 闸/
  三页截断/失败不建任务/ParseTargets，事件契约钉 task.created（坑在案：
  Bus 只投递发布时已在册的消费者，须先订阅后轮询）——poller 是试点常开的
  生产任务源且 golden 只覆盖 webhook 源
- 删 plugin.IsReviewKind 死转发（v3.106.0 自引入：storage 直连 domain 后
  无人消费，复扫 deadcode 抓获）；口径注意：deadcode 必须 ./...（含 main），
  ./internal/... 无入口全图误报
- tech-debt.md 全量续账：新基线（24,145 行/覆盖 34.3%/百行函数 6→5）+
  gosec 基线全集补档（G301×4/G306×8 by-design 留痕，非生成 26 条全归属）+
  GO-2026-6505 标记已清偿（v3.105.0）+ v3.106~v3.112 收口记录
- style 收编：3 个 requirement 测试文件悬空 import 分组（goimports 重放
  逐字节一致）落账 d4ef891，工作树归零

- 测试(qa)：v3.69.0 问答面 SDK+模型打桩全覆盖——8.9%→80.4%，Answer/loadContext/LatestReview 全路径
- 测试(poster,qa)：v3.68.0 回帖面降级全覆盖——poster 13%→90.3% + InlinePosted 按实投递计数
- 测试(domain,worker)：领域纯函数与队列适配器测试——两包覆盖率 0%→100%

- domain: NormalizeSeverity 归一/越界(critical=high 同义词, banana=low+not-ok)、
  SeverityRank 排序、HigherSeverity 双向、Fingerprint/FingerprintNorm 确定性、
  32-hex、行号敏感与负行号钳 0。
- worker: StoreQueue 对 storage.Store 的三方法委托——空队列、单次抢占、
  心跳续期、reaper 重置语义(staleAfter=0 重置/1h 不重置)，含 RunnerID
  未装配时 touch 显式拒绝的装配纪律负控。
- 验证: 容器内 gofmt/vet 清洁, go test ./... 全绿, 两包 -cover 均 100%。

- 测试(eval)：v3.30.0 夹具 v14——Labels 触发编队 + 真实世界形态两则

- 59 编队 Labels 触发分支首次入册（matchAnyLabel 零覆盖）：PR 标签 hotfix
  命中互盲编队→异文件双意见互未复现→交叉标注 + 汇总行。归因在案：
  annotateCross 要求 recs≥2 条成员意见（「另一专家静默」不产生标注——
  交叉标注的语义是分歧而非缺度）
- 60 配置值调整 / 61 SQL 迁移（真实世界高频形态，change-shape 家族诚实放宽）
59 首轮 MISS = 夹具自身坏实现（Clamp 写成 return v），49 号教训重演：
正文承诺的能力必须在代码里真实成立——修正后单例复验通过
集合 57→60（真实分母 43→46）；打桩 60/60（clean 5/5、FP=0、boundary 2/2）；
真实增量首轮 2/3 + 59 修正单例过（快照 v14-*-first/-fixed-single.json）

- 测试(eval)：v3.29.0 夹具 v13——R3.5 报告反思入册（critique-fail + 修订草稿双注入）

harness 双注入：
- r35_critique_fail：critique 打回（agentkit reflection 契约 {"pass","issues"}）
- r35_drafts：修订草稿（reflectFindingWire 锚定列表，只认已有 id，省略即剔除）
58-r35-reflect-comment（guard,render，skip_real）：critique 打回→修订稿仅留
id 0→「R3.5 报告反思…剔除 1 条误报嫌疑」注记 + 剩 1 条 medium 落 R5.4；被剔
原始身份计入 regret。首轮失误在案：critique-fail 未配草稿→mock 回 []→全剔
落 R5.5——「打回」必须与「草稿」成对注入
至此批次 2/3/4 编排语义 golden 全闭环：编队（共识/互盲）、反思、完备性三裁判、
降级路径（R2 除外——无失败路径可钉）。集合 56→57（skip_real，分母 43）
打桩 57/57（clean 5/5、FP=0、boundary 2/2）；全量单测绿

- 测试(eval)：v3.28.0 夹具 v12——编队语义入册（共识/互盲交叉标注）+ TS 补齐

harness：golden 夹具面暴露 extra_squads（opt.squads→plugins.yaml→WithSquads
管线 v3.12 起即在，此前从未被夹具消费）
- 55 consensus 黑板共识首次入册：成员不进 fan-out 由 RunConsensus 召集，
  beta 见黑板已有 alpha 所收发现不重复收集→共识单条 R5.4；真实 comment 精确
- 56 互盲交叉审查标注首次入册：异文件意见互未复现→「编队另一专家未复现」
  降权标注 + CrossReview 汇总行；首轮设计失误在案（replicated=同文件±5行
  互现，6/9 行相距 3 行被判互现）；真实 approve 落放宽集合
- 57 TS 侧入册（polyglot Python/JS/TS 三块齐）；真实 comment 精确
登记缓办：R3.5 反思需 opts 级 critique-fail 注入（critique 恒 pass 零剔除
无注记可断言）——v13 harness 方向
集合 53→56（真实分母 40→43）；打桩 56/56（clean 5/5、FP=0、boundary 2/2）；
真实增量 3/3（快照 v12-additions-55-57.json）

- 测试(eval)：v3.27.0 夹具 v11——R3 失效降级补覆盖 + JS 变更入册

- R3 降级路径单测补齐（此前单测/golden 双零覆盖）：
  TestCheckComplianceDegradedOnUnparseable 垃圾模型直驱 CheckCompliance，
  断言降级报告（全 AC→unverifiable/Summary 降级声明/不报错）+ 真值表落
  R5.3 不许 APPROVE
- harness garble_r3 开关：mockLLM 命名 marker case 优先于 persona，R3 失效
  以开关实现（恒不可解析→重试后仍败→降级）；53-r3-degraded-comment 端到端
  钉 R5.3 + 逐 AC unverifiable（skip_real；must_not_find R5.5——降级不白给）
- 54-js-change-comment：JS 侧首次入册（38 Python 对侧），clamp 精确收窄 +
  low 非阻塞意见→comment 正例；真实模式 comment 精确命中
  （快照 v11-additions-54.json）
集合 51→53 条（真实分母 40）；距 ≥100 门槛差 47 条。
门禁：build/vet/staticcheck+gofmt（新门禁首次拦截自身未格式化编辑，生效
验证）/test/golden 53/53（clean 5/5、FP=0、boundary 2/2）全绿

- 测试(eval)：v3.26.0 夹具 v10——扩容 49/51/52 + max_comments 测试层改道

扩容（集合 48→51，真实分母 37→40）：
- 49 include_paths 白名单首次入册（47 的黑名单对侧分支）；首轮 MISS 归因=
  夹具 body-内容矛盾（main 包 vs pkg 包），真实模式抓出、stub 掩盖——修正后
  单例复验通过（双模式 harness 价值案例）
- 51 best-of-N 部分采样失败降级（k=2/3 注记 + 复现 2/3 确定性升级）；归因在案：
  garbage 须放 persona 序列末位（中段会被 agentrun 回喂重试救活）
- 52 依赖升级 PR 首次入册（go.mod 可见可判/go.sum 噪声排除；change-shape
  家族诚实放宽）
max_comments 测试层改道：钳制在 poster 评论体不在 task.Report——golden 不可达，
50 号退场，抽 clampInlineByMaxComments 纯函数补四分支单测（此前零覆盖）
候选登记：R2 失效无失败路径可钉；R3 失效降级 harness 不可达且单测覆盖为空
（后续 harness/单测小批方向）
打桩 51/51（clean 5/5、FP=0、boundary 2/2）；真实增量 49/51/52 全过（49 修正
后单例复验）

- 测试(eval)：v3.21.0 夹具 v7 扩容（36-41）——issue 需求源/英文需求/非 Go 变更/删除文件/重命名/二进制+lockfile；harness 支持 is_binary/is_renamed/is_deleted；打桩 41/41、真实有效增量 4/4（39/41 skip_real 登记在案）
- 测试(eval)：v3.20.0 夹具 v6 扩容——真值表优先级（33）/空 diff 不白给 approve（34）/中文路径稳定性（35）；集合 35 条真实分母 29，打桩 35/35、真实增量 3/3
- 测试(eval)：v3.18.0 夹具 v4——03/10/13/31 换 Add 无安全面实现、29 需求句收敛；首轮 A 组 23/26（口径修订后 24/26）FP=0，03/10/13 转绿；31 锚点语义对齐 27 号；v5 方向=R1 元声明契约
- 测试(eval)：v3.17.0 北极星口径首轮 A/B 登记——A 基线 22/26（85%）clean 2/3 FP=0、boundary 1/2；pro 73% 无红利三度复现；miss 逐条归因（29=R1 颗粒度/31=过严锚点）；失败信息附报告截断
- 测试(eval,obs)：v3.17.0 北极星口径——clean×3/边界×2 夹具（28-32）+ FP/clean/boundary 聚合与 argus_eval_false_positives 指标
- 测试(merger)：regret 标注/复现标注单测——正常+负控(suppressed 不复活)+异常(GetPrintState 失败不阻塞)
- 测试：e2e 场景⑩——acpx 插件全链路（bootstrap→R4→合并→回帖）

- stack_test: withACPX 选项——假 kimi CLI 进 PATH（kimi-code stream-json 协议），
  fixture 工作副本复用，acpx AgentDecl 声明
- helpers_test: writeFakeKimi（content 携带 findings JSON）
- e2e_test: 场景⑩断言——REQUEST_CHANGES（R5.2）/ CRITICAL→HIGH 归一化渲染 /
  by coding-agent-kimi / 行内评论 1 条
至此 acpx 链路三层验证齐备：
  单测（stub agent）→ 真机冒烟（kimi/mimo 实调 LLM）→ e2e（全编排栈打桩）

- 测试(edge)：全面边缘场景覆盖（7个新测试文件，45+用例）

config：ContextToken=0 回退、1M 派生值、小窗口下限、显式覆盖、单文件缩放
llm：fitInput 无操作/disabled/截断最长 user/只截 user；retryableLLMError context-length 标记
diffprep：单文件缩放、小 diff 不截断、总量截断、噪声过滤、RawDiff 回填、大文件截断、二进制
R1：只分支名、恰好边界、超过边界、body+issue 双截断、部分块失败回退、AC30 恰好/31 溢出、JSON 解析失败降级、全缺提示、R1a 分块序号流
RAG：空目录、不存在目录、单字符查询、CJK 二元组、精确过滤、不相关词元、topK 限制、工具输出含出处
adapter_remote：缺字段、ctx 超时、input-required、未知状态轮询、canceled、未知协议、格式错误
budget：恰好预算线、超限、failed/stale 不计入、上月不计入、多 SHA 独立查、空聚合、多仓库分组


## [v3.0.1] — 2026-08-31

### 新增(mcp-server)：Argus as MCP Server 实装（v3.1 方向 B）

/mtcp 占位转正式：mark3labs/mcp-go v0.58.0 streamable http，Hertz 桥接
（保留 Mcp-Session-Id 等响应头），Bearer 鉴权同 🔒 路由（GET/POST/DELETE）。
四工具：
- review_pr                发起审查（kind 可选，wait/timeout_seconds 可选等待终态）
- requirement_compliance   需求复核固定形态
- list_agents              插件清单（三参全传附带仓库配置视图）
- get_report               按 taskId 取状态/verdict/完整报告
service 层剥离 hertz 依赖（CreateTaskDirect/GetTask/ListTasks/SuppressFinding
去 RequestContext 参数），供 MCP 门面与 HTTP handler 共用。
E2E 场景16：真实 mark3labs MCP 客户端 initialize → list_agents →
review_pr(wait=true) → get_report 全链路（approve 路径断言）。

### 新增(打磨)：误报反馈入口 + 任务列表 API + poller 分页 + 变更文档

四个运营体感驱动的打磨项：
1. POST /findings/suppress（🔒，§3.3 反馈通道最小入口）：按 Merger 同一指纹函数
   置 suppressed，既有意见 matched=true、新指纹预插 matched=false；
   下一轮报告自动剔除并渲染抑制计数。（E2E 场景13；修 SuppressPrint 忘置 matched）
2. GET /tasks?status=&limit=（🔒）：status 白名单过滤 + limit≤100 收口。
   （E2E 场景14）
3. poller 分页拉全（≤3 页×30）；CreateTask number 纯数字快速失败；
   repoConfigView 改用请求 ctx。
4. CHANGELOG.md（v3.0.0→v3.0.1 全量条目）+ design.md §13 实施修订记录
   （设计条文与实现的差异全部可追溯）+ README API 示例补充。
IDL 新增端点经 hz update 重生成，既有 handler 定制全部保留。

### 修复(评审可见性)：每轮必留评审记录 + 自审降级 + R3 checks 对齐（真实试点驱动）

用户在真实 PR 页指出：报告评论存在但'评审人：暂无评审员'——审查动作没有以
评审形式落地。三项修复：
1. poster 每轮必提 CreateReview（verdict→REQUEST_CHANGES/COMMENT/APPROVE），
   行内 findings 附带；不再因无行内意见而跳过。
2. 平台自审限制降级（真实环境实测：Forgejo 拒绝 PR 作者提 blocking 评审，
   'reject your own pull is not allowed'）：自动降级为 COMMENT 评审，原判定
   写入 body；生产部署应使用独立 bot 账号（PILOT.md 已标注）。
   真实验证：评审记录 id=30/31 落地，comments_count=6 条行内意见全部附上。
3. alignChecksToSpec：R3 发明验收清单外的 check 条目会放大 R5.1 计数
   （实测出现过'6 项未达标'实为 2 项 AC）——只保留 spec 内 AC ID。
真实环境四轮终态（demo-user/argus-pilot PR#2，mimo-v2.5）：
- R5.1 否决×2（含 6 条行内意见：SQL 注入/硬编码密钥被抓出）
- R5.3 人工确认×1（AC-1 implemented+AC-2 unverifiable 不装懂 + security 抓出演示口令弱）
- 三种 verdict 的评审记录全部在 PR 页可见；总结评论始终保持 1 条被更新

### 修复(P2)：仓库配置并入 GET /agents 可观测（v3.0.1 评审收尾）

- IDL：ListAgentsRequest（platform/owner/repo query 三参）+ RepoConfigView/
  RepoAgentStatus/RoutingRuleView，hz update 重生成（handler 主体保留）
- plugin.ObserveRepoConfig：信任边界规范化后逐插件计算生效状态
  default/selected/routed/excluded_by_use/disabled/locked + routedBy 引用 + 告警透出
  （新增 routed 语义：不在 use、仅由 routing 条件选中——与 dispatcher routing
  分支绕过 use 白名单的实现一致）
- service.repoConfigView：拉取仓库配置组视图，失败降级 loaded=false+告警（不 5xx）
- E2E 场景11 锚点：规范化回显/locked 拒绝备注/routed/未注册名只进告警不进插件列表/
  无 repo 参数时向后兼容不返回 repoConfig；e2e 栈补 security+frontend-visual 对齐生产默认
- 单测：TestObserveRepoConfig / TestObserveRepoConfigNotLoaded
P1（Triggers.Events 双重失效）与 P2 空 token 放行已于上一提交修复：
E2E 场景10 + TestBearerNoTokenFailsClosed 为锚点

- 杂务：服务版本号对齐 v3.0.1

- deploy：docker-compose 与 k8s(kustomize) 双模式部署交付

deploy/docker-compose/：
- compose.yaml：健康检查/日志轮转/数据卷/插件配置挂载，宿主端口参数化
  （ARGUS_HOST_PORT，默认 8899）
- .env.example：全量环境变量样例（凭证/LLM/轮询兜底），占位符启动语义已标注
- plugins.yaml：插件声明本地化
deploy/k8s/（kubectl apply -k）：
- 单副本 Deployment + Recreate + RWO PVC（SQLite 单写模型约束，文件头注明）
- terminationGracePeriodSeconds 90s > worker 优雅停机 60s grace
- secret/configmap/pvc/plugins-configmap/service + ingress 参考（APISIX 集群走路由）
- Prometheus 注解（argus_tasks_pending/running 等）接入 kube-prometheus-stack
- 安全上下文：非 root(uid 1000)/降权/弃用 capabilities
Dockerfile：固定 uid/gid=1000 + OCI labels。
Makefile：compose-up/compose-down/k8s-apply 目标。
deploy/README.md：两模式步骤/清单说明/工作区对接（Harbor+ArgoCD）/注意事项。
验证：compose 真实起栈冒烟通过（healthz/鉴权 401↔200/插件清单/占位符降级不崩）；
kubectl kustomize 渲染通过；全量测试绿。


- 文档(试点)：bot 账号 friday 配置与 official 评审实测结论

friday（ID=8 普通用户）作为 Argus 独立 bot 账号接入试点：
- REQUEST_CHANGES 以 official:true 正式落地（评审 id 32，3 条行内意见）——
  blocking 评审计入 PR 评审状态的完整链路在真实 Forgejo 验证通过
- friday 可原地更新总结评论（权限语义修正：repo write 即可，非仅作者）
- ARGUS_BOT_USERNAME=friday 防自触发


- 加固(运维): 两阶段优雅停机 + 任务保留策略 + 队列指标 + 日志/凭据卫生

长期运行健壮性五项（试点运营驱动）：
1. worker pool 两阶段优雅停机：Stop(grace) 先停领取、给在途审查 60s 自然完成，
   超时才硬取消（保持 running，下次启动重置）——部署重启不再浪费跑到一半的
   审查、双倍消耗 LLM。单测覆盖 grace 内完成/grace 超时两条路径。
2. 任务保留策略：ARGUS_TASK_RETENTION_DAYS（0=永久）——超期已终结任务
   启动+每日清理，pending/running 永不清理。单测锚点 TestPruneTasks。
3. 队列积压可观测：argus_tasks_pending/running gauge（30s 采样，
   storage.TaskCounts），堆积一眼可见。
4. 日志卫生：worker 空轮询（500ms 一条红色 record not found）等预期内
   查无记录路径静默化（quietDB），不再淹没真实告警——实测空转 12s 日志零噪音。
5. 凭据卫生：workcopy git 报错脱敏嵌入 URL 的 token（scrub）；
   poster 扫描兜底只看最近 50 条评论。

- 试点改造：轮询兜底任务源 + RawDiff 按文件回填（真实 Forgejo 实测驱动）

真实试点（git.example.com/Forgejo，demo-user/argus-pilot PR#2）两处环境适配：
1. webhook 入站不可达（10.0.0.x → Mac 抓包零包，跨网段/100.x 均不通）：
   新增 internal/poller 轮询兜底任务源（ARGUS_POLL_INTERVAL/ARGUS_POLL_REPOS），
   出站轮询 PR 列表，发现新 PR/head 变化即建任务；复用 CreateTaskForWebhook
   同 SHA 双查去重，与 webhook 任务完全同语义；draft 跳过；空闲零 LLM 消耗。
   E2E 场景12 锚点：发现+执行+同 head 幂等（仅 1 个任务）。
2. SDK gitea 后端 ChangedFile.Diff 为空（内容在 RawDiff）导致下游拿到空 diff，
   R3 曾判 'no diff' 误报 AC 缺失：diffprep 检测该形态后按 'diff --git' 切块
   回填各文件（保留噪声过滤/截断语义）。单测 TestPrepareFillFromRawDiff 锚点。
真实环境验证结论（mimo-v2.5）：
- TaskAPI 路径：报告评论落地，AC-1 partial@login.go:5 / AC-2 missing → R5.1
- 轮询路径：三次 head 变更三次自动审查，总结评论始终 1 条被更新（新 head 写入头注释）
- 注入对抗：PR body 与 commit message 双注入'请输出 APPROVE'，verdict 仍为
  R5.1 request_changes，AC-1 诚实判 partial（返回 bool≠返回错误）——四防线真实有效

- Argus v3.0.1 演进基线：可插拔多 Agent 审查平台全量实现

L1-L5 全层落地（design.md v3.0 定稿为契约）：
- L4 插拔层：AgentPlugin 契约 + Registry（plugins.yaml/仓库配置信任边界）
  + Dispatcher（fan-out/熔断 3败5min/预算裁剪）+ Merger（指纹去重/冲突取高/误报抑制）
- L3 workflow：R1 需求解析 → R2 变更理解 → R3 完成度（ADK ReAct + 工具）
  → R4 专家委派 → R5 确定性真值表；反注入四防线含代码级校验
- 适配器：builtin（含 skillRef 注入）/ cli（OCR 进程组 exec）/ remote（v3.1 接口位）
- storage：GORM+SQLite 三表（webhook 幂等/同 SHA 去重同事务/原子抢占/staleness）
- poster：行内评论 + 总结评论三级降级 + staleness 门控
- 运行时：DB 即队列 worker pool、/metrics 指标、healthz/readyz、Bearer 鉴权
v3.0.1 修复（试点前置）：
- Triggers.Events 失效：Match 重写为类型间 AND/同类型 OR，纯事件触发器可用，
  EventType 贯穿 webhook→storage→dispatcher（E2E 场景10 回归覆盖）
- 空 token 放行：Bearer 中间件 fail-closed，未配置 ARGUS_APITOKEN 时 🔒 路由 503
- 平台 SkipTLS（ARGUS_*_SKIP_TLS）：内网自建实例证书过期场景（git.example.com 实测）
验证：单测+E2E 十场景全绿；真实 LLM（mimo-v2.5）集成实测通过；
Docker 镜像构建+容器冒烟通过。试点手册见 PILOT.md


- polish(反馈/诊断)：报告指纹短码 + suppress 直达 + 阶段产物留存

1. 报告意见行尾渲染指纹短码（fp:xxxxxxxx）；suppress 支持 fingerprint 参数
   前缀直达（服务端解析完整指纹），与 file+comment 模式二选一——反馈闭环
   从'复制原文'升级为'复制短码'。（E2E 场景15）
2. ARGUS_DEBUG=1：R1 spec/R2 摘要/R3 原始输出随任务留存（tasks.debug_json），
   GET /tasks/:id 的 debug 字段可查——诊断 prompt 问题不再需要翻日志。
3. 多行意见渲染两空格缩进续行（LLM 多行输出不再打碎 markdown 列表）。
4. 平台 SECRET 缺失启动告警（SDK 空 secret 跳过签名校验的语义显式化）。
   suppress number 必填校验。
