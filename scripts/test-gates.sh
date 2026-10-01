#!/usr/bin/env bash
# 门禁脚本的自测（D-055）：底层工具（go list、gofmt、grep）出错时，门禁必须失败，不能报 OK。
# 用一个会失败的假 go / gofmt 替换 PATH 里的真工具来模拟；只测失败路径，正常路径由 make ci 本身覆盖。
set -euo pipefail
cd "$(dirname "$0")/.."
root=$(pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"

# 假 go：`go list -m` 照常回模块名，其余 go list 失败（模拟编译不过、缓存不可写）
cat >"$tmp/bin/go" <<'STUB'
#!/bin/sh
if [ "$1" = "list" ] && [ "$2" = "-m" ]; then echo github.com/goalladmin/goalladmin/server; exit 0; fi
if [ "$1" = "env" ]; then exit 0; fi
echo "fake go: failing on purpose" >&2
exit 42
STUB
# 假 gofmt：解析出错
printf '#!/bin/sh\necho "fake gofmt: failing on purpose" >&2\nexit 2\n' >"$tmp/bin/gofmt"
chmod +x "$tmp/bin/go" "$tmp/bin/gofmt"

fail=0
expect_fail() {
  local name=$1
  shift
  if "$@" >"$tmp/out" 2>&1; then
    echo "test-gates: $name 在工具出错时报了成功："; sed 's/^/  /' "$tmp/out"; fail=1
  else
    echo "test-gates: $name 在工具出错时失败 ✓"
  fi
}

expect_fail depcheck env PATH="$tmp/bin:$PATH" bash scripts/depcheck.sh
expect_fail go-licenses env PATH="$tmp/bin:$PATH" sh scripts/go-licenses.sh server "$tmp/licenses.txt"
expect_fail fmt-check env PATH="$tmp/bin:$PATH" make -s -C "$root" fmt-check
printf '(\n' >"$tmp/bad-words.txt" # 写错的正则：grep 出错（退出码 2），不能当成"没命中"
expect_fail banned-words env BANNED_WORDS_FILE="$tmp/bad-words.txt" bash scripts/banned-words.sh

[ "$fail" -eq 0 ] || { echo "test-gates: FAILED"; exit 1; }
echo "test-gates: OK"
