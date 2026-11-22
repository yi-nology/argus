#!/usr/bin/env bash
# release.sh —— 从 argus-service 构建发布二进制（linux amd64/arm64）。
#
# 用法：
#   scripts/release.sh v8.2.0 [out-dir]
# 前置：
#   git clone git@github.com:yi-nology/argus-service.git ../argus-service
#   （或设 ARGUS_SERVICE_DIR）
set -euo pipefail

VERSION="${1:?用法: scripts/release.sh vX.Y.Z [out-dir]}"
OUT="${2:-dist}"
HERE="$(cd "$(dirname "$0")/.." && pwd)"
SRC="${ARGUS_SERVICE_DIR:-$HERE/../argus-service}"

if [[ ! -f "$SRC/Makefile" ]]; then
  echo "✗ 未找到 argus-service 源码：$SRC" >&2
  echo "  git clone git@github.com:yi-nology/argus-service.git \"$SRC\"" >&2
  exit 1
fi

# OUT 转绝对路径：make build 会 cd 进 $SRC，相对 dist 会在源码树下找不着
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
GIT_COMMIT="$(git -C "$SRC" rev-parse --short HEAD 2>/dev/null || echo unknown)"
BUILD_TIME="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"

echo "== 构建 argus $VERSION（commit=$GIT_COMMIT）=="
cd "$SRC"
for goarch in amd64 arm64; do
  echo "-- linux/$goarch"
  make build \
    VERSION="$VERSION" \
    GIT_COMMIT="$GIT_COMMIT" \
    GIT_BRANCH="main" \
    BUILD_TIME="$BUILD_TIME" \
    GOOS=linux GOARCH="$goarch"
  cp bin/argus "$OUT/argus_linux_${goarch}"
done

( cd "$OUT" && sha256sum argus_linux_amd64 argus_linux_arm64 > checksums.txt )

# SBOM（go modules 清单）
{
  echo "{"
  echo "  \"tool\": \"argus-release\","
  echo "  \"version\": \"$VERSION\","
  echo "  \"commit\": \"$GIT_COMMIT\","
  echo "  \"modules\": ["
  ( cd "$SRC" && go list -m -f '    {"path":"{{.Path}}","version":"{{.Version}}"}' all ) | sed '$s/,$//'
  echo "  ]"
  echo "}"
} > "$OUT/sbom.json"

echo
echo "✓ 产物："
ls -la "$OUT"
echo
echo "校验： (cd $OUT && sha256sum -c checksums.txt)"
