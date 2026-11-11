#!/usr/bin/env bash
# install-skills.sh —— Argus AI 技能一键安装
#
# 把 Argus 的 AI 客户端技能（argus-mcp / argus-triage / argus-precommit）装进支持
# Agent Skills 的 AI 客户端技能目录（Claude Code / ZCode / 其他）。幂等：重复执行
# 覆盖更新到 main 最新版。
#
# 一键安装（默认装到 ~/.claude/skills）：
#   curl -fsSL https://raw.githubusercontent.com/yi-nology/argus/main/scripts/install-skills.sh | bash
#
# 指定目标：
#   curl -fsSL .../install-skills.sh | bash -s -- --target zcode     # ~/.zcode/skills
#   curl -fsSL .../install-skills.sh | bash -s -- --target DIR      # 自定义目录
#
# 从仓库检出安装（离线/内网）：
#   ./scripts/install-skills.sh [--target ...]
#
# 装完后在 AI 客户端里说「用 Argus 审查这个 PR」即可触发；连接参数与 token
# 三档见 https://yi-nology.github.io/argus/skills.html
set -euo pipefail

RAW_BASE="https://raw.githubusercontent.com/yi-nology/argus/main/skills"
TARGET=""
# 每个技能 = 名称 + 文件清单（相对技能目录）
SKILL_NAMES=(argus-mcp argus-triage argus-precommit)
SKILL_FILES_argus_mcp="SKILL.md references/tools.md"
SKILL_FILES_argus_triage="SKILL.md"
SKILL_FILES_argus_precommit="SKILL.md"

while [ $# -gt 0 ]; do
  case "$1" in
    --target) TARGET="${2:?--target 需要目录名}"; shift 2 ;;
    -h|--help) sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "未知参数: $1（--help 看用法）" >&2; exit 2 ;;
  esac
done

if [ -z "$TARGET" ]; then TARGET="$HOME/.claude/skills"; fi
case "$TARGET" in
  claude) TARGET="$HOME/.claude/skills" ;;
  zcode)  TARGET="$HOME/.zcode/skills" ;;
esac

# 仓库检出内执行 → 本地复制（离线）；否则从 GitHub raw 拉取
SCRIPT_DIR="$(cd "$(dirname "$0")" 2>/dev/null && pwd || true)"
LOCAL_SKILLS=""
if [ -n "$SCRIPT_DIR" ] && [ -d "$SCRIPT_DIR/../skills/argus-mcp" ]; then
  LOCAL_SKILLS="$SCRIPT_DIR/../skills"
fi

fetch() { # $1=skill 目录名 $2=相对文件 $3=目标文件
  if [ -n "$LOCAL_SKILLS" ]; then
    cp "$LOCAL_SKILLS/$1/$2" "$3"
  else
    curl -fsSL "$RAW_BASE/$1/$2" -o "$3"
  fi
}

echo "==> Argus AI 技能安装 → $TARGET"
mkdir -p "$TARGET"
for skill in "${SKILL_NAMES[@]}"; do
  var="SKILL_FILES_${skill//-/_}"
  files="${!var}"
  for f in $files; do
    dest="$TARGET/$skill/$f"
    mkdir -p "$(dirname "$dest")"
    fetch "$skill" "$f" "$dest"
    echo "    $skill/$f"
  done
done

cat <<EOF

✅ 安装完成（3 个技能）：
  argus-mcp       MCP 驱动主指南（发起审查/结论/问答/修复，含 tools.md 参数级参考）
  argus-triage    审查意见分诊（修复/驳回/上报 + 历史核查）
  argus-precommit 提交前预审（review_diff 快审 / review_diff_async 闭环）

下一步：
  1. 接入 MCP（如 Claude Code）：
     claude mcp add --transport http argus http://<argus 地址>:8899/mcp \\
       --header "Authorization: Bearer <ci 档 token>"
  2. 对 AI 说：「用 Argus 审查 <owner/repo> 的 PR #7」
  详见 https://yi-nology.github.io/argus/skills.html
EOF
