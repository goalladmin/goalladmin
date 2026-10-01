#!/usr/bin/env bash
# 前端冒烟测试（规范 §13.3）：起一个真实后端，建超管，跑 Playwright。
#
# 需要：Go、Node（pnpm 或 npm 都行）、可用的 MySQL（默认用 GA_TEST_DSN 指向的库，与 make test 相同；
# 表会迁移到位，测试数据用带时间戳的账号名，不清库）。
# Playwright 的浏览器需要先装一次：cd web/apps/platform && npx playwright install chromium
set -euo pipefail
cd "$(dirname "$0")/.."

DSN=${GA_E2E_DSN:-${GA_TEST_DSN:-ga:ga_test_pw@tcp(127.0.0.1:3307)/goalladmin_test?charset=utf8mb4&parseTime=true&loc=UTC&multiStatements=true}}
PORT=${GA_E2E_API_PORT:-18080}

# 从 DSN 拆出 user、password、host、port、name
creds=${DSN%%@*}; rest=${DSN#*@}
export GA_DB_USER=${creds%%:*} GA_DB_PASSWORD=${creds#*:}
hostport=${rest#tcp(}; hostport=${hostport%%)*}
export GA_DB_HOST=${hostport%%:*} GA_DB_PORT=${hostport##*:}
name=${rest#*/}; export GA_DB_NAME=${name%%\?*}
export GA_DB_PARAMS=${name#*\?}
export GA_JWT_SECRET_PLATFORM=${GA_JWT_SECRET_PLATFORM:-e2e-only-secret-0123456789abcdef0123456789}
export GA_SERVER_ADDR=127.0.0.1:$PORT GA_SERVER_MODE=debug GA_LOG_LEVEL=warn GA_MIGRATE_AUTO=false
export GA_CONFIG=""

bin=$(mktemp -d)/server
echo "==> build server"
(cd server && go build -o "$bin" .)
echo "==> migrate"
"$bin" migrate up >/dev/null
admin="e2e_admin_$(date +%s)"
echo "==> create admin $admin"
pwd_line=$("$bin" admin create -username "$admin" | tail -1)
export GA_E2E_ADMIN=$admin GA_E2E_PASSWORD=${pwd_line##*：}
echo "==> serve on $GA_SERVER_ADDR"
"$bin" serve &
server_pid=$!
trap 'kill $server_pid 2>/dev/null || true' EXIT
for _ in $(seq 1 50); do
  curl -sf "http://$GA_SERVER_ADDR/healthz" >/dev/null && break
  sleep 0.2
done
export GA_API_TARGET="http://$GA_SERVER_ADDR"
echo "==> playwright"
(cd web && npm run e2e -w @ga/platform -- "$@")
