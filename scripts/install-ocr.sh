#!/usr/bin/env bash
# 安装 OCR（alibaba/open-code-review）到 /opt/argus/bin/ocr —— design §8 供应链信任：
# pin 版本 + checksum 校验由部署脚本负责；运行期 Argus 只执行此路径的白名单二进制。
#
# 用法：
#   OCR_VERSION=v1.2.0 OCR_CHECKSUM=<sha256> ./scripts/install-ocr.sh
#   # 或离线：已有二进制时
#   OCR_BIN=/path/to/ocr ./scripts/install-ocr.sh
set -euo pipefail

DEST="${OCR_DEST:-/opt/argus/bin/ocr}"
REPO="${OCR_REPO:-https://github.com/alibaba/open-code-review}"
VERSION="${OCR_VERSION:-}"
CHECKSUM="${OCR_CHECKSUM:-}"

install_from_local_bin() {
  local src="$1"
  mkdir -p "$(dirname "$DEST")"
  install -m 0755 "$src" "$DEST"
  echo "已安装（本地二进制）: $DEST"
}

install_from_source() {
  # 从源码 pin commit 构建（commit 本身即版本锚点；产物 sha256 打印供基线留存）
  local commit="${VERSION:-main}"
  local tmp
  tmp="$(mktemp -d)"
  echo "克隆 $REPO @ $commit ..."
  git clone --depth 1 --branch "$commit" "$REPO" "$tmp/src" 2>/dev/null \
    || git clone "$REPO" "$tmp/src"
  git -C "$tmp/src" checkout --force "$commit"
  echo "构建中（需要 Go 1.21+）..."
  (cd "$tmp/src" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$tmp/ocr" ./...)
  mkdir -p "$(dirname "$DEST")"
  install -m 0755 "$tmp/ocr" "$DEST"
  echo "产物 sha256: $(shasum -a 256 "$DEST" | awk '{print $1}')"
  rm -rf "$tmp"
  echo "已安装（源码构建 @ $commit）: $DEST"
}

verify_checksum() {
  if [[ -n "$CHECKSUM" ]]; then
    echo "$CHECKSUM  $DEST" | shasum -a 256 -c - >/dev/null
    echo "checksum 校验通过: $CHECKSUM"
  else
    echo "⚠️  未设置 OCR_CHECKSUM：建议记录产物 sha256 并在下次安装时 pin 住（§8）"
    shasum -a 256 "$DEST"
  fi
}

main() {
  if [[ -n "${OCR_BIN:-}" ]]; then
    [[ -x "$OCR_BIN" ]] || { echo "OCR_BIN 不可执行: $OCR_BIN"; exit 1; }
    install_from_local_bin "$OCR_BIN"
  else
    install_from_source
  fi
  verify_checksum
  echo "验证: $DEST --help"
  if "$DEST" --help >/dev/null 2>&1; then
    echo "OCR 安装完成。"
    echo "  MCP 通道（推荐，v3.9）: ocrshim -ocr-bin $DEST（compose 侧挂 ./bin/ocr-amd64）"
    echo "  cli 回退通道: plugins.yaml cmd 填: [\"$DEST\", \"review\", \"--format\", \"json\"]"
    echo "  （注意：上游 v1.12+ 无 diff 子命令；range 审查由 ocrshim 按工作副本自动传 --from/--to）"
  fi
}

main "$@"
