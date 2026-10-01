#!/usr/bin/env bash
# 依赖方向检查（规范 §3.3、§16.1）：
#   1. server/core/**      不得 import server/modules/**
#   2. server/modules/<a>  不得 import server/modules/<b>（a != b）
#   3. server/core 之外的包不得 import server/core/internal/**（Go 编译器也会拦，这里提前给出清晰报错）
#   4. server/modules/**   除了自己模块内的包，只能 import server/core/**（不得 import 装配点 server、cmd/、migrations/ 等）
set -euo pipefail
cd "$(dirname "$0")/../server"

MOD=$(go list -m)
fail=0

# 先把依赖清单完整取出来：go list 失败（编译不过、缓存不可写）时这里就失败退出，
# 不能在进程替换里吞掉它的退出码、对着空清单报 OK（D-055）
listing=$(go list -f '{{.ImportPath}} {{join .Imports " "}}' ./...)
[ -n "$listing" ] || { echo "depcheck: go list 没有输出任何包"; exit 1; }

while IFS= read -r line; do
  pkg=${line%% *}
  imports=${line#* }
  [ "$pkg" = "$imports" ] && continue
  rel=${pkg#"$MOD"/}
  for imp in $imports; do
    case "$imp" in
      "$MOD"/*) ;;
      *) continue ;;
    esac
    irel=${imp#"$MOD"/}
    # 规则 1
    if [[ "$rel" == core/* && "$irel" == modules/* ]]; then
      echo "违反规则 1: $rel 依赖了业务模块 $irel"; fail=1
    fi
    # 规则 2
    if [[ "$rel" == modules/* && "$irel" == modules/* ]]; then
      a=${rel#modules/}; a=${a%%/*}
      b=${irel#modules/}; b=${b%%/*}
      if [ "$a" != "$b" ]; then
        echo "违反规则 2: 模块 $a 依赖了模块 $b（$rel → $irel）"; fail=1
      fi
    fi
    # 规则 3
    if [[ "$irel" == core/internal/* && "$rel" != core/* ]]; then
      echo "违反规则 3: $rel 依赖了框架内部包 $irel"; fail=1
    fi
    # 规则 4（根包的 rel 等于 MOD 本身，去前缀后不变；别的模块归规则 2）
    if [[ "$rel" == modules/* && "$irel" != core/* && "$irel" != modules/* ]]; then
      echo "违反规则 4: $rel 依赖了 $irel"; fail=1
    fi
  done
done <<<"$listing"

if [ "$fail" -ne 0 ]; then
  echo "depcheck: FAILED"; exit 1
fi
echo "depcheck: OK"
