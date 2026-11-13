#!/usr/bin/env bash
# install-skills.sh —— Argus AI 技能一键安装
#
# 把 Argus 的 AI 客户端技能（argus-mcp / argus-triage / argus-precommit）装进支持
# Agent Skills 的 AI 客户端技能目录（Claude Code / ZCode / 其他）。幂等：重复执行
# 覆盖更新。
#
# 一键安装（默认装 main 最新版到 ~/.claude/skills）：
#   curl -fsSL https://raw.githubusercontent.com/yi-nology/argus/main/scripts/install-skills.sh | bash
#
# 固定 Release 版本（#26 发布管理：可重复安装，推荐生产用法）：
#   curl -fsSL .../install-skills.sh | bash -s -- --version v7.0.0
#   ./scripts/install-skills.sh --version v7.0.0 --target zcode
#
# sha256 校验（#26：装完对 release 附带的 skills-checksums.txt 逐文件核验；
# --version 时自动从同 Release 拉校验清单）：
#   ./scripts/install-skills.sh --version v7.0.0 --verify
#
# 指定目标：
#   ./scripts/install-skills.sh --target zcode     # ~/.zcode/skills
#   ./scripts/install-skills.sh --target DIR      # 自定义目录
#
# 从仓库检出安装（离线/内网；--version 对本地检出无效，装的就是检出内容）：
#   ./scripts/install-skills.sh [--target ...] [--verify]
#
# 装完后在 AI 客户端里说「用 Argus 审查这个 PR」即可触发；连接参数与 token
# 三档见 https://yi-nology.github.io/argus/skills.html
set -euo pipefail

RAW_REPO="https://raw.githubusercontent.com/yi-nology/argus"
REL_REPO="https://github.com/yi-nology/argus/releases/download"
REF="main"          # --version 固定的 git ref（tag）
TARGET=""
VERIFY=0            # --verify 装后 sha256 核验
# 每个技能 = 名称 + 文件清单（相对技能目录）
SKILL_NAMES=(argus-mcp argus-triage argus-precommit)
SKILL_FILES_argus_mcp="SKILL.md references/tools.md"
SKILL_FILES_argus_triage="SKILL.md"
SKILL_FILES_argus_precommit="SKILL.md"

while [ $# -gt 0 ]; do
  case "$1" in
    --target) TARGET="${2:?--target 需要目录名}"; shift 2 ;;
    --version) REF="${2:?--version 需要 tag（如 v7.0.0）}"; shift 2 ;;
    --verify) VERIFY=1; shift ;;
    -h|--help) sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "未知参数: $1（--help 看用法）" >&2; exit 2 ;;
  esac
done

RAW_BASE="$RAW_REPO/$REF/skills"
if [ -z "$TARGET" ]; then TARGET="$HOME/.claude/skills"; fi
case "$TARGET" in
  claude) TARGET="$HOME/.claude/skills" ;;
  zcode)  TARGET="$HOME/.zcode/skills" ;;
esac

# 仓库检出内执行 → 本地复制（离线）；否则从 GitHub raw 拉取。
# --version 且在检出内：本地内容可能超前于 tag，显式警告（校验面同样跳过）。
SCRIPT_DIR="$(cd "$(dirname "$0")" 2>/dev/null && pwd || true)"
LOCAL_SKILLS=""
if [ -n "$SCRIPT_DIR" ] && [ -d "$SCRIPT_DIR/../skills/argus-mcp" ]; then
  LOCAL_SKILLS="$SCRIPT_DIR/../skills"
  if [ "$REF" != "main" ]; then
    echo "⚠️  本地检出安装：--version $REF 被忽略（装的是检出内容，非 tag 内容）" >&2
    REF="local"
    VERIFY=0
  fi
fi

fetch() { # $1=skill 目录名 $2=相对文件 $3=目标文件
  if [ -n "$LOCAL_SKILLS" ]; then
    cp "$LOCAL_SKILLS/$1/$2" "$3"
  else
    curl -fsSL "$RAW_BASE/$1/$2" -o "$3"
  fi
}

echo "==> Argus AI 技能安装 → $TARGET（ref=$REF）"
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

# sha256 核验（#26）：清单 = Release asset skills-checksums.txt（相对 skills/ 的
# 路径形态，release workflow 与本地 make skills-checksums 同口径生成）。
if [ "$VERIFY" = "1" ]; then
  if [ -n "$LOCAL_SKILLS" ]; then
    echo "==> sha256 校验（本地检出）"
    (cd "$LOCAL_SKILLS" && sha256sum -c --quiet "$LOCAL_SKILLS/../scripts/skills-checksums.txt" 2>/dev/null) || {
      # 清单缺失时本地重建再核（开发态自洽）
      make -C "$(dirname "$LOCAL_SKILLS")" skills-checksums >/dev/null
      (cd "$LOCAL_SKILLS" && sha256sum -c --quiet "../scripts/skills-checksums.txt")
    }
    echo "    ✅ 本地检出校验通过"
  else
    if [ "$REF" = "main" ]; then
      echo "❌ --verify 需要 --version <tag>（main 无固定校验清单）" >&2
      exit 3
    fi
    echo "==> sha256 校验（$REF release 清单）"
    tmp="$(mktemp)"
    trap 'rm -f "$tmp"' EXIT
    curl -fsSL "$REL_REPO/$REF/skills-checksums.txt" -o "$tmp"
    # 清单路径相对 skills/；逐文件核对已装内容
    fail=0
    while read -r sum path; do
      [ -z "${path:-}" ] && continue
      case "$path" in \#*) continue ;; esac
      got="$(sha256sum "$TARGET/$path" 2>/dev/null | awk '{print $1}')" || true
      if [ "$got" != "$sum" ]; then
        echo "    ❌ $path 校验失败（期望 $sum 实得 ${got:-缺失}）" >&2
        fail=1
      else
        echo "    ✅ $path"
      fi
    done < "$tmp"
    if [ "$fail" != "0" ]; then
      echo "❌ sha256 校验未通过——安装内容与 $REF Release 不一致" >&2
      exit 4
    fi
    echo "    ✅ 全部校验通过（$REF）"
  fi
fi

cat <<EOF

✅ 安装完成（3 个技能，ref=$REF）：
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
