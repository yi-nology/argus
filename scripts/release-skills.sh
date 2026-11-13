#!/usr/bin/env bash
# release-skills.sh —— Skills 发布物校验和生成（P3-01，#26）
#
# 发布流程的固定环节：改技能文件 → 跑本脚本再生成 skills/checksums.txt →
# 随发布 PR 提交 → 打 tag。安装侧（install-skills.sh --version <tag>）以该
# 清单为 sha256 校验单源——清单与技能树不同步会被 test/skills 漂移门禁拦下。
#
# 用法（仓库根执行）：
#   ./scripts/release-skills.sh          # 再生成 skills/checksums.txt
#   ./scripts/release-skills.sh --check  # 只校验不写（CI/本地门禁同参）
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CHECKSUMS="$REPO_ROOT/skills/checksums.txt"
CHECK_ONLY=""

case "${1:-}" in
  --check) CHECK_ONLY=1 ;;
  "") ;;
  *) echo "用法: $0 [--check]" >&2; exit 2 ;;
  esac
if [ -n "$CHECK_ONLY" ]; then
  echo "校验模式（不写文件）"
fi

cd "$REPO_ROOT"

# sha256 口径单源：GNU sha256sum 优先，macOS 用 shasum -a 256。
if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | cut -d' ' -f1; }
else
  sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
fi

# 技能文件清单：与 install-skills.sh 的 SKILL_FILES_* 对齐（单源在这里再生成，
# 两处清单漂移由 test/skills 守卫）。
SKILL_FILES=(
  skills/argus-mcp/SKILL.md
  skills/argus-mcp/references/tools.md
  skills/argus-triage/SKILL.md
  skills/argus-precommit/SKILL.md
)

TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT
for f in "${SKILL_FILES[@]}"; do
  if [ ! -f "$f" ]; then
    # ${f} 花括号防 bash 3.2 + UTF-8 locale 把 $f（ 解析成变量名（见 install-skills.sh 同款注释）
    echo "清单文件缺失: ${f}（install-skills.sh 与实际技能树漂移）" >&2
    exit 1
  fi
  printf '%s  %s\n' "$(sha256 "$f")" "$f" >> "$TMP"
done

if [ -n "$CHECK_ONLY" ]; then
  if [ ! -f "$CHECKSUMS" ]; then
    echo "skills/checksums.txt 不存在（发布流程未跑 release-skills.sh）" >&2
    exit 1
  fi
  if diff -u "$CHECKSUMS" "$TMP" >/dev/null; then
    echo "✅ checksums.txt 与技能树一致"
  else
    echo "❌ checksums.txt 与技能树漂移（技能文件改了但未再生成校验和）：" >&2
    diff -u "$CHECKSUMS" "$TMP" | sed -n '3,20p' >&2
    exit 1
  fi
else
  mv "$TMP" "$CHECKSUMS"
  echo "✅ 已再生成 ${CHECKSUMS}："
  cat "${CHECKSUMS}"
fi
