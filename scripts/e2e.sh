#!/usr/bin/env bash
# 前端冒烟测试（规范 §13.3）：起真实的平台、代理商、商户三个后端程序，建超管，跑 Playwright。
# 三个端的前端由 Playwright 各起一个 vite preview（apps/platform/playwright.config.ts），分别反代到这三个程序。
#
# 需要：Go、Node（pnpm 或 npm 都行）、可用的 MySQL（默认用 GA_TEST_DSN 指向的库，与 make test 相同；
# 表会迁移到位，测试数据用带时间戳的账号名，不清库）。
# Playwright 的浏览器需要先装一次：cd web/apps/platform && npx playwright install chromium
set -euo pipefail
cd "$(dirname "$0")/.."

DSN=${GA_E2E_DSN:-${GA_TEST_DSN:-ga:ga_test_pw@tcp(127.0.0.1:3307)/goalladmin_test?charset=utf8mb4&parseTime=true&loc=UTC&multiStatements=true}}
PORT=${GA_E2E_API_PORT:-18080}
AGENT_PORT=${GA_E2E_AGENT_API_PORT:-18081}
MERCHANT_PORT=${GA_E2E_MERCHANT_API_PORT:-18082}

# 从 DSN 拆出 user、password、host、port、name
creds=${DSN%%@*}; rest=${DSN#*@}
export GA_DB_USER=${creds%%:*} GA_DB_PASSWORD=${creds#*:}
hostport=${rest#tcp(}; hostport=${hostport%%)*}
export GA_DB_HOST=${hostport%%:*} GA_DB_PORT=${hostport##*:}
name=${rest#*/}; export GA_DB_NAME=${name%%\?*}
export GA_DB_PARAMS=${name#*\?}
export GA_JWT_SECRET_PLATFORM=${GA_JWT_SECRET_PLATFORM:-e2e-only-secret-0123456789abcdef0123456789}
export GA_JWT_SECRET_AGENT=${GA_JWT_SECRET_AGENT:-e2e-only-agent-secret-0123456789abcdef012345}
export GA_JWT_SECRET_MERCHANT=${GA_JWT_SECRET_MERCHANT:-e2e-only-merchant-secret-0123456789abcdef01}
export GA_SERVER_MODE=debug GA_LOG_LEVEL=warn GA_MIGRATE_AUTO=false
export GA_CONFIG=""

dir=$(mktemp -d)
echo "==> build server, agent, merchant"
(cd server && go build -o "$dir/server" . && go build -o "$dir/agent" ./cmd/agent && go build -o "$dir/merchant" ./cmd/merchant)
echo "==> migrate"
"$dir/server" migrate up >/dev/null
stamp=$(date +%s)
admin="e2e_admin_$stamp"
echo "==> create admin $admin"
pwd_line=$("$dir/server" admin create -username "$admin" | tail -1)
export GA_E2E_ADMIN=$admin GA_E2E_PASSWORD=${pwd_line##*：}
# 代理商端、商户端的冒烟用另一个超管：平台的冒烟会改第一个超管的密码，两份用例不必互相等
pwd_line=$("$dir/server" admin create -username "${admin}_p" | tail -1)
export GA_E2E_ADMIN2=${admin}_p GA_E2E_PASSWORD2=${pwd_line##*：}

pids=()
trap 'kill "${pids[@]}" 2>/dev/null || true' EXIT
start() { # start <程序> <端口>
  echo "==> serve $1 on 127.0.0.1:$2"
  GA_SERVER_ADDR=127.0.0.1:$2 "$dir/$1" serve &
  pids+=($!)
  for _ in $(seq 1 50); do
    curl -sf "http://127.0.0.1:$2/healthz" >/dev/null && return 0
    sleep 0.2
  done
  echo "$1 没有起来"; exit 1
}
start server "$PORT"
start agent "$AGENT_PORT"
start merchant "$MERCHANT_PORT"
export GA_API_TARGET="http://127.0.0.1:$PORT"
export GA_E2E_AGENT_API="http://127.0.0.1:$AGENT_PORT" GA_E2E_MERCHANT_API="http://127.0.0.1:$MERCHANT_PORT"
echo "==> playwright"
(cd web && npm run e2e -w @ga/platform -- "$@")
