#!/bin/sh
# nginx server_name 只接受三个不同的普通域名，不接受通配符、端口或指令。
set -eu
check_host() {
  host=$1
  case "$host" in ''|*[!a-z0-9.-]*) echo 'invalid portal hostname' >&2; exit 1 ;; esac
  [ ${#host} -le 253 ] && printf '%s\n' "$host" | LC_ALL=C awk '
    /^[a-z0-9.-]+$/ {
      n = split($0, labels, ".")
      for (i = 1; i <= n; i++) {
        if (length(labels[i]) < 1 || length(labels[i]) > 63 || labels[i] ~ /^-/ || labels[i] ~ /-$/) exit 1
      }
      valid = 1
    }
    END { if (!valid) exit 1 }
  ' || { echo 'portal hosts must be valid lowercase hostnames without ports' >&2; exit 1; }
}
check_host "${GA_PLATFORM_HOST:-}"
check_host "${GA_AGENT_HOST:-}"
check_host "${GA_MERCHANT_HOST:-}"
[ "$GA_PLATFORM_HOST" != "$GA_AGENT_HOST" ] &&
  [ "$GA_PLATFORM_HOST" != "$GA_MERCHANT_HOST" ] &&
  [ "$GA_AGENT_HOST" != "$GA_MERCHANT_HOST" ] || {
    echo 'portal hosts must be distinct' >&2
    exit 1
  }
