#!/usr/bin/env bash
# 向 Forgejo/Gitea 仓库注册（或更新）Argus webhook —— 真实试点一键接入。
#
# 前置环境变量：
#   ARGUS_GITEA_TOKEN   必填，Forgejo Access Token（Settings → Applications，repo+issue 读写）
#   ARGUS_GITEA_SECRET  必填，webhook 签名密钥（与 Argus 启动配置同值）
# 可选：
#   ARGUS_GITEA_URL     默认 https://git.example.com
#   ARGUS_GITEA_SKIP_TLS  证书过期/自签时设 1（本机实测 git.example.com 证书已过期）
#   ARGUS_PUBLIC_URL    Argus 对 Forgejo 可达的地址，默认 http://<Mac内网IP>:8899
#
# 用法：./scripts/setup-webhook.sh <owner/repo>   例：./scripts/setup-webhook.sh enjoydream/argus-pilot
set -euo pipefail

REPO="${1:?用法: setup-webhook.sh <owner/repo>}"
TOKEN="${ARGUS_GITEA_TOKEN:?需要 ARGUS_GITEA_TOKEN}"
SECRET="${ARGUS_GITEA_SECRET:?需要 ARGUS_GITEA_SECRET}"
BASE="${ARGUS_GITEA_URL:-https://git.example.com}"
MAC_IP="$(ipconfig getifaddr en0 2>/dev/null || echo 127.0.0.1)"
WEBHOOK_URL="${ARGUS_PUBLIC_URL:-http://${MAC_IP}:8899}/webhook/gitea"

CURL=(curl -sS -m 15 -H "Authorization: token ${TOKEN}" -H "Content-Type: application/json")
if [[ "${ARGUS_GITEA_SKIP_TLS:-0}" == "1" ]]; then CURL+=(-k); fi

api() { "${CURL[@]}" "$@"; }

echo "==> 仓库: ${BASE}/${REPO}"
echo "==> webhook 回调: ${WEBHOOK_URL}"

# 幂等：已有同名 argus webhook 则更新，否则新建
EXISTING=$(api "${BASE}/api/v1/repos/${REPO}/hooks" | python3 -c "
import json,sys
try: hooks=json.load(sys.stdin)
except Exception: hooks=[]
for h in hooks:
    cfg=h.get('config',{})
    if 'argus' in cfg.get('url','').lower():
        print(h['id']); break
")
PAYLOAD=$(python3 - "$WEBHOOK_URL" "$SECRET" <<'PY'
import json, sys
url, secret = sys.argv[1], sys.argv[2]
print(json.dumps({
    "type": "gitea",
    "active": True,
    "branch_filter": "*",
    "events": ["pull_request_only"],
    "config": {"url": url, "content_type": "json", "secret": secret, "method": "POST"},
}))
PY
)

if [[ -n "$EXISTING" ]]; then
  api -X PUT -d "$PAYLOAD" "${BASE}/api/v1/repos/${REPO}/hooks/${EXISTING}" >/dev/null
  echo "==> 已更新既有 webhook (id=${EXISTING})"
else
  api -X POST -d "$PAYLOAD" "${BASE}/api/v1/repos/${REPO}/hooks" >/dev/null
  echo "==> 已新建 webhook"
fi

echo "==> 完成。验证方式："
echo "    1) Argus 日志出现 argus.webhook.accepted / skipped(+原因)"
echo "    2) Forgejo 仓库 → Webhook 设置页 → 测试投递 应显示绿色"
echo "    3) 在该仓库开/同步一个 PR，观察 PR 页出现的 Argus 报告评论"
