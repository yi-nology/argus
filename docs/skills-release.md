# Skills/MCP 发布管理（P3-01，issue #26）

> 状态：骨架落地（固定 Release 版本安装 + sha256 校验和 + 兼容性声明 + 幂等安装门禁）。
> 关联：安装使用面见 https://yi-nology.github.io/argus/skills.html 与
> `scripts/install-skills.sh --help`。

## 发布物与校验单源

| 发布物 | 内容 | 校验 |
|---|---|---|
| `skills/argus-mcp` | MCP 驱动主指南 + references/tools.md | `skills/checksums.txt` |
| `skills/argus-triage` | 审查意见分诊 | 同上 |
| `skills/argus-precommit` | 提交前预审 | 同上 |

- `skills/checksums.txt`：sha256 清单（`sha256sum -c` 兼容格式），由
  `scripts/release-skills.sh` 生成——**改技能文件必须再生成**，漂移被
  `test/skills` 门禁硬拦（清单缺文件/树缺清单项/sha 不符三向守卫）。
- 安装侧（`install-skills.sh`）以清单为校验单源：先全量取到临时目录 → 逐文件
  sha256 核对 → 通过后原子落位（失败不产生半装残留）。

## 发布流程（固定 Release 版本）

1. 修改技能文件（SKILL.md / references）。
2. 仓库根跑 `./scripts/release-skills.sh` 再生成 `skills/checksums.txt`。
3. 发布 PR（含技能改动 + checksums）；CI `test/skills` 门禁绿。
4. 打 Release tag（vX.Y.Z，与 argus 服务发版同节点——skills 随仓 tag 发布）。
5. 用户侧固定版本安装：

   ```bash
   ./scripts/install-skills.sh --version v6.16.0
   # 或管道形态
   curl -fsSL https://raw.githubusercontent.com/yi-nology/argus/main/scripts/install-skills.sh | bash -s -- --version v6.16.0
   ```

   `--version <tag>` 从该 tag 的 raw ref 拉取技能与校验和清单（旧 tag 无清单时
   告警跳过，建议固定版本安装）；不带 `--version` = 跟踪 main 最新。

## 兼容性声明

每个可安装 SKILL.md 的 front matter 必须声明适配的 argus 服务版本区间：

```yaml
---
name: argus-mcp
description: ...
compatibility: ">=6.9.0 <7.0.0"  # 适配的 argus 服务版本区间
---
```

- 契约测试 `TestSKILLFrontMatterCompat`：name=目录名、description 非空、
  compatibility 可解析（`>=X.Y.Z <A.B.C` 区间形态），缺失即红。
- 口径：skills 面消费的 MCP 工具/语义在区间下限已定型；上限 = 下一个不兼容
  大版本。服务端版本查询走 `/version` 端点。

## 幂等安装（可重复、无残留）

- 安装顺序 = 临时目录全量取回 → 校验 → 原子落位（先清目标技能目录再复制）。
  重复执行覆盖更新，`test/skills` 本地模式回归锁定（含意外残留检测）。
- 仓库检出内执行 = 本地复制（离线/内网），校验和按检出树清单核对；
  `--version` 与本地模式互斥（固定 Release 必须走远端 ref——本地树不是发布物）。

## 完成度边界（骨架口径）

- 已落地：checksums 生成/校验脚本、安装器 `--version`/`--no-verify`、兼容性
  声明契约 + 门禁测试、本地模式幂等安装回归、本发布流程文档。
- 未含（后续批次）：release workflow 自动产出 binaries+checksums 的接线
  （既有发布基建复用点）、skills.html 宣传页的安装命令更新（`--version`
  示例段）、远端 ref 安装的在线回归（需网络，CI 不跑）。
