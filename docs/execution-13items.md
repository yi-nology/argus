# 13 项全做执行清单（2026-11-13 拍板）

> 用户拍板：三层全做——二层六单（#24–#29 实装深化）+ 四层长期愿景三件 + 五层 tech-debt 四件。
> 部署/43 现场事项全部搁置（用户不在公司）。本清单是活文档：每批完成勾账，
> 向后演进时在此追加批次细节。推进方式=批间顺序推进、批内逐项落地。

## 批次总览

| 批 | 内容 | 性质 | 状态 |
|---|---|---|---|
| **A 小赢清账** | #27 趋势接线 + #29 CI helm-lint + P2-1~10 打磨 + ptc 划账 + ggp 升级尝试 + #26 验收实测 | bounded 小 | **进行中** |
| **B 评测合流** | #25+#28 真实 44 案跑批归档新基线 + 按月曲线消费 + known_fp 误报治理 | bounded 中 | 待启 |
| **C 容器级 chaos** | #24 testcontainers pg/MySQL 真依赖故障注入 + 门禁 | bounded 大 | 待启 |
| **D 长函数全拆** | 重扫真清单（旧表过时，htmlTemplate 577 行）→ 推翻留置裁定逐个拆 + 覆盖率目标 40% | bounded 大 | 待启 |
| **E 组织级学习环** | 跨仓误报判例共享（组织桶写侧聚合） | architectural，独立 spec | 待启 |
| **F 需求追溯闭环** | issue→AC→代码→测试证据全链追踪 | architectural，独立 spec | 待启 |
| **G 多模态审查** | 截图/设计稿对照 UI 变更（OCR 通路已在，设计走查深化） | architectural，独立 spec | 待启 |

## 已拍板的边界决策（勿再问）

- **现网已裁决 MR 样例回填**：本批跳过（需 43 生产数据，用户不在公司）——标注为回公司后的后续批；
  起步集合成夹具口径维持 docs/eval-baseline.md 既有声明。
- **长函数拆法 = A**：推翻既往「留置」裁定，全部 >100 行函数都拆；先重扫真清单。
- **部署/43/webhook 直连/token 续购**：全部搁置，不在本 13 项内。
- **覆盖率目标 = 40%**（34.3% 基线 → 40%，随批 D 验收）。

## 批 A 明细（✅ 2026-11-14 完成）

- [x] A1 #27：`renderConsoleTrend` 接线（`qconsole.LoadEvalTrend` 只读投影），
      总指标卡+分语言表+缺位占位；config `ARGUS_EVAL_REPORT`；两态测试；
      arch 白名单 service→qconsole 单向补边。
- [x] A2 #29：`ci.yml` 增 `helm` job（lint + template 双路径冒烟含 pgRestore）；
      deploy-production.md「CI 未接」欠账声明同步销。
- [x] A3 P2：报告归档 `docs/archive/2026-11-11-arch-hygiene-report.md`（状态头：
      7 条已被 v6.3.0 顺带修掉）；本批补余 4 条——#11 mcpserver / #13
      adapter_remote 包文档归位（隔开竞争性 package 注释）、P2-9 dup 回查
      Warn 留痕、P2-10 三处 WalkDir 口径注释。
- [x] A4 ptc `Result.Describe()` 划账（〔已闭环 v6.3.1〕，ptc.go:158 实锤）。
- [x] A5 ggp v0.127→v0.134 **升级成功**（非退回）：连环断三环——pkg v0.48 断
      guardx→升 guardx v0.25.0；再断 goagent-kernel egress（= #134 门，
      **#134 已 2026-11-13 CLOSED**）→升 goagent-kernel v0.35.0 解锁。
      终态七依赖齐升（见 tech-debt 批 A 节），全量测试零失败。
- [x] A6 #26 验收实测（本地检出模式三连）：①首次安装 ✅ ②幂等：双目标
      `diff -r` 一致 + 同目标重装文件集 md5 不变（无残留）✅ ③篡改
      checksums → exit=1 拒装（want/got 报文正确）✅。
- [x] A7 收尾：三门禁 + 按单拆 commit。

## 批 B+ 明细（随推进补写）

（每批启动前在此追加明细；完成后勾账并回写状态列。）

## 验收总口径

每批完成 = 批内明细全勾 + 三门禁绿（`make lint && make test && make arch`；
涉评测批加 `make eval`）。E/F/G 三批各出独立 spec 后再入本清单状态列。
