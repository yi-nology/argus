#!/usr/bin/env bash
# install-skills.sh —— Argus AI 技能一键安装
#
# 把 Argus 的 AI 客户端技能（argus-mcp / argus-triage / argus-precommit）装进支持
# Agent Skills 的 AI 客户端技能目录（Claude Code / ZCode / 其他）。幂等：重复执行
# 覆盖更新（sha256 校验通过后原地覆盖，无残留）。
#
# 一键安装（默认装到 ~/.claude/skills，跟踪 main 最新版）：
#   curl -fsSL https://raw.githubusercontent.com/yi-nology/argus/main/scripts/install-skills.sh | bash
#
# 固定 Release 版本安装（P3-01 #26：可重复安装，sha256 校验单源 skills/checksums.txt）：
#   ./scripts/install-skills.sh --version v6.16.0
#   curl -fsSL .../install-skills.sh | bash -s -- --version v6.16.0 --target zcode
#
# 指定目标：
#   ./scripts/install-skills.sh --target zcode     # ~/.zcode/skills
#   ./scripts/install-skills.sh --target DIR       # 自定义目录
#
# 跳过校验和（不推荐；仅调试）：
#   ./scripts/install-skills.sh --no-verify
#
# 从仓库检出安装（离线/内网）：
#   ./scripts/install-skills.sh [--target ...]
#   本地模式校验和按检出树 skills/checksums.txt 核对（缺文件时告警跳过）。
#
# 装完后在 AI 客户端里说「用 Argus 审查这个 PR」即可触发；连接参数与 token
# 三档见 https://yi-nology.github.io/argus/skills.html
set -euo pipefail

REPO_SLUG="yi-nology/argus"
VERSION=""        # 空 = main 最新；TAG 形态（如 v6.16.0）= 固定 Release
RAW_BASE=""
TARGET=""
NO_VERIFY=""

# 每个技能 = 名称 + 文件清单（相对技能目录）
SKILL_NAMES=(argus-mcp argus-triage argus-precommit)
SKILL_FILES_argus_mcp="SKILL.md references/tools.md"
SKILL_FILES_argus_triage="SKILL.md"
SKILL_FILES_argus_precommit="SKILL.md"

while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="${2:?--version 需要 tag 名（如 v6.16.0）}"; shift 2 ;;
    --target) TARGET="${2:?--target 需要目录名}"; shift 2 ;;
    --no-verify) NO_VERIFY=1; shift ;;
    -h|--help) sed -n '2,28p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "未知参数: $1（--help 看用法）" >&2; exit 2 ;;
  esac
done

REF="${VERSION:-main}"
RAW_BASE="https://raw.githubusercontent.com/${REPO_SLUG}/${REF}/skills"

if [ -z "$TARGET" ]; then TARGET="$HOME/.claude/skills"; fi
case "$TARGET" in
  claude) TARGET="$HOME/.claude/skills" ;;
  zcode)  TARGET="$HOME/.zcode/skills" ;;
esac

# 仓库检出内执行 → 本地复制（离线）；否则从 GitHub raw 拉取。
# --version 与本地模式互斥：固定 Release 安装必须走远端 ref（本地树不是发布物）。
SCRIPT_DIR="$(cd "$(dirname "$0")" 2>/dev/null && pwd || true)"
LOCAL_SKILLS=""
if [ -n "$SCRIPT_DIR" ] && [ -d "$SCRIPT_DIR/../skills/argus-mcp" ]; then
  LOCAL_SKILLS="$SCRIPT_DIR/../skills"
fi
if [ -n "$LOCAL_SKILLS" ] && [ -n "$VERSION" ]; then
  echo "==> --version 与仓库检出模式互斥：固定 Release 安装走远端 ref，忽略本地树" >&2
  LOCAL_SKILLS=""
fi

# sha256 口径单源：GNU sha256sum 优先，macOS 用 shasum -a 256。
if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | cut -d' ' -f1; }
else
  sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
fi

fetch() { # $1=skill 目录名 $2=相对文件 $3=目标文件
  if [ -n "$LOCAL_SKILLS" ]; then
    cp "$LOCAL_SKILLS/$1/$2" "$3"
  else
    curl -fsSL "$RAW_BASE/$1/$2" -o "$3"
  fi
}

# fetch_checksums 拉取发布物校验和清单（skills/checksums.txt，sha256sum -c 兼容
# 格式：<sha256>  skills/<skill>/<file>）。拉不到（旧 tag 无此文件）返回 1。
fetch_checksums() { # $1=目标临时文件
  if [ -n "$LOCAL_SKILLS" ]; then
    if [ -f "$LOCAL_SKILLS/checksums.txt" ]; then
      cp "$LOCAL_SKILLS/checksums.txt" "$1"
      return 0
    fi
    return 1
  fi
  curl -fsSL "$RAW_BASE/checksums.txt" -o "$1"
}

echo "==> Argus AI 技能安装 → ${TARGET}（ref: ${REF}）"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

# 先全量取到临时目录，校验通过后再落位（半装残留不会发生——失败即整体退出）
STAGE="$TMP_DIR/stage"
mkdir -p "$STAGE"
for skill in "${SKILL_NAMES[@]}"; do
  var="SKILL_FILES_${skill//-/_}"
  files="${!var}"
  for f in $files; do
    mkdir -p "$STAGE/$skill/$(dirname "$f")"
    fetch "$skill" "$f" "$STAGE/$skill/$f"
    echo "    取回 $skill/$f"
  done
done

# ---- sha256 校验（P3-01 #26）：清单存在即强制核对；--no-verify 显式豁免 ----
if [ -z "$NO_VERIFY" ]; then
  if fetch_checksums "$TMP_DIR/checksums.txt"; then
    echo "==> 校验和核对（skills/checksums.txt @ ${REF}）"
    while read -r want path; do
      [ -n "$path" ] || continue
      rel="${path#skills/}"
      [ -f "$STAGE/$rel" ] || { echo "校验失败：清单文件缺失 $path" >&2; exit 1; }
      got="$(sha256 "$STAGE/$rel")"
      if [ "$got" != "$want" ]; then
        # ${got} 必须花括号：bash 3.2 + UTF-8 locale 下 $got 紧跟全角括号会被
        # 解析成变量名 got）⇒ unbound；且 EXIT trap 会把该退出码洗成 0（静默装空）。
        echo "校验失败：$path sha256 不符（want=$want got=${got}）" >&2
        exit 1
      fi
    done < "$TMP_DIR/checksums.txt"
    echo "    全部文件 sha256 一致"
  else
    echo "    ⚠️  无校验和清单（${REF} 无 skills/checksums.txt）——跳过校验（建议固定 Release 版本安装）"
  fi
else
  echo "==> --no-verify：跳过 sha256 校验"
fi

# 校验通过 → 原子落位（先清目标技能目录再复制：重复安装无残留）
mkdir -p "$TARGET"
for skill in "${SKILL_NAMES[@]}"; do
  rm -rf "$TARGET/$skill"
  mkdir -p "$TARGET/$skill"
  cp -R "$STAGE/$skill/." "$TARGET/$skill/"
done

cat <<EOF

✅ 安装完成（3 个技能，ref ${REF}${VERSION:+，已固定 Release 版本}）：
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
