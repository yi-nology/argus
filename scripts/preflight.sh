#!/usr/bin/env bash
# preflight.sh —— 部署前/升级后环境自检（对齐 MergePilot preflight 思路）。
# 退出码：0=通过（允许 warn）；1=存在 blocking 失败。
# 用法：scripts/preflight.sh [--live]
#   默认只查本地配置与二进制；--live 追加 HTTP 探针（需 ARGUS_ADDR）。
set -euo pipefail

LIVE=0
[[ "${1:-}" == "--live" ]] && LIVE=1

PASS=0
WARN=0
FAIL=0

ok()   { PASS=$((PASS+1)); printf '  \033[32mPASS\033[0m %s\n' "$1"; }
warn() { WARN=$((WARN+1)); printf '  \033[33mWARN\033[0m %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  \033[31mFAIL\033[0m %s\n' "$1"; }

echo "== Argus preflight =="

# 1. 二进制 / 构建
if [[ -x ./argus ]] || command -v argus >/dev/null 2>&1; then
  ok "argus 二进制可执行"
else
  warn "未发现 ./argus（make build 后再跑）"
fi

# 2. 配置文件
if [[ -f plugins.yaml ]]; then
  ok "plugins.yaml 存在"
else
  warn "plugins.yaml 缺失（将用内置默认）"
fi

# 3. 必需 env（静态 token 或引导 admin 至少其一 —— 鉴权 fail-closed）
if [[ -n "${ARGUS_APITOKEN:-}" || -n "${ARGUS_API_TOKENS:-}" ]]; then
  ok "静态 API token 已配置"
else
  warn "ARGUS_APITOKEN/ARGUS_API_TOKENS 为空：🔒 路由依赖会话登录（需 bootstrap admin）"
fi
if [[ -n "${ARGUS_BOOTSTRAP_ADMIN_USER:-}" && -n "${ARGUS_BOOTSTRAP_ADMIN_PASS:-}" ]]; then
  ok "引导 admin env 已设置"
else
  warn "未设 ARGUS_BOOTSTRAP_ADMIN_*（已有用户表则不需要）"
fi

# 4. 平台凭据（至少一项）
PLAT=0
for v in ARGUS_GITEA_TOKEN ARGUS_GITHUB_TOKEN ARGUS_GITLAB_TOKEN ARGUS_GITEE_TOKEN \
         ARGUS_GITCODE_TOKEN ARGUS_TENCENT_CODE_TOKEN; do
  [[ -n "${!v:-}" ]] && PLAT=1
done
if [[ -n "${ARGUS_GITHUB_APP_ID:-}" && -n "${ARGUS_GITHUB_APP_PRIVATE_KEY:-}" ]]; then
  PLAT=1
fi
if [[ $PLAT -eq 1 ]]; then
  ok "至少一项平台凭据已配置"
else
  bad "无任何平台凭据（ARGUS_<PLATFORM>_TOKEN 或 GitHub App 三件套）"
fi

# 5. LLM
if [[ -n "${OPENAI_API_KEY:-}" || -n "${OPENAI_BASE_URL:-}" ]]; then
  ok "LLM 端点 env 存在"
else
  warn "OPENAI_* 未配置：审查/Q&A 将不可用"
fi

# 6. 修复环一致性
if [[ -n "${ARGUS_FIX_PUSH:-}" && -z "${ARGUS_FIX_VERIFY_CMD:-}" ]]; then
  bad "ARGUS_FIX_PUSH 已设但 ARGUS_FIX_VERIFY_CMD 为空（push 环依赖验证环）"
elif [[ -n "${ARGUS_FIX_PUSH:-}" ]]; then
  ok "修复推送环配置一致"
fi
if [[ "${ARGUS_FIX_PUSH:-}" == "confirm" ]]; then
  ok "FixPushMode=confirm（用户面两阶段）"
fi

# 7. 数据库文件可写（SQLite 形态）
if [[ -z "${ARGUS_DB_DSN:-}" || "${ARGUS_DB_DSN}" == *.db ]]; then
  DBF="${ARGUS_DB_DSN:-argus.db}"
  DIR=$(dirname "$DBF")
  if [[ -d "$DIR" && -w "$DIR" ]]; then
    ok "SQLite 目录可写: $DIR"
  else
    bad "SQLite 目录不可写: $DIR"
  fi
fi

# 8. live 探针
if [[ $LIVE -eq 1 ]]; then
  ADDR="${ARGUS_ADDR:-:8899}"
  ADDR="${ADDR#:}"
  BASE="http://127.0.0.1:${ADDR}"
  for path in /healthz /readyz /version; do
    code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$BASE$path" || echo 000)
    if [[ "$code" == "200" ]]; then
      ok "GET $path → 200"
    else
      bad "GET $path → $code"
    fi
  done
  if [[ -n "${ARGUS_APITOKEN:-}" ]]; then
    code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 \
      -H "Authorization: Bearer $ARGUS_APITOKEN" "$BASE/tasks?limit=1" || echo 000)
    if [[ "$code" == "200" ]]; then
      ok "GET /tasks（静态 token）→ 200"
    else
      bad "GET /tasks → $code"
    fi
  fi
fi

echo "== 结果: PASS=$PASS WARN=$WARN FAIL=$FAIL =="
if [[ $FAIL -gt 0 ]]; then
  exit 1
fi
exit 0
