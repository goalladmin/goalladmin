#!/bin/sh
# 168：域名配置必须在模板展开前拒绝重复、通配符、控制字符和非法标签。
set -eu
cd "$(dirname "$0")/.."
check() {
  GA_PLATFORM_HOST=$1 GA_AGENT_HOST=$2 GA_MERCHANT_HOST=$3 sh deploy/15-check-portal-hosts.sh
}
check platform.localhost agent.localhost merchant.localhost
check platform.example.com agent.example.com merchant.example.com
for bad in '' '*.example.com' 'UPPER.example.com' 'host:80' 'a b' 'a;b' 'a_b' '.a' 'a.' 'a..b' '-a.b' 'a-.b' "$(printf 'a\nb')" "$(printf '%064d' 0)"; do
  if check "$bad" agent.localhost merchant.localhost >/dev/null 2>&1; then
    echo 'test-portal-hosts: accepted an invalid host' >&2
    exit 1
  fi
done
for duplicate in 'platform.localhost platform.localhost merchant.localhost' \
  'platform.localhost agent.localhost platform.localhost' \
  'platform.localhost agent.localhost agent.localhost'; do
  # 三个固定值按空格分开，不接受调用者输入。
  # shellcheck disable=SC2086
  if check $duplicate >/dev/null 2>&1; then
    echo 'test-portal-hosts: accepted duplicate hosts' >&2
    exit 1
  fi
done
echo 'test-portal-hosts: OK'
