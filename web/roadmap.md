# 版本规划

> 规划快照 2026-11；状态标注：✅ 已拍板（排期中）/ 🔍 待评估（做不做未定）/ 🔗 外部依赖。
> 已完成里程碑见 [CHANGELOG](https://github.com/yi-nology/argus/blob/main/CHANGELOG.md)。

## 已走过的路（里程碑速览）

<div class="roadmap-phase">
<div class="when">v3.x · 2026-09</div>
<h3>从审查 bot 到审查平台</h3>
<p>R1→R5 需求复核流水线、多专家插件编队、确定性真值表、golden 评测体系奠基；四轮差距收口对标业界（/describe 写作面、CI 失败归因、confidence 标注、修复验证环）。</p>
</div>

<div class="roadmap-phase">
<div class="when">v5.x · 2026-10 ~ 11</div>
<h3>图谱与生产化</h3>
<p>非 Go 词法图层、仓库级代码地图、函数级图 diff、消歧符号图（get_trace/get_symbol_context）、可选 go/types 真调用边；43 k3s 双副本+PostgreSQL 生产在役、多副本租约、webhook 幂等自注册。</p>
</div>

<div class="roadmap-phase">
<div class="when">v6.x · 2026-11</div>
<h3>开源与收口</h3>
<p>Apache-2.0 开源（全历史脱敏）、四库深度收敛、IDL 收敛、平台配置面七平台单一事实源、语言能力矩阵（internal/langcap）、宣传页 VuePress 2 + 真实回帖画廊。</p>
</div>

## 近期（v6.7+）

- ✅ **SQL 审查专家插件**（bounded 设计已出）：`skills/sql-audit` + skillRefs 接线——SQL 迁移/查询审查进 R4 专家面，覆盖 deploy-sql 类仓库的高频真实场景。
- ✅ **polyglot 词法表补 shell**（上游 [oagentkit/textx#83](https://github.com/oagentkit/textx/issues/83)）：get_poly_symbol 覆盖 shell（+.mjs/.cjs/.pyw），补齐后同步 internal/langcap。
- ✅ **QA 路径 DefaultBranch 漏填修复**（ops/tools#4 恒败的既有 bug 实锤）。
- ✅ **webhook 入站直连 43**：网络放行后 PUBLIC_URL 切换 + 滚动重启自动翻转全仓（215 前门退役）。
- 🔍 **gitcode / 腾讯工蜂实测**：接入面已随 v6.5.0 收口（SDK 后端+事件词表就绪），待真实实例实弹验证后转正。

## 中期（v7.0 方向）

- ✅ **Verified Fix Loop（v7.0）**：检测→patch→沙箱验证→**修复分支+PR** 全链落地——`ARGUS_FIX_PUSH=1` 推 `argus/fix/<task>` 分支并开 PR 进入原审查分支，不直推受保护分支；审计 `fix.push`/`fix.cr` 可追溯、回滚=关 PR 删分支；`ARGUS_FIX_PUSH=direct` 保留直推旧形态（显式选择）。运营指标 `argus_fix_verify_total`/`argus_fix_push_total` 回答「自动修复有效率」。
- 🔍 **本地 pre-commit 审查**：argus-copilot 扩展成 git hook 形态，同一套审查管线左移。
- 🔍 **非 Go 符号级索引**：semsearch 对 Python/JS/TS 从文件级摘要升级到声明级 chunk+调用边（当前 Go 专属）。
- 🔍 **ARGUS_GRAPH_PRECISE 生产档**：镜像叠 Go 工具链层启用 go/types 精确调用边（当前容器 GO-ABSENT 缺省关）。
- 🔍 **review metrics 趋势报表**：按仓审查质量/耗时/成本聚合面（usage 记账与指标已在，缺报表出口）。

## 长期愿景

- **组织级学习环**：跨仓误报判例/评审偏好共享（组织桶记忆写侧聚合），审查越用越准。
- **需求追溯闭环**：需求 issue → AC 清单 → 代码实现 → 测试证据的全链路追踪，让「做没做完」可审计。
- **多模态审查**：截图/设计稿对照 UI 变更（OCR 通路已在，深化设计走查）。

有想法？[Issues / Discussions](https://github.com/yi-nology/argus) 欢迎拍砖。
